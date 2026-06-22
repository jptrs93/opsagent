package logconsumer

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/containerd/v2/core/runtime/v2/logging"
)

const CommandName = "log-consumer"

const (
	SystemLogDeploymentID  int32 = 0
	SystemLogConfigVersion int32 = 0
	SystemLogRunNumber     int32 = 1
)

const logOutputQueueSize = 5_000
const maxUnformattedBlockBytes = 256 * 1024

func SystemLogBasePath(runOutputDir string) string {
	return filepath.Join(runOutputDir, fmt.Sprintf("%d", SystemLogDeploymentID))
}

func NewSystemLogWriter(basePath string) (io.WriteCloser, error) {
	return newSystemLogWriterWithClock(basePath, time.Now)
}

func newSystemLogWriterWithClock(basePath string, now func() time.Time) (*systemLogWriter, error) {
	out, err := newRawBinaryWriter(basePath, SystemLogConfigVersion, SystemLogRunNumber)
	if err != nil {
		return nil, err
	}
	return &systemLogWriter{out: out, now: now}, nil
}

type systemLogWriter struct {
	mu      sync.Mutex
	out     *rawBinaryWriter
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
		if err := w.writeLine(data[start:end]); err != nil {
			return 0, err
		}
		start = end
	}
	return len(p), nil
}

func (w *systemLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	var err error
	if len(w.pending) > 0 {
		err = w.writeLine(w.pending)
		w.pending = nil
	}
	if closeErr := w.out.Close(); err == nil {
		err = closeErr
	}
	return err
}

func (w *systemLogWriter) writeLine(line []byte) error {
	if len(line) > math.MaxInt32-SplitRecordPayloadLen {
		return fmt.Errorf("log line too large: %d bytes", len(line))
	}
	return w.out.writeLine(rawBinaryLogLine{t: w.now().UTC(), stream: SplitStreamStdout, line: line})
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
		defer hourly.Close()
		if err := hourly.ensureOpen(time.Now().UTC()); err != nil {
			return err
		}

		outlines := make(chan []byte, logOutputQueueSize)
		var wg sync.WaitGroup
		var stdoutErr error
		var stderrErr error
		closeInputs := sync.OnceFunc(func() {
			closeReader(cfg.Stdout)
			closeReader(cfg.Stderr)
		})

		wg.Go(func() { stdoutErr = processLinesWithClock(cfg.Stdout, outlines, time.Now) })
		wg.Go(func() { stderrErr = processLinesWithClock(cfg.Stderr, outlines, time.Now) })

		go func() {
			<-ctx.Done()
			closeInputs()
		}()

		go func() {
			wg.Wait()
			close(outlines)
		}()

		if err := ready(); err != nil {
			closeInputs()
			for range outlines {
			} // drain to allow line consumers to continue
			return err
		}

		var writeErr error
		for line := range outlines {
			if writeErr != nil {
				continue
			}
			if _, err := hourly.writeAt(time.Now().UTC(), line); err != nil {
				writeErr = err
				closeInputs()
			}
		}

		if ctx.Err() != nil && writeErr == nil {
			return nil
		}
		var errs []error
		if writeErr != nil {
			errs = append(errs, writeErr)
		}
		if stdoutErr != nil && ctx.Err() == nil {
			errs = append(errs, stdoutErr)
		}
		if stderrErr != nil && ctx.Err() == nil {
			errs = append(errs, stderrErr)
		}
		return errors.Join(errs...)
	})
}

func processLinesWithClock(r io.Reader, ch chan<- []byte, now func() time.Time) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxUnformattedBlockBytes)
	for scanner.Scan() {
		line := bytes.Clone(scanner.Bytes())
		if bytes.HasPrefix(line, []byte("time=")) {
			ch <- append(line, '\n')
		} else {
			ch <- []byte(formatUnformattedLogLine(now(), string(line)))
		}
	}
	return scanner.Err()
}

func closeReader(r io.Reader) {
	if c, ok := r.(io.Closer); ok {
		_ = c.Close()
	}
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
	current  string
	lineOpen bool
	file     *os.File
}

func (w *hourlyWriter) Write(p []byte) (int, error) {
	return w.writeAt(time.Now().UTC(), p)
}

func (w *hourlyWriter) writeAt(now time.Time, p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	bucket := now.Format("20060102_15")
	targetBucket := bucket
	if w.file != nil && w.current != "" && w.current != bucket && w.lineOpen {
		targetBucket = w.current
	}
	if err := w.openBucket(targetBucket); err != nil {
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
