// Package preparerlog writes user-visible deployment preparation logs.
package preparerlog

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// Log wraps a preparation output file and standardizes messages written by
// OpenDeploy. Output returns a writer for unmodified subprocess output.
type Log struct {
	ctx  context.Context
	file *os.File
	mu   sync.Mutex
}

func New(ctx context.Context, dep *apigen.DeploymentConfig2) (*Log, string, error) {
	path := dep.PrepareOutputPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, path, fmt.Errorf("creating prepare log dir: %w", err)
	}
	file, err := os.Create(path)
	if err != nil {
		return nil, path, err
	}
	return &Log{ctx: ctx, file: file}, path, nil
}

// Write records a normal OpenDeploy preparation message.
func (l *Log) Write(format string, args ...any) {
	l.write("", format, args...)
}

// Error records a failed OpenDeploy preparation step.
func (l *Log) Error(format string, args ...any) {
	l.write("ERROR: ", format, args...)
}

func (l *Log) write(prefix, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	if prefix == "" {
		slog.InfoContext(l.ctx, message)
	} else {
		slog.ErrorContext(l.ctx, message)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintf(l.file, "==> %s%s\n", prefix, message)
}

// Output returns a synchronized writer for raw subprocess stdout and stderr.
func (l *Log) Output() io.Writer {
	return outputWriter{log: l}
}

func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}

type outputWriter struct {
	log *Log
}

func (w outputWriter) Write(p []byte) (int, error) {
	w.log.mu.Lock()
	defer w.log.mu.Unlock()
	return w.log.file.Write(p)
}
