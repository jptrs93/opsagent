package logconsumer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/containerd/containerd/v2/core/runtime/v2/logging"
	"github.com/jptrs93/opsagent/backend/ainit"
	logv2 "github.com/jptrs93/opsagent/backend/lib/log/v2"
)

const (
	maxLineLen         = logv2.MaxLineLen
	lineReadBufLen     = maxLineLen - utf8.UTFMax + 1
	shutdownDrainGrace = 2 * time.Second
)

// RawBinaryConfig is everything the logger needs to stamp records without
// consulting anything else at runtime; it is handed to the consumer process as
// a json argv element.
type RawBinaryConfig struct {
	DeploymentDir   string `json:"deployment_dir"`
	Version         int32  `json:"version"`
	Run             int32  `json:"run"`
	Deployment      int32  `json:"deployment"`
	Node            int32  `json:"node"`
	InstanceOrdinal int32  `json:"instance_ordinal"`
}

func (c RawBinaryConfig) recordMeta(stream int8) logv2.RecordMeta {
	return logv2.RecordMeta{
		Version:         c.Version,
		Run:             c.Run,
		Deployment:      c.Deployment,
		Node:            c.Node,
		InstanceOrdinal: c.InstanceOrdinal,
		Stream:          stream,
	}
}

func RawBinaryConfigArg(cfg RawBinaryConfig) (string, error) {
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

func runRawBinaryLogger(rawCfg RawBinaryConfig) {
	logging.Run(func(ctx context.Context, cfg *logging.Config, ready func() error) error {
		stdout, err := logv2.NewAppender(rawCfg.DeploymentDir, rawCfg.recordMeta(logv2.StreamStdout))
		if err != nil {
			return err
		}
		defer stdout.Close()
		stderr, err := logv2.NewAppender(rawCfg.DeploymentDir, rawCfg.recordMeta(logv2.StreamStderr))
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
	br := bufio.NewReaderSize(r, lineReadBufLen)
	var carry []byte
	for {
		line, err := br.ReadSlice('\n')
		chunk := line
		if len(carry) > 0 {
			chunk = append(carry, line...)
			carry = nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			if kept := trimPartialRune(chunk); len(kept) < len(chunk) {
				carry = bytes.Clone(chunk[len(kept):])
				chunk = kept
			}
		}
		if len(chunk) > 0 {
			sink(now().UTC(), chunk)
		}
		switch {
		case err == nil, errors.Is(err, bufio.ErrBufferFull):
		case errors.Is(err, io.EOF):
			if len(carry) > 0 {
				sink(now().UTC(), carry)
			}
			return nil
		default:
			return err
		}
	}
}

func trimPartialRune(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if !utf8.RuneStart(b[i]) {
			continue
		}
		if utf8.FullRune(b[i:]) {
			return b
		}
		return b[:i]
	}
	return b
}

func parseRawBinaryConfigArg(arg string) (RawBinaryConfig, error) {
	var cfg RawBinaryConfig
	if err := json.Unmarshal([]byte(arg), &cfg); err != nil {
		return RawBinaryConfig{}, fmt.Errorf("parsing raw binary log config: %w", err)
	}
	if cfg.DeploymentDir == "" {
		return RawBinaryConfig{}, fmt.Errorf("raw binary log config deployment_dir is empty")
	}
	return cfg, nil
}

func closeReader(r io.Reader) {
	if c, ok := r.(io.Closer); ok {
		_ = c.Close()
	}
}
