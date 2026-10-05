FROM golang:1.21 AS builder
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 go build -o devops-demo .

FROM alpine:3.19
WORKDIR /app
COPY --from=builder /app/devops-demo .
EXPOSE 8080
CMD ["./devops-demo"]
