package logconsumer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/containerd/v2/core/runtime/v2/logging"
)

const CommandName = "log-consumer"
const SystemLogRun = "opendeploy"

func SystemLogBasePath(runOutputDir string, machine string) string {
	return filepath.Join(runOutputDir, "0", machine, SystemLogRun)
}

func NewHourlyWriter(basePath string) (io.WriteCloser, error) {
	if err := os.MkdirAll(basePath, 0o750); err != nil {
		return nil, err
	}
	w := &hourlyWriter{basePath: basePath}
	if err := w.ensureOpen(time.Now().UTC()); err != nil {
		return nil, err
	}
	return w, nil
}

func RunBinaryProcess(args []string) error {
	if len(args) != 3 || args[1] != CommandName || args[2] == "" {
		return fmt.Errorf("usage: %s %s <base-path>", args[0], CommandName)
	}
	runBinaryLogger(args[2])
	return nil
}

func runBinaryLogger(basePath string) {
	logging.Run(func(ctx context.Context, cfg *logging.Config, ready func() error) error {
		hourly := &hourlyWriter{basePath: basePath}
		stdoutWriter := newLogfmtStreamWriter(hourly, "INFO")
		stderrWriter := newLogfmtStreamWriter(hourly, "ERROR")
		defer hourly.Close()
		defer stdoutWriter.Close()
		defer stderrWriter.Close()
		if err := hourly.ensureOpen(time.Now().UTC()); err != nil {
			return err
		}
		if err := ready(); err != nil {
			return err
		}

		var wg sync.WaitGroup
		errCh := make(chan error, 2)
		copyStream := func(r io.Reader, writer io.Writer) {
			defer wg.Done()
			_, err := io.CopyBuffer(writer, r, make([]byte, 32*1024))
			if err != nil && ctx.Err() == nil {
				errCh <- err
			}
		}

		wg.Add(2)
		go copyStream(cfg.Stdout, stdoutWriter)
		go copyStream(cfg.Stderr, stderrWriter)

		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-ctx.Done():
			closeReader(cfg.Stdout)
			closeReader(cfg.Stderr)
			<-done
			return nil
		case <-done:
			close(errCh)
			var errs []error
			for err := range errCh {
				errs = append(errs, err)
			}
			return errors.Join(errs...)
		}
	})
}

func closeReader(r io.Reader) {
	if c, ok := r.(io.Closer); ok {
		_ = c.Close()
	}
}

type logfmtWriter struct {
	out          *hourlyWriter
	now          func() time.Time
	defaultLevel string
	closeOut     bool

	mu      sync.Mutex
	partial []byte
	err     error
	closed  bool
}

func newLogfmtWriter(out *hourlyWriter) *logfmtWriter {
	return &logfmtWriter{
		out:          out,
		now:          func() time.Time { return time.Now().UTC() },
		defaultLevel: "ERROR",
		closeOut:     true,
	}
}

func newLogfmtStreamWriter(out *hourlyWriter, defaultLevel string) *logfmtWriter {
	w := newLogfmtWriter(out)
	w.defaultLevel = defaultLevel
	w.closeOut = false
	return w
}

func (w *logfmtWriter) Write(p []byte) (int, error) {
	return w.writeAt(w.now(), p)
}

func (w *logfmtWriter) writeAt(now time.Time, p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	if w.closed {
		return 0, os.ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	w.partial = append(w.partial, p...)
	for {
		idx := bytes.IndexByte(w.partial, '\n')
		if idx < 0 {
			return len(p), nil
		}
		line := string(bytes.TrimSuffix(w.partial[:idx], []byte{'\r'}))
		w.partial = w.partial[idx+1:]
		if err := w.handleLineLocked(now.UTC(), line); err != nil {
			w.err = err
			return 0, err
		}
	}
}

func (w *logfmtWriter) handleLineLocked(now time.Time, line string) error {
	if strings.HasPrefix(line, "time=") {
		_, err := w.out.writeAt(now, []byte(line+"\n"))
		return err
	}
	logLine := formatUnformattedLogLine(now, w.defaultLevel, line)
	_, err := w.out.writeAt(now, []byte(logLine))
	return err
}

func (w *logfmtWriter) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	if len(w.partial) > 0 {
		line := string(bytes.TrimSuffix(w.partial, []byte{'\r'}))
		w.partial = nil
		if err := w.handleLineLocked(w.now().UTC(), line); err != nil && w.err == nil {
			w.err = err
		}
	}
	err := w.err
	w.mu.Unlock()
	if w.closeOut {
		if closeErr := w.out.Close(); err == nil {
			err = closeErr
		}
	}
	return err
}

func formatUnformattedLogLine(t time.Time, level string, message string) string {
	if level == "" {
		level = "ERROR"
	}
	return "time=" + t.UTC().Format(time.RFC3339Nano) + " level=" + level + " fmt=unformatted msg=" + quoteLogfmtValue(message) + "\n"
}

func quoteLogfmtValue(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

type hourlyWriter struct {
	basePath string
	mu       sync.Mutex
	current  string
	lineOpen bool
	file     *os.File
}

func (w *hourlyWriter) Write(p []byte) (int, error) {
	return w.writeAt(time.Now().UTC(), p)
}

func (w *hourlyWriter) writeAt(now time.Time, p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	bucket := now.Format("20060102_15")
	if err := w.openBucket(w.targetBucket(bucket)); err != nil {
		return 0, err
	}
	written := 0
	for len(p) > 0 {
		idx := bytes.IndexByte(p, '\n')
		if idx < 0 {
			_, err := w.file.Write(p)
			if err != nil {
				return written, err
			}
			written += len(p)
			w.lineOpen = true
			return written, nil
		}
		line := p[:idx+1]
		_, err := w.file.Write(line)
		if err != nil {
			return written, err
		}
		written += len(line)
		w.lineOpen = false
		p = p[idx+1:]
		if len(p) > 0 && bucket != w.current {
			if err := w.openBucket(bucket); err != nil {
				return written, err
			}
		}
	}
	return written, nil
}

func (w *hourlyWriter) ensureOpen(now time.Time) error {
	return w.openBucket(now.Format("20060102_15"))
}

func (w *hourlyWriter) targetBucket(bucket string) string {
	if w.file != nil && w.current != "" && w.current != bucket && w.lineOpen {
		return w.current
	}
	return bucket
}

func (w *hourlyWriter) openBucket(bucket string) error {
	if w.file != nil && w.current == bucket {
		return nil
	}
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return err
		}
		w.file = nil
	}
	uid, gid, mode, err := dirMetadata(w.basePath)
	if err != nil {
		return err
	}
	path := filepath.Join(w.basePath, bucket+".logbin")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_ = os.Chown(path, uid, gid)
	_ = os.Chmod(path, mode)
	w.current = bucket
	w.file = file
	return nil
}

func (w *hourlyWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func dirMetadata(dir string) (int, int, os.FileMode, error) {
	info, err := os.Stat(filepath.Clean(dir))
	if err != nil {
		return -1, -1, 0, err
	}
	if !info.IsDir() {
		return -1, -1, 0, fmt.Errorf("%s is not a directory", dir)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, -1, 0, fmt.Errorf("stat %s: unsupported stat type", dir)
	}
	return int(stat.Uid), int(stat.Gid), fileModeForDir(info.Mode().Perm()), nil
}

func fileModeForDir(mode os.FileMode) os.FileMode {
	return mode & 0o660
}
