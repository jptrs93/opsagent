package logconsumer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/runtime/v2/logging"
)

const RawBinaryCommandName = "raw-binary-log-consumer"

type rawBinaryConfig struct {
	DeploymentDir string `json:"deployment_dir"`
	Version       int32  `json:"version"`
	Run           int32  `json:"run"`
}

func RawBinaryConfigArg(deploymentDir string, version int32, run int32) (string, error) {
	cfg := rawBinaryConfig{DeploymentDir: deploymentDir, Version: version, Run: run}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

type rawBinaryLogLine struct {
	t      time.Time
	stream int8
	line   []byte
}

func RunRawBinaryProcess(args []string) error {
	if len(args) != 3 || args[1] != RawBinaryCommandName || args[2] == "" {
		return fmt.Errorf("usage: %s %s <config-json>", args[0], RawBinaryCommandName)
	}
	cfg, err := parseRawBinaryConfigArg(args[2])
	if err != nil {
		return err
	}
	runRawBinaryLogger(cfg)
	return nil
}

func runRawBinaryLogger(rawCfg rawBinaryConfig) {
	logging.Run(func(ctx context.Context, cfg *logging.Config, ready func() error) error {
		writer, err := newRawBinaryWriter(rawCfg.DeploymentDir, rawCfg.Version, rawCfg.Run)
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

func newRawBinaryWriter(deploymentDir string, version int32, run int32) (*rawBinaryWriter, error) {
	if deploymentDir == "" {
		return nil, fmt.Errorf("raw binary log deployment dir is empty")
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

func parseRawBinaryConfigArg(arg string) (rawBinaryConfig, error) {
	var cfg rawBinaryConfig
	if err := json.Unmarshal([]byte(arg), &cfg); err != nil {
		return rawBinaryConfig{}, fmt.Errorf("parsing raw binary log config: %w", err)
	}
	if cfg.DeploymentDir == "" {
		return rawBinaryConfig{}, fmt.Errorf("raw binary log config deployment_dir is empty")
	}
	return cfg, nil
}
