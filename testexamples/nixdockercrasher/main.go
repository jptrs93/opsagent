package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const crashCountPath = "/var/crashcount.txt"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	count, err := readCrashCount()
	if err != nil {
		logger.Error("nixdockercrasher read crash count error", "err", err)
		os.Exit(1)
	}

	if count >= 3 {
		logger.Info(fmt.Sprintf("nixdockercrasher crash count=%d; staying alive", count), "count", count)
		for tick := 1; ; tick++ {
			logger.Info("nixdockercrasher healthy", "tick", tick)
			time.Sleep(10 * time.Second)
		}
	}

	next := count + 1
	logger.Info(fmt.Sprintf("nixdockercrasher will crash number=%d after 2s", next), "number", next, "delay", 2*time.Second)
	time.Sleep(2 * time.Second)

	if err := writeCrashCount(next); err != nil {
		logger.Error("nixdockercrasher write crash count error", "err", err)
		os.Exit(1)
	}

	logger.Info(fmt.Sprintf("nixdockercrasher wrote crash number=%d to %s; exiting now", next, crashCountPath), "number", next, "path", crashCountPath)
	logger.Error(fmt.Sprintf("panic: nixdockercrasher panic crash count=%d", next), "count", next)
	os.Exit(2)
}

func readCrashCount() (int, error) {
	content, err := os.ReadFile(crashCountPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	text := strings.TrimSpace(string(content))
	if text == "" {
		return 0, nil
	}

	count, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", crashCountPath, err)
	}
	if count < 0 {
		return 0, fmt.Errorf("parse %s: negative crash count %d", crashCountPath, count)
	}
	return count, nil
}

func writeCrashCount(count int) error {
	if err := os.MkdirAll(filepath.Dir(crashCountPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(crashCountPath, []byte(strconv.Itoa(count)+"\n"), 0o644)
}
