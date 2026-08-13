package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	backend := requireEnv("OPENDEPLOY_HTTP_BACKEND")
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/sse"):
			serveSSE(w, r, backend)
		case strings.HasSuffix(r.URL.Path, "/upload"):
			serveUpload(w, r, backend)
		case strings.HasSuffix(r.URL.Path, "/echo-upgrade"):
			serveUpgrade(w, r, backend)
		default:
			serveEcho(w, r, backend)
		}
	})
	server := &http.Server{Addr: "[::]:8080", Handler: mux}
	log.Printf("httpecho listening backend=%s address=%s", backend, server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}

func requireEnv(name string) string {
	value := os.Getenv(name)
	if value == "" {
		log.Fatalf("%s is required", name)
	}
	return value
}

func serveEcho(w http.ResponseWriter, r *http.Request, backend string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "backend=%s\n", backend)
	fmt.Fprintf(w, "method=%s\n", r.Method)
	fmt.Fprintf(w, "host=%s\n", r.Host)
	fmt.Fprintf(w, "path=%s\n", r.URL.Path)
	fmt.Fprintf(w, "query=%s\n", r.URL.RawQuery)
	fmt.Fprintf(w, "proto=%s\n", r.Proto)
	fmt.Fprintf(w, "x-forwarded-proto=%s\n", r.Header.Get("X-Forwarded-Proto"))
	fmt.Fprintf(w, "x-forwarded-prefix=%s\n", r.Header.Get("X-Forwarded-Prefix"))
	fmt.Fprintf(w, "x-forwarded-host=%s\n", r.Header.Get("X-Forwarded-Host"))
	fmt.Fprintf(w, "has-x-forwarded-for=%t\n", r.Header.Get("X-Forwarded-For") != "")
}

func serveSSE(w http.ResponseWriter, r *http.Request, backend string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	for i := 1; i <= 20; i++ {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
		fmt.Fprintf(w, "data: %s tick %d\n\n", backend, i)
		flusher.Flush()
	}
}

func serveUpload(w http.ResponseWriter, r *http.Request, backend string) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	n, err := io.Copy(io.Discard, r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("read body: %v", err), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "backend=%s\nreceived=%d\n", backend, n)
}

func serveUpgrade(w http.ResponseWriter, r *http.Request, backend string) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "opendeploy-echo") {
		http.Error(w, "upgrade to opendeploy-echo required", http.StatusUpgradeRequired)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		log.Printf("hijack: %v", err)
		return
	}
	defer conn.Close()
	fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: opendeploy-echo\r\nConnection: Upgrade\r\n\r\n")
	if err := rw.Flush(); err != nil {
		return
	}
	scanner := bufio.NewScanner(rw)
	for scanner.Scan() {
		fmt.Fprintf(rw, "echo:%s backend=%s\n", scanner.Text(), backend)
		if err := rw.Flush(); err != nil {
			return
		}
	}
}
