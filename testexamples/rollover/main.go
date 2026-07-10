package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.LUTC)
	generation := env("OPD_ROLLOVER_GENERATION", "initial")
	addr := env("OPD_ROLLOVER_ADDR", ":8080")
	readyPath := os.Getenv("OPENDEPLOY_READINESS_SOCK_PATH")
	bindBeforeReady := strings.ToLower(env("OPD_ROLLOVER_BIND_BEFORE_READY", "true")) != "false"
	readyDelay := envDurationMS("OPD_ROLLOVER_READY_DELAY_MS")

	log.Printf("rollover starting generation=%s addr=%s readyPath=%q bindBeforeReady=%t readyDelay=%s", generation, addr, readyPath, bindBeforeReady, readyDelay)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var server *http.Server
	if readyPath == "" || bindBeforeReady {
		server = listenAndServe(ctx, addr, generation)
	}

	if readyPath != "" {
		if readyDelay > 0 {
			log.Printf("rollover readiness delay generation=%s delay=%s", generation, readyDelay)
			select {
			case <-time.After(readyDelay):
			case <-ctx.Done():
				return
			}
		}
		if err := signalReady(readyPath); err != nil {
			log.Printf("rollover readiness signal failed generation=%s err=%v", generation, err)
			os.Exit(1)
		}
		log.Printf("rollover readiness sent generation=%s", generation)
		if server == nil {
			server = waitListenAndServe(ctx, addr, generation)
		}
	}

	<-ctx.Done()
	log.Printf("rollover stopping generation=%s", generation)
	if server != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}
}

func listenAndServe(ctx context.Context, addr string, generation string) *http.Server {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("rollover listen failed generation=%s addr=%s err=%v", generation, addr, err)
		os.Exit(1)
	}
	return serve(ctx, listener, generation)
}

func waitListenAndServe(ctx context.Context, addr string, generation string) *http.Server {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		listener, err := net.Listen("tcp", addr)
		if err == nil {
			return serve(ctx, listener, generation)
		}
		log.Printf("rollover waiting for port generation=%s addr=%s err=%v", generation, addr, err)
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return nil
		}
	}
}

func serve(ctx context.Context, listener net.Listener, generation string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintf(w, "rollover generation=%s\n", generation)
	})
	server := &http.Server{Handler: mux}
	log.Printf("rollover listen successful generation=%s addr=%s actual=%s", generation, listener.Addr().String(), listener.Addr().String())
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("rollover serve failed generation=%s err=%v", generation, err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	return server
}

func signalReady(path string) error {
	conn, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte("ready\n"))
	return err
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envDurationMS(key string) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return 0
	}
	ms, err := strconv.Atoi(value)
	if err != nil || ms < 0 {
		log.Printf("rollover invalid duration key=%s value=%q", key, value)
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}
