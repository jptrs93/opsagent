package main

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jptrs93/goutil/logu"
)

func main() {
	slog.SetDefault(logu.NewJSONLogger(os.Stdout, slog.LevelInfo))
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
		case strings.HasSuffix(r.URL.Path, "/headers"):
			serveHeaders(w, r, backend)
		case strings.HasSuffix(r.URL.Path, "/setheaders"):
			serveSetHeaders(w, r, backend)
		default:
			serveEcho(w, r, backend)
		}
	})
	addr := "[::]:8080"
	if port := os.Getenv("PORT"); port != "" {
		addr = "[::]:" + port
	}
	server := &http.Server{Addr: addr, Handler: mux}
	logf("httpecho listening backend=%s address=%s", backend, server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fatalf("serve: %v", err)
	}
}

func requireEnv(name string) string {
	value := os.Getenv(name)
	if value == "" {
		fatalf("%s is required", name)
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

// serveHeaders dumps every request header the backend received, one
// `header:<lower-name>=<values>` line per header with multiple values joined
// by "|", so tests can assert exactly what the proxy forwarded.
func serveHeaders(w http.ResponseWriter, r *http.Request, backend string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "backend=%s\n", backend)
	fmt.Fprintf(w, "host=%s\n", r.Host)
	fmt.Fprintf(w, "method=%s\n", r.Method)
	fmt.Fprintf(w, "content-length=%d\n", r.ContentLength)
	names := make([]string, 0, len(r.Header))
	for name := range r.Header {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "header:%s=%s\n", strings.ToLower(name), strings.Join(r.Header[name], "|"))
	}
}

// serveSetHeaders sets response headers from repeated `h=Name:Value` query
// params (Add semantics, so repeated names become multi-value headers) and
// echoes what it set so tests can tell a header the proxy stripped from one
// the backend never sent.
func serveSetHeaders(w http.ResponseWriter, r *http.Request, backend string) {
	set := []string{}
	for _, pair := range r.URL.Query()["h"] {
		name, value, ok := strings.Cut(pair, ":")
		if !ok {
			http.Error(w, "h must be Name:Value", http.StatusBadRequest)
			return
		}
		w.Header().Add(name, value)
		set = append(set, pair)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "backend=%s\n", backend)
	fmt.Fprintf(w, "set=%s\n", strings.Join(set, ","))
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
		logf("hijack: %v", err)
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

func logf(format string, args ...any) {
	slog.Info(fmt.Sprintf(format, args...))
}

func fatalf(format string, args ...any) {
	slog.Error(fmt.Sprintf(format, args...))
	os.Exit(1)
}
