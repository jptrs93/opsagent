package logconsumer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/runtime/v2/logging"
	"github.com/jptrs93/opsagent/backend/ainit"
	logv2 "github.com/jptrs93/opsagent/backend/lib/log/v2"
)

const (
	maxLineLen         = 64 * 1024
	shutdownDrainGrace = 2 * time.Second
)

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
		stdout, err := logv2.NewAppender(rawCfg.DeploymentDir, rawCfg.Version, rawCfg.Run, logv2.StreamStdout)
		if err != nil {
			return err
		}
		defer stdout.Close()
		stderr, err := logv2.NewAppender(rawCfg.DeploymentDir, rawCfg.Version, rawCfg.Run, logv2.StreamStderr)
		if err != nil {
			return err
		}
		defer stderr.Close()

		closeInputs := sync.OnceFunc(func() {
			closeReader(cfg.Stdout)
			closeReader(cfg.Stderr)
		})
		done := make(chan struct{})
		var wg sync.WaitGroup
		var stdoutErr error
		var stderrErr error
		wg.Go(func() {
			stdoutErr = consumeStream(cfg.Stdout, stdout.Append, time.Now)
		})
		wg.Go(func() {
			stderrErr = consumeStream(cfg.Stderr, stderr.Append, time.Now)
		})
		go func() {
			<-ctx.Done()
			select {
			case <-done:
			case <-time.After(shutdownDrainGrace):
				closeInputs()
			}
		}()

		readyErr := ready()
		if readyErr != nil {
			closeInputs()
		}
		wg.Wait()
		close(done)
		if readyErr != nil {
			return readyErr
		}
		if ctx.Err() != nil {
			return nil
		}
		return errors.Join(stdoutErr, stderrErr)
	})
}

func consumeStream(r io.Reader, sink func(time.Time, []byte), now func() time.Time) error {
	br := bufio.NewReaderSize(r, maxLineLen)
	for {
		line, err := br.ReadSlice('\n')
		if len(line) > 0 {
			sink(now().UTC(), line)
		}
		switch {
		case err == nil, errors.Is(err, bufio.ErrBufferFull):
		case errors.Is(err, io.EOF):
			return nil
		default:
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
