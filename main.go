package main

import (
"fmt"
"log"
"net/http"
"os"
)

var version = "dev"

func main() {
port := os.Getenv("PORT")
if port == "" {
port = "8080"
}

hostname, err := os.Hostname()
if err != nil {
hostname = "unknown"
}

http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
fmt.Fprintf(w, "devops-demo\nversion: %s\nhost: %s\npath: %s\n",
version, hostname, r.URL.Path)
})

http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
w.WriteHeader(http.StatusOK)
fmt.Fprintln(w, "ok")
})

log.Printf("devops-demo v%s listening on :%s (host=%s)", version, port, hostname)
if err := http.ListenAndServe(":"+port, nil); err != nil {
log.Fatal(err)
}
}
