package main

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

func env(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func main() {
	memMiB := env("LOADGEN_MEM_MIB", 64)
	cpuPct := env("LOADGEN_CPU_PERCENT", 40)
	port := env("LOADGEN_PORT", 8080)

	held := make([]byte, memMiB<<20)
	for i := 0; i < len(held); i += 4096 {
		held[i] = byte(i>>12) | 1
	}
	slog.Info(fmt.Sprintf("loadgen holding %d MiB, burning %d%% of one core, listening on %d", memMiB, cpuPct, port))

	go burn(cpuPct)
	go func() {
		mux := http.NewServeMux()
		body := make([]byte, 64<<10)
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(body)
		})
		if err := http.ListenAndServe(net.JoinHostPort("", strconv.Itoa(port)), mux); err != nil {
			slog.Error("loadgen listen failed", "err", err)
		}
	}()
	go pollTarget(port)

	for count := 1; ; count++ {
		sum := 0
		for i := 0; i < len(held); i += 4096 {
			sum += int(held[i])
		}
		slog.Info(fmt.Sprintf("loadgen count=%d held_bytes=%d checksum=%d", count, len(held), sum))
		time.Sleep(10 * time.Second)
	}
}

func burn(pct int) {
	const slice = 100 * time.Millisecond
	busy := slice * time.Duration(pct) / 100
	x := 0
	for {
		start := time.Now()
		for time.Since(start) < busy {
			x = x*1103515245 + 12345
		}
		if x == 42 {
			slog.Info("loadgen unlikely value")
		}
		time.Sleep(slice - busy)
	}
}

func pollTarget(port int) {
	addr := os.Getenv("LOADGEN_TARGET_ADDR")
	if addr == "" {
		return
	}
	client := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://%s/", net.JoinHostPort(addr, strconv.Itoa(port)))
	failures := 0
	for {
		resp, err := client.Get(url)
		if err != nil {
			failures++
			if failures%20 == 1 {
				slog.Warn(fmt.Sprintf("loadgen poll %s failed", url), "err", err)
			}
		} else {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			failures = 0
		}
		time.Sleep(500 * time.Millisecond)
	}
}
