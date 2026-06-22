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
	"strconv"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/runtime/v2/logging"
)

const RawBinaryCommandName = "raw-binary-log-consumer"

type rawBinaryLogLine struct {
	t      time.Time
	stream int8
	line   []byte
}

func RunRawBinaryProcess(args []string) error {
	if len(args) != 3 || args[1] != RawBinaryCommandName || args[2] == "" {
		return fmt.Errorf("usage: %s %s <base-path>", args[0], RawBinaryCommandName)
	}
	runRawBinaryLogger(args[2])
	return nil
}

func runRawBinaryLogger(basePath string) {
	logging.Run(func(ctx context.Context, cfg *logging.Config, ready func() error) error {
		writer, err := newRawBinaryWriter(basePath)
		if err != nil {
			return err
		}
		defer writer.Close()

		outlines := make(chan rawBinaryLogLine, logOutputQueueSize)
		var wg sync.WaitGroup
		var stdoutErr error
		var stderrErr error
		closeInputs := sync.OnceFunc(func() {
			closeReader(cfg.Stdout)
			closeReader(cfg.Stderr)
		})

		wg.Go(func() { stdoutErr = processRawBinaryLinesWithClock(cfg.Stdout, SplitStreamStdout, outlines, time.Now) })
		wg.Go(func() { stderrErr = processRawBinaryLinesWithClock(cfg.Stderr, SplitStreamStderr, outlines, time.Now) })

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
			}
			return err
		}

		var writeErr error
		for line := range outlines {
			if writeErr != nil {
				continue
			}
			if err := writer.writeLine(line); err != nil {
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

func processRawBinaryLinesWithClock(r io.Reader, stream int8, ch chan<- rawBinaryLogLine, now func() time.Time) error {
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if len(line) > math.MaxInt32-SplitRecordPayloadLen {
				return fmt.Errorf("log line too large: %d bytes", len(line))
			}
			ch <- rawBinaryLogLine{t: now().UTC(), stream: stream, line: bytes.Clone(line)}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

type rawBinaryWriter struct {
	deploymentDir string
	version       int32
	run           int32
	current       time.Time
	file          *os.File
}

func newRawBinaryWriter(basePath string) (*rawBinaryWriter, error) {
	deploymentDir, version, run, err := rawBinaryPathParts(basePath)
	if err != nil {
		return nil, err
	}
	w := &rawBinaryWriter{deploymentDir: deploymentDir, version: version, run: run}
	if err := os.MkdirAll(deploymentDir, 0o750); err != nil {
		return nil, err
	}
	if err := w.openBucket(splitLogBucket(time.Now().UTC())); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rawBinaryWriter) writeLine(line rawBinaryLogLine) error {
	if err := w.openBucket(splitLogBucket(line.t)); err != nil {
		return err
	}
	record := EncodeSplitRecord(line.t, w.version, w.run, line.stream, line.line)
	n, err := w.file.Write(record)
	if err == nil && n != len(record) {
		err = io.ErrShortWrite
	}
	return err
}

func (w *rawBinaryWriter) openBucket(bucket time.Time) error {
	if w.file != nil && w.current.Equal(bucket) {
		return nil
	}
	uid, gid, mode, err := dirMetadata(w.deploymentDir)
	if err != nil {
		return err
	}
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return err
		}
		w.file = nil
	}
	path := filepath.Join(w.deploymentDir, fmt.Sprintf("%s_%d_%d.logbin", bucket.Format("20060102_1504"), w.version, w.run))
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

func (w *rawBinaryWriter) Close() error {
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func rawBinaryPathParts(basePath string) (string, int32, int32, error) {
	runPart := filepath.Base(basePath)
	versionDir := filepath.Dir(basePath)
	versionPart := filepath.Base(versionDir)
	deploymentDir := filepath.Dir(versionDir)
	if deploymentDir == "." || deploymentDir == string(filepath.Separator) {
		return "", 0, 0, fmt.Errorf("raw binary log base path %q is not a run output path", basePath)
	}
	version, err := parseRawBinaryPathInt(versionPart, "version")
	if err != nil {
		return "", 0, 0, err
	}
	run, err := parseRawBinaryPathInt(runPart, "run")
	if err != nil {
		return "", 0, 0, err
	}
	return deploymentDir, version, run, nil
}

func parseRawBinaryPathInt(value string, label string) (int32, error) {
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil || value == "" {
		return 0, fmt.Errorf("raw binary log base path has invalid %s %q", label, value)
	}
	return int32(parsed), nil
}
