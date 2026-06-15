package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const crashCountPath = "/var/crashcount.txt"

func main() {
	count, err := readCrashCount()
	if err != nil {
		fmt.Printf("nixdockercrasher read crash count error=%v\n", err)
		os.Exit(1)
	}

	if count >= 3 {
		fmt.Printf("nixdockercrasher crash count=%d; staying alive\n", count)
		for tick := 1; ; tick++ {
			fmt.Printf("nixdockercrasher healthy tick=%d time=%s\n", tick, time.Now().Format(time.RFC3339))
			time.Sleep(10 * time.Second)
		}
	}

	next := count + 1
	fmt.Printf("nixdockercrasher will crash number=%d after 2s\n", next)
	time.Sleep(2 * time.Second)

	if err := writeCrashCount(next); err != nil {
		fmt.Printf("nixdockercrasher write crash count error=%v\n", err)
		os.Exit(1)
	}

	fmt.Printf("nixdockercrasher wrote crash number=%d to %s; panicking now\n", next, crashCountPath)
	panic(fmt.Sprintf("nixdockercrasher panic crash count=%d", next))
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
