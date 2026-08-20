package log

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	logv2 "github.com/jptrs93/opsagent/backend/lib/log/v2"
)

const (
	SystemLogDeploymentID  int32 = 0
	SystemLogConfigVersion int32 = 0
	SystemLogRunNumber     int32 = 1
)

func SystemLogBasePath(runOutputDir string) string {
	return filepath.Join(runOutputDir, fmt.Sprintf("%d", SystemLogDeploymentID))
}

func NewSystemLogWriter(basePath string) (io.WriteCloser, error) {
	return newSystemLogWriterWithClock(basePath, time.Now)
}

func newSystemLogWriterWithClock(basePath string, now func() time.Time) (*systemLogWriter, error) {
	out, err := logv2.NewAppender(basePath, SystemLogConfigVersion, SystemLogRunNumber, logv2.StreamStdout)
	if err != nil {
		return nil, err
	}
	return &systemLogWriter{out: out, now: now}, nil
}

type systemLogWriter struct {
	mu      sync.Mutex
	out     *logv2.Appender
	now     func() time.Time
	pending []byte
}

func (w *systemLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	data := append(w.pending, p...)
	w.pending = nil
	start := 0
	for start < len(data) {
		idx := bytes.IndexByte(data[start:], '\n')
		if idx < 0 {
			w.pending = append(w.pending[:0], data[start:]...)
			return len(p), nil
		}
		end := start + idx + 1
		w.writeLine(data[start:end])
		start = end
	}
	return len(p), nil
}

func (w *systemLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) > 0 {
		w.writeLine(w.pending)
		w.pending = nil
	}
	return w.out.Close()
}

func (w *systemLogWriter) writeLine(line []byte) {
	w.out.Append(w.now().UTC(), line)
}
