# devops-demo

一个用 Go 写的 HTTP 服务，用于实践从「容器化」到「CI/CD → K8s 部署 → 监控」的完整交付链路。

> 本项目是个人技术转型的实战项目，每个阶段都会补充对应的工程实践与踩坑记录。

---

## 项目简介

`devops-demo` 是一个极简的 Go HTTP 服务，提供两个接口：

| 路径         | 作用                 |
| ---------- | ------------------ |
| `/`        | 返回服务版本、容器主机名、请求路径  |
| `/healthz` | 健康检查，固定返回 `200 ok` |

设计上有两个刻意的安排：

- **端口从环境变量 `PORT` 读取**（默认 `8080`）——为后续在 K8s 里通过 ConfigMap 注入配置做准备
- **`/healthz` 接口**——为后续 K8s 的 `livenessProbe` / `readinessProbe` 预留

`/` 返回的 `host` 是容器主机名（即容器 ID），这个设计在后续多副本部署时可以直接用来验证流量分发到了哪个 Pod。

---

## 架构

当前阶段（Week 1）：

```
                    ┌──────────────────────────────────┐
   浏览器  ────────▶│  nginx:1.24-alpine               │
   :8081            │  反向代理 / 容器网络内 DNS 解析    │
                    └───────────────┬──────────────────┘
                                    │ proxy_pass http://app:8080
                                    ▼
                    ┌──────────────────────────────────┐
                    │  devops-demo:slim                │
                    │  Go HTTP 服务（alpine + 静态二进制）│
                    └──────────────────────────────────┘
```

两个容器由 `docker compose` 统一编排，运行在 compose 自动创建的默认网络里，  
通过**服务名**（`app`）互相访问。

规划中的完整链路：

```
Git 提交 ─▶ Jenkins / GitLab CI ─▶ 构建镜像 ─▶ 推送镜像仓库
                                                    │
                                                    ▼
                              K8s 部署 ─▶ Prometheus + Grafana 监控
```

---

## 镜像优化：884MB → 14.1MB

这是本项目第一个可量化的成果。

| 镜像                  | 基础镜像          | 体积          | 构建耗时   |
| ------------------- | ------------- | ----------- | ------ |
| `devops-demo:naive` | `golang:1.21` | **884 MB**  | 70.9 s |
| `devops-demo:slim`  | `alpine:3.19` | **14.1 MB** | 12.3 s |

**体积缩小 62.7 倍，减少 98.4%。**

### 优化手段：多阶段构建

```dockerfile
FROM golang:1.21 AS builder
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 go build -o devops-demo .

FROM alpine:3.19
WORKDIR /app
COPY --from=builder /app/devops-demo .
EXPOSE 8080
CMD ["./devops-demo"]
```

关键在于：**最终镜像只包含最后一个阶段**。第一阶段（golang 的 Debian 系统、Go 工具链、  
gcc、源码、构建缓存）被整个丢弃。

优化后第二阶段的实际构成：

```
alpine:3.19 解压后        约 7.7 MB
编译产物 devops-demo      6,722,897 字节（约 6.4 MB）
─────────────────────────────────────
合计                      14.1 MB
```

验证方式：`docker exec <容器> ls -la /app` → 目录下**只有一个文件**。

### 这个数字在生产环境的意义

按一个 20 节点的集群估算：

| 指标     | 优化前       | 优化后     |
| ------ | --------- | ------- |
| 单节点拉取  | 884 MB    | 14.1 MB |
| 全量分发总量 | 约 17.3 GB | 282 MB  |

此外，884MB 镜像里的编译工具链和系统库本身就是漏洞扫描的重点，去掉后攻击面大幅缩小。

---

## 快速开始

### 构建镜像

```bash
# 多阶段构建（推荐）
docker build -t devops-demo:slim .

# 对比：朴素单阶段构建
docker build -f Dockerfile.naive -t devops-demo:naive .
```

### 单独运行

```bash
docker run -d --name demo -p 8080:8080 devops-demo:slim
curl localhost:8080
```

### 用 compose 起完整栈

```bash
docker compose up -d
curl localhost:8081        # 经 nginx 反向代理访问
docker compose down        # 停止并清理容器与网络（保留镜像）
```



---

## 目录结构

```
devops-demo/
├── main.go              # Go HTTP 服务
├── go.mod               # 模块定义（module devops-demo, go 1.21）
├── Dockerfile           # 多阶段构建
├── docker-compose.yml   # app + nginx 编排
└── nginx.conf           # nginx 反向代理配置
```

---

## 遇到的问题与解决

### 问题 1：nginx 容器启动后立即退出（`Exited (1)`）

**现象**

`docker compose up -d` 显示一切正常：

```
Network devops-demo_default  Created
Container devops-demo-app-1    Started
Container devops-demo-nginx-1  Started
```

但访问 8081 端口被拒绝：

```
$ curl localhost:8081
curl: (7) Failed connect to localhost:8081; 拒绝连接
```

**定位过程**

*第一步 · 确认容器的真实状态*

```
$ docker compose ps -a
NAME                  IMAGE               STATUS
devops-demo-app-1     devops-demo:slim    Up 2 minutes
devops-demo-nginx-1   nginx:alpine        Exited (1) 2 minutes ago
```

关键点：**`docker compose ps` 默认只显示运行中的容器**，必须加 `-a` 才能看到已经退出的  
nginx。不加这个参数，问题根本不可见。

*第二步 · 从日志里挑出信号*

```
$ docker logs devops-demo-nginx-1
...
[crit] 1#1: prwrite() "/run/nginx.pid" failed (1: Operation not permitted)
```

日志中 90% 是噪音，只盯 `[crit]` / `[emerg]` / `[error]` 级别和最后一行。

日志里还有一条：

```
10-listen-on-ipv6-by-default.sh: can not modify /etc/nginx/conf.d/default.conf (read-only file system?)
```

这条是**噪音**——它是我们自己用 `:ro` 只读挂载配置文件导致的，属于预期行为。

关键判断：报错是 `Operation not permitted`（EPERM），这是**系统调用被内核拒绝**，  
而不是配置语法错误。因此问题不在 compose 配置层面。

*第三步 · 隔离变量*

nginx 起不来有四种可能：SELinux 拦截、文件系统权限、自己的配置、镜像本身。  
逐个排除：

| # | 命令                                                                  | 结果          | 排除了什么      |
| - | ------------------------------------------------------------------- | ----------- | ---------- |
| 1 | `getenforce`                                                        | `Disabled`  | SELinux 拦截 |
| 2 | `docker run --rm alpine sh -c 'touch /run/t && echo ok'`            | `ok`        | 文件系统权限     |
| 3 | `docker run -d --name t-nginx -p 8082:80 nginx:alpine`（**不挂载任何配置**） | 报**一模一样**的错 | **自己的配置**  |

实验 3 是决定性的：完全不挂载自己的 `nginx.conf`，使用镜像自带的默认配置，照样报出相同的  
错误。**配置这个变量被彻底移除**，问题被锁定在镜像本身。

**根因**

`nginx:alpine`（1.31.6，由 gcc 15.2.0 构建）与 **CentOS 7 的内核 3.10** 不兼容。  
nginx 二进制在创建 pid 文件（`/run/nginx.pid`）时被内核拒绝，返回 EPERM。

**与本项目的 compose 配置完全无关。**

**解决**

将镜像换成较老的 tag：

```yaml
nginx:
  image: nginx:1.24-alpine
```

问题消失，`curl localhost:8081` 正常返回。

**止损原则**：环境级的怪问题，10 分钟内判断归属——确认不是自己的问题就绕过，不死磕。

**方法论收获**

1. `docker compose ps` 默认隐藏已退出的容器，排查必须加 `-a`
2. 日志里 90% 是噪音，只盯 `[crit]` / `[emerg]` / `[error]` 和最后一行
3. **隔离变量时，实验条件必须与真实现场一致**——最初用 `sh -c` 绕过 entrypoint 做的  
   测试绕过了镜像的启动流程，实验设计本身有缺陷，结论不可用，后来重做
4. 环境级怪问题要止损，不要死磕

---

### 问题 2：`docker build` 报 `requires exactly 1 argument`

**现象**

```
ERROR: "docker buildx build" requires exactly 1 argument
```

**根因**

Docker 23+ 中 `docker build` 已经是 `docker buildx build` 的别名，所以报错信息里会出现 `buildx`。  
真正的原因是命令末尾的**构建上下文路径**（`.`）没有被正确传入。

**解决**

确认命令末尾带有 `.`：

```bash
docker build -t devops-demo:slim .
```

---

## 后续计划

- [ ] Week 2：接入 Jenkins，实现代码提交后自动构建镜像
- [ ] Week 3：接入 GitLab CI，双流水线对比
- [ ] Week 3：部署到 K8s（Deployment / Service / Ingress）
- [ ] Week 4：接入 Prometheus + Grafana 监控
- [ ] 构建期通过 `-ldflags` 注入版本号（目前 `version` 恒为 `dev`）
- [ ] 补充 `.dockerignore`，减小构建上下文

---

## 环境说明

本项目在以下环境完成构建与验证：

| 项目     | 版本                          |
| ------ | --------------------------- |
| 操作系统   | CentOS Linux 7 (Core)       |
| 内核     | 3.10.0-1160.71.1.el7.x86_64 |
| Docker | 26.1.4                      |
