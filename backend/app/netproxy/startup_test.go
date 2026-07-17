package netproxy

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
)

func TestRunLogsStartupAndDNSReadiness(t *testing.T) {
	previousConfig := ainit.StaticConfig
	ainit.StaticConfig.NetproxyStatePath = t.TempDir() + "/netstate.pb"
	t.Cleanup(func() { ainit.StaticConfig = previousConfig })
	t.Setenv("OPENDEPLOY_NETPROXY_DNS_LISTEN", "127.0.0.1:0")

	lines := make(chan string, 10)
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logChannelWriter{lines: lines}, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx) }()

	waitForLogMessage(t, lines, "opendeploy net starting")
	waitForLogMessage(t, lines, "DNS listeners started")
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned an error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

type logChannelWriter struct {
	lines chan<- string
}

func (w logChannelWriter) Write(p []byte) (int, error) {
	w.lines <- string(p)
	return len(p), nil
}

func waitForLogMessage(t *testing.T, lines <-chan string, message string) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case line := <-lines:
			if strings.Contains(line, message) {
				return
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for log message %q", message)
		}
	}
}
