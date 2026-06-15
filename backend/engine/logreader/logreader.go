package logreader

import (
	"bytes"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jptrs93/opsagent/backend/ainit"
)

type LogLine struct {
	Time  time.Time
	Level string
	Msg   string
	Props map[string]string
}

func StreamLogs(deploymentID int, since time.Time, till *time.Time) iter.Seq2[LogLine, error] {
	return func(yield func(LogLine, error) bool) {
		runDirs, err := candidateRunDirs(deploymentID)
		if err != nil {
			yield(LogLine{}, err)
			return
		}
		streams := make([]iter.Seq2[LogLine, error], 0, len(runDirs))
		for _, dir := range runDirs {
			streams = append(streams, streamRunDir(dir, since, till))
		}
		for line, err := range mergeStreams(streams...) {
			if !yield(line, err) {
				return
			}
			if err != nil {
				return
			}
		}
	}
}

func candidateRunDirs(deploymentID int) ([]string, error) {
	root := filepath.Join(ainit.StaticConfig.RunOutputDir, fmt.Sprintf("%d", deploymentID))
	versions, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var dirs []string
	for _, version := range versions {
		if !version.IsDir() {
			continue
		}
		versionDir := filepath.Join(root, version.Name())
		runs, err := os.ReadDir(versionDir)
		if err != nil {
			return nil, err
		}
		for _, run := range runs {
			if run.IsDir() {
				dirs = append(dirs, filepath.Join(versionDir, run.Name()))
			}
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}

func streamRunDir(dir string, since time.Time, till *time.Time) iter.Seq2[LogLine, error] {
	return func(yield func(LogLine, error) bool) {
		files, err := candidateLogFiles(dir, since, till)
		if err != nil {
			yield(LogLine{}, err)
			return
		}
		for _, path := range files {
			if !streamLogFile(path, since, till, yield) {
				return
			}
		}
	}
}

func candidateLogFiles(dir string, since time.Time, till *time.Time) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".logbin" {
			continue
		}
		bucket, ok := parseBucket(entry.Name())
		if !ok {
			continue
		}
		bucketEnd := bucket.Add(time.Hour)
		if bucketEnd.Before(since) || bucketEnd.Equal(since) {
			continue
		}
		if till != nil && !bucket.Before(*till) {
			continue
		}
		files = append(files, filepath.Join(dir, entry.Name()))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	return files, nil
}

func parseBucket(name string) (time.Time, bool) {
	base := strings.TrimSuffix(name, ".logbin")
	t, err := time.ParseInLocation("20060102_15", base, time.UTC)
	return t, err == nil
}

func streamLogFile(path string, since time.Time, till *time.Time, yield func(LogLine, error) bool) bool {
	f, err := os.Open(path)
	if err != nil {
		return yield(LogLine{}, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return yield(LogLine{}, err)
	}

	const chunkSize = 64 * 1024
	var remainder []byte
	for offset := info.Size(); offset > 0; {
		readSize := int64(chunkSize)
		if offset < readSize {
			readSize = offset
		}
		offset -= readSize

		buf := make([]byte, readSize+int64(len(remainder)))
		if _, err := f.ReadAt(buf[:readSize], offset); err != nil {
			return yield(LogLine{}, err)
		}
		copy(buf[readSize:], remainder)
		end := len(buf)
		for end > 0 {
			idx := bytes.LastIndexByte(buf[:end], '\n')
			if idx == -1 {
				break
			}
			if idx+1 < end && !yieldParsedLogLine(path, buf[idx+1:end], since, till, yield) {
				return false
			}
			end = idx
		}
		remainder = append(remainder[:0], buf[:end]...)
	}
	if len(remainder) > 0 {
		return yieldParsedLogLine(path, remainder, since, till, yield)
	}
	return true
}

func yieldParsedLogLine(path string, raw []byte, since time.Time, till *time.Time, yield func(LogLine, error) bool) bool {
	line, err := ParseLogfmtLine(string(raw))
	if err != nil {
		return yield(LogLine{}, fmt.Errorf("parse %s: %w", path, err))
	}
	if line.Time.Before(since) {
		return true
	}
	if till != nil && !line.Time.Before(*till) {
		return true
	}
	return yield(line, nil)
}

func mergeStreams(streams ...iter.Seq2[LogLine, error]) iter.Seq2[LogLine, error] {
	type streamState struct {
		next func() (LogLine, error, bool)
		stop func()
		line LogLine
		err  error
		ok   bool
	}
	return func(yield func(LogLine, error) bool) {
		states := make([]streamState, 0, len(streams))
		defer func() {
			for i := range states {
				states[i].stop()
			}
		}()
		for _, stream := range streams {
			next, stop := iter.Pull2(stream)
			line, err, ok := next()
			if ok {
				states = append(states, streamState{next: next, stop: stop, line: line, err: err, ok: ok})
			} else {
				stop()
			}
		}

		for len(states) > 0 {
			maxIdx := 0
			for i := 1; i < len(states); i++ {
				if states[maxIdx].line.Time.Before(states[i].line.Time) {
					maxIdx = i
				}
			}
			state := &states[maxIdx]
			if !yield(state.line, state.err) || state.err != nil {
				return
			}
			line, err, ok := state.next()
			if ok {
				state.line = line
				state.err = err
				continue
			}
			state.stop()
			states = append(states[:maxIdx], states[maxIdx+1:]...)
		}
	}
}

func ParseLogfmtLine(line string) (LogLine, error) {
	fields, err := parseLogfmt(line)
	if err != nil {
		return LogLine{}, err
	}
	value, ok := fields["time"]
	if !ok || value == "" {
		return LogLine{}, fmt.Errorf("missing time")
	}
	ts, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return LogLine{}, fmt.Errorf("invalid time %q: %w", value, err)
	}
	msg := fields["msg"]
	if msg == "" {
		msg = fields["message"]
	}
	props := make(map[string]string)
	for k, v := range fields {
		switch k {
		case "time", "level", "msg", "message":
			continue
		default:
			props[k] = v
		}
	}
	return LogLine{Time: ts.UTC(), Level: fields["level"], Msg: msg, Props: props}, nil
}

func parseLogfmt(line string) (map[string]string, error) {
	fields := make(map[string]string)
	for i := 0; i < len(line); {
		for i < len(line) && unicode.IsSpace(rune(line[i])) {
			i++
		}
		if i >= len(line) {
			break
		}
		keyStart := i
		for i < len(line) && line[i] != '=' && !unicode.IsSpace(rune(line[i])) {
			i++
		}
		if keyStart == i || i >= len(line) || line[i] != '=' {
			return nil, fmt.Errorf("invalid token near %q", line[keyStart:])
		}
		key := line[keyStart:i]
		i++
		value, next, err := parseLogfmtValue(line, i)
		if err != nil {
			return nil, err
		}
		fields[key] = value
		i = next
	}
	return fields, nil
}

func parseLogfmtValue(line string, i int) (string, int, error) {
	if i < len(line) && line[i] == '"' {
		var b strings.Builder
		i++
		for i < len(line) {
			switch line[i] {
			case '"':
				return b.String(), i + 1, nil
			case '\\':
				i++
				if i >= len(line) {
					return "", i, fmt.Errorf("unterminated escape")
				}
				switch line[i] {
				case 'n':
					b.WriteByte('\n')
				case 'r':
					b.WriteByte('\r')
				case 't':
					b.WriteByte('\t')
				default:
					b.WriteByte(line[i])
				}
			default:
				b.WriteByte(line[i])
			}
			i++
		}
		return "", i, fmt.Errorf("unterminated quoted value")
	}
	start := i
	for i < len(line) && !unicode.IsSpace(rune(line[i])) {
		i++
	}
	return line[start:i], i, nil
}
