package logconsumer

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/runtime/v2/logging"
)

const SplitCommandName = "split-log-consumer"

const (
	SplitStreamStdout int8 = 0
	SplitStreamStderr int8 = 1

	SplitMarkerEnd    int8 = 0
	SplitMarkerRotate int8 = 1

	SplitRecordLengthLen  = 4
	SplitRecordPayloadLen = 8 + 4 + 4 + 1
	SplitRecordHeaderLen  = SplitRecordLengthLen + SplitRecordPayloadLen
	SplitRecordTrailerLen = 4
	SplitRecordMinLen     = SplitRecordHeaderLen + SplitRecordTrailerLen
)

func RunSplitProcess(args []string) error {
	if len(args) != 3 || args[1] != SplitCommandName || args[2] == "" {
		return fmt.Errorf("usage: %s %s <base-path>", args[0], SplitCommandName)
	}
	runSplitLogger(args[2])
	return nil
}

func runSplitLogger(basePath string) {
	logging.Run(func(ctx context.Context, cfg *logging.Config, ready func() error) error {
		version, run, err := splitVersionRunFromBasePath(basePath)
		if err != nil {
			return err
		}

		stdout, err := newSplitRotatingWriter(basePath, "stdout", SplitStreamStdout, version, run)
		if err != nil {
			return err
		}
		defer stdout.Close()

		stderr, err := newSplitRotatingWriter(basePath, "stderr", SplitStreamStderr, version, run)
		if err != nil {
			return err
		}
		defer stderr.Close()

		var wg sync.WaitGroup
		var stdoutErr error
		var stderrErr error
		closeInputs := sync.OnceFunc(func() {
			closeReader(cfg.Stdout)
			closeReader(cfg.Stderr)
		})

		wg.Go(func() {
			stdoutErr = processSplitLinesWithClock(cfg.Stdout, stdout, time.Now)
			if stdoutErr != nil {
				closeInputs()
			}
		})
		wg.Go(func() {
			stderrErr = processSplitLinesWithClock(cfg.Stderr, stderr, time.Now)
			if stderrErr != nil {
				closeInputs()
			}
		})

		go func() {
			<-ctx.Done()
			closeInputs()
		}()

		if err := ready(); err != nil {
			closeInputs()
			wg.Wait()
			return err
		}

		wg.Wait()
		if ctx.Err() != nil && stdoutErr == nil && stderrErr == nil {
			return nil
		}
		var errs []error
		if stdoutErr != nil && ctx.Err() == nil {
			errs = append(errs, stdoutErr)
		}
		if stderrErr != nil && ctx.Err() == nil {
			errs = append(errs, stderrErr)
		}
		return errors.Join(errs...)
	})
}

func processSplitLinesWithClock(r io.Reader, w *splitRotatingWriter, now func() time.Time) error {
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if err := w.writeLineAt(now(), line); err != nil {
				return err
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

type splitRotatingWriter struct {
	basePath string
	stream   string
	streamID int8
	version  int32
	run      int32
	current  time.Time
	index    int
	file     *os.File
}

func newSplitRotatingWriter(basePath string, stream string, streamID int8, version int32, run int32) (*splitRotatingWriter, error) {
	index, err := nextSplitFileIndex(basePath, stream)
	if err != nil {
		return nil, err
	}
	w := &splitRotatingWriter{basePath: basePath, stream: stream, streamID: streamID, version: version, run: run, index: index}
	if err := w.ensureOpen(time.Now().UTC()); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *splitRotatingWriter) writeLineAt(now time.Time, line []byte) error {
	if len(line) > math.MaxInt32-SplitRecordPayloadLen {
		return fmt.Errorf("log line too large: %d bytes", len(line))
	}
	if err := w.openBucket(splitLogBucket(now)); err != nil {
		return err
	}
	record := EncodeSplitRecord(now, w.version, w.run, w.streamID, line)
	n, err := w.file.Write(record)
	if err == nil && n != len(record) {
		err = io.ErrShortWrite
	}
	return err
}

func (w *splitRotatingWriter) ensureOpen(now time.Time) error {
	return w.openBucket(splitLogBucket(now))
}

func (w *splitRotatingWriter) openBucket(bucket time.Time) error {
	if w.file != nil && w.current.Equal(bucket) {
		return nil
	}
	uid, gid, mode, err := dirMetadata(w.basePath)
	if err != nil {
		return err
	}
	if w.file != nil {
		path := filepath.Join(w.basePath, fmt.Sprintf("%s%d.logbin", w.stream, w.index+1))
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		_ = os.Chown(path, uid, gid)
		_ = os.Chmod(path, mode)
		if err := w.writeRotateMarker(); err != nil {
			_ = file.Close()
			return err
		}
		if err := w.file.Close(); err != nil {
			_ = file.Close()
			return err
		}
		w.file = file
		w.current = bucket
		w.index++
		return nil
	}
	path := filepath.Join(w.basePath, fmt.Sprintf("%s%d.logbin", w.stream, w.index))
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

func (w *splitRotatingWriter) Close() error {
	if w.file == nil {
		return nil
	}
	err := w.writeEndMarker()
	if closeErr := w.file.Close(); err == nil {
		err = closeErr
	}
	w.file = nil
	return err
}

func (w *splitRotatingWriter) writeEndMarker() error {
	record := EncodeSplitEndMarker()
	return w.writeMarker(record)
}

func (w *splitRotatingWriter) writeRotateMarker() error {
	record := EncodeSplitRotateMarker()
	return w.writeMarker(record)
}

func (w *splitRotatingWriter) writeMarker(record []byte) error {
	n, err := w.file.Write(record)
	if err == nil && n != len(record) {
		err = io.ErrShortWrite
	}
	return err
}

func splitLogBucket(t time.Time) time.Time {
	t = t.UTC()
	minute := 0
	if t.Minute() >= 30 {
		minute = 30
	}
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), minute, 0, 0, time.UTC)
}

func nextSplitFileIndex(basePath string, stream string) (int, error) {
	matches, err := filepath.Glob(filepath.Join(basePath, stream+"*.logbin"))
	if err != nil {
		return 0, err
	}
	maxIndex := -1
	for _, match := range matches {
		name := filepath.Base(match)
		if !strings.HasPrefix(name, stream) || !strings.HasSuffix(name, ".logbin") {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, stream), ".logbin"))
		if err != nil {
			continue
		}
		if idx > maxIndex {
			maxIndex = idx
		}
	}
	return maxIndex + 1, nil
}

func splitVersionRunFromBasePath(basePath string) (int32, int32, error) {
	run, err := parseSplitPathInt(filepath.Base(basePath), "run")
	if err != nil {
		return 0, 0, err
	}
	version, err := parseSplitPathInt(filepath.Base(filepath.Dir(basePath)), "version")
	if err != nil {
		return 0, 0, err
	}
	return version, run, nil
}

func parseSplitPathInt(value string, label string) (int32, error) {
	var parsed int64
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return 0, fmt.Errorf("split log base path has invalid %s %q", label, value)
		}
		parsed = parsed*10 + int64(value[i]-'0')
		if parsed > math.MaxInt32 {
			return 0, fmt.Errorf("split log base path %s %q is too large", label, value)
		}
	}
	if value == "" {
		return 0, fmt.Errorf("split log base path has empty %s", label)
	}
	return int32(parsed), nil
}

func EncodeSplitRecord(t time.Time, version int32, run int32, stream int8, line []byte) []byte {
	length := SplitRecordPayloadLen + len(line)
	record := make([]byte, SplitRecordLengthLen+length+SplitRecordTrailerLen)
	binary.BigEndian.PutUint32(record[:4], uint32(length))
	binary.BigEndian.PutUint64(record[4:12], uint64(t.UnixNano()))
	binary.BigEndian.PutUint32(record[12:16], uint32(version))
	binary.BigEndian.PutUint32(record[16:20], uint32(run))
	record[20] = byte(stream)
	copy(record[21:], line)
	binary.BigEndian.PutUint32(record[len(record)-4:], uint32(length))
	return record
}

func EncodeSplitEndMarker() []byte {
	return EncodeSplitRecord(time.Unix(0, 0).UTC(), 0, 0, SplitMarkerEnd, nil)
}

func EncodeSplitRotateMarker() []byte {
	return EncodeSplitRecord(time.Unix(0, 0).UTC(), 0, 0, SplitMarkerRotate, nil)
}
