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
		writer := newLogfmtWriter(hourly)
		defer writer.Close()
		if err := hourly.ensureOpen(time.Now().UTC()); err != nil {
			return err
		}
		if err := ready(); err != nil {
			return err
		}

		var wg sync.WaitGroup
		errCh := make(chan error, 2)
		copyStream := func(r io.Reader) {
			defer wg.Done()
			_, err := io.CopyBuffer(writer, r, make([]byte, 32*1024))
			if err != nil && ctx.Err() == nil {
				errCh <- err
			}
		}

		wg.Add(2)
		go copyStream(cfg.Stdout)
		go copyStream(cfg.Stderr)

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

const unformattedFlushDelay = 20 * time.Millisecond

type logfmtWriter struct {
	out        *hourlyWriter
	now        func() time.Time
	flushDelay time.Duration

	mu          sync.Mutex
	partial     []byte
	pending     []string
	pendingTime time.Time
	timer       *time.Timer
	err         error
	closed      bool
}

func newLogfmtWriter(out *hourlyWriter) *logfmtWriter {
	return &logfmtWriter{
		out:        out,
		now:        func() time.Time { return time.Now().UTC() },
		flushDelay: unformattedFlushDelay,
	}
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
		if err := w.flushUnformattedLocked(); err != nil {
			return err
		}
		_, err := w.out.writeAt(now, []byte(line+"\n"))
		return err
	}
	w.appendUnformattedLocked(now, line)
	return nil
}

func (w *logfmtWriter) appendUnformattedLocked(now time.Time, line string) {
	if len(w.pending) == 0 {
		w.pendingTime = now
	}
	w.pending = append(w.pending, line)
	w.resetTimerLocked()
}

func (w *logfmtWriter) resetTimerLocked() {
	if w.flushDelay <= 0 {
		return
	}
	if w.timer == nil {
		w.timer = time.AfterFunc(w.flushDelay, w.flushFromTimer)
		return
	}
	w.timer.Reset(w.flushDelay)
}

func (w *logfmtWriter) flushFromTimer() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.err != nil {
		return
	}
	if err := w.flushUnformattedLocked(); err != nil {
		w.err = err
	}
}

func (w *logfmtWriter) flushUnformattedLocked() error {
	if len(w.pending) == 0 {
		return nil
	}
	if w.timer != nil {
		w.timer.Stop()
	}
	writeTime := w.pendingTime
	line := formatUnformattedLogLine(writeTime, strings.Join(w.pending, "\n"))
	w.pending = nil
	w.pendingTime = time.Time{}
	_, err := w.out.writeAt(writeTime, []byte(line))
	return err
}

func (w *logfmtWriter) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	if w.timer != nil {
		w.timer.Stop()
	}
	if len(w.partial) > 0 {
		line := string(bytes.TrimSuffix(w.partial, []byte{'\r'}))
		w.partial = nil
		if err := w.handleLineLocked(w.now().UTC(), line); err != nil && w.err == nil {
			w.err = err
		}
	}
	if err := w.flushUnformattedLocked(); err != nil && w.err == nil {
		w.err = err
	}
	err := w.err
	w.mu.Unlock()
	if closeErr := w.out.Close(); err == nil {
		err = closeErr
	}
	return err
}

func formatUnformattedLogLine(t time.Time, message string) string {
	return "time=" + t.UTC().Format(time.RFC3339Nano) + " level=ERROR fmt=unformatted msg=" + quoteLogfmtValue(message) + "\n"
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
