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
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/runtime/v2/logging"
	"github.com/jptrs93/opsagent/backend/ainit"
	odlog "github.com/jptrs93/opsagent/backend/lib/log"
)

const logOutputQueueSize = 5_000

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
	commandName := string(ainit.CommandRawLogConsumer)
	if len(args) != 3 || args[1] != commandName || args[2] == "" {
		return fmt.Errorf("usage: %s %s <config-json>", args[0], commandName)
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
		writer, err := odlog.NewBinaryWriter(rawCfg.DeploymentDir, rawCfg.Version, rawCfg.Run)
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

		wg.Go(func() {
			stdoutErr = processRawBinaryLinesWithClock(cfg.Stdout, odlog.BinaryStreamStdout, outlines, time.Now)
		})
		wg.Go(func() {
			stderrErr = processRawBinaryLinesWithClock(cfg.Stderr, odlog.BinaryStreamStderr, outlines, time.Now)
		})

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
			if err := writer.WriteLineAt(line.t, line.stream, line.line); err != nil {
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
			if len(line) > math.MaxInt32-odlog.BinaryRecordPayloadLen {
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

func closeReader(r io.Reader) {
	if c, ok := r.(io.Closer); ok {
		_ = c.Close()
	}
}
