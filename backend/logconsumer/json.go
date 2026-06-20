package logconsumer

import (
	"bufio"
	"bytes"
	"context"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/runtime/v2/logging"
	"github.com/jptrs93/sjson"
)

const JSONCommandName = "json-log-consumer"

func RunJSONProcess(args []string) error {
	if len(args) != 3 || args[1] != JSONCommandName || args[2] == "" {
		return fmt.Errorf("usage: %s %s <base-path>", args[0], JSONCommandName)
	}
	runJSONLogger(args[2])
	return nil
}

func runJSONLogger(basePath string) {
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

		wg.Go(func() { stdoutErr = processJSONLinesWithClock(cfg.Stdout, outlines, time.Now) })
		wg.Go(func() { stderrErr = processJSONLinesWithClock(cfg.Stderr, outlines, time.Now) })

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

func processJSONLinesWithClock(r io.Reader, ch chan<- []byte, now func() time.Time) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxUnformattedBlockBytes)
	for scanner.Scan() {
		line := bytes.Clone(scanner.Bytes())
		ch <- []byte(formatJSONLogLine(now(), line))
	}
	return scanner.Err()
}

func formatJSONLogLine(t time.Time, line []byte) string {
	if !stdjson.Valid(line) {
		return formatUnformattedLogLine(t, string(line))
	}
	json, err := sjson.ParseUTF8(line)
	if err != nil || json == nil || !json.IsObject() {
		return formatUnformattedLogLine(t, string(line))
	}

	logTime, ok, err := jsonLogObjectString(json, "time")
	if err != nil {
		return formatUnformattedLogLine(t, string(line))
	}
	if !ok {
		logTime = t.UTC().Format(time.RFC3339Nano)
	}

	level, ok, err := jsonLogObjectString(json, "level")
	if err != nil {
		return formatUnformattedLogLine(t, string(line))
	}
	if !ok {
		level = "WARN"
	} else {
		level = strings.ToUpper(level)
	}

	var b strings.Builder
	appendLogfmtField(&b, "time", logTime)
	appendLogfmtField(&b, "level", level)
	if msg, ok, err := jsonLogObjectString(json, "msg"); err != nil {
		return formatUnformattedLogLine(t, string(line))
	} else if ok {
		appendLogfmtField(&b, "msg", msg)
	}

	keys := json.Keys()
	sort.Strings(keys)
	for _, key := range keys {
		if key == "time" || key == "level" || key == "msg" {
			continue
		}
		if err := appendJSONLogfmtFields(&b, key, json.ObjectItems()[key]); err != nil {
			return formatUnformattedLogLine(t, string(line))
		}
	}
	b.WriteByte('\n')
	return b.String()
}

func jsonLogObjectString(json *sjson.Json, key string) (string, bool, error) {
	value, ok := json.ObjectItems()[key]
	if !ok {
		return "", false, nil
	}
	formatted, err := jsonLogValue(value)
	return formatted, true, err
}

func appendJSONLogfmtFields(b *strings.Builder, key string, value *sjson.Json) error {
	if value.IsObject() {
		keys := value.Keys()
		sort.Strings(keys)
		for _, childKey := range keys {
			formatted, err := jsonLogValue(value.ObjectItems()[childKey])
			if err != nil {
				return err
			}
			appendLogfmtField(b, key+"."+childKey, formatted)
		}
		return nil
	}

	formatted, err := jsonLogValue(value)
	if err != nil {
		return err
	}
	appendLogfmtField(b, key, formatted)
	return nil
}

func jsonLogValue(value *sjson.Json) (string, error) {
	if !value.IsString() {
		return value.String(), nil
	}
	bytes := value.Bytes()
	if len(bytes) < 2 {
		return "", nil
	}
	return sjson.EscapeUTF8(bytes[1 : len(bytes)-1])
}
