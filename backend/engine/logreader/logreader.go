package logreader

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/logconsumer"
)

type LogLine struct {
	Time    int64
	Version int32
	Run     int32
	Stream  int8
	Line    []byte
}

func StreamLogs(deploymentID int, configVersion int, since time.Time, till *time.Time) iter.Seq2[LogLine, error] {
	return func(yield func(LogLine, error) bool) {
		files, err := candidateLogFiles(filepath.Join(ainit.StaticConfig.RunOutputDir, fmt.Sprintf("%d", deploymentID)), configVersion, since, till)
		if err != nil {
			yield(LogLine{}, err)
			return
		}
		var lines []LogLine
		for _, path := range files {
			items, err := readLogFile(path, configVersion, since, till)
			if err != nil {
				yield(LogLine{}, err)
				return
			}
			lines = append(lines, items...)
		}
		sort.SliceStable(lines, func(i, j int) bool { return lines[i].Time > lines[j].Time })
		for _, line := range lines {
			if !yield(line, nil) {
				return
			}
		}
	}
}

func candidateLogFiles(dir string, configVersion int, since time.Time, till *time.Time) ([]string, error) {
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
		bucket, version, _, ok := parseLogFileName(entry.Name())
		if !ok {
			continue
		}
		if configVersion > 0 && version != int32(configVersion) {
			continue
		}
		bucketEnd := bucket.Add(30 * time.Minute)
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

func parseLogFileName(name string) (time.Time, int32, int32, bool) {
	base := strings.TrimSuffix(name, ".logbin")
	parts := strings.Split(base, "_")
	if len(parts) != 4 {
		return time.Time{}, 0, 0, false
	}
	t, err := time.ParseInLocation("20060102_1504", parts[0]+"_"+parts[1], time.UTC)
	if err != nil {
		return time.Time{}, 0, 0, false
	}
	version, err := parseFileNameInt(parts[2])
	if err != nil {
		return time.Time{}, 0, 0, false
	}
	run, err := parseFileNameInt(parts[3])
	if err != nil {
		return time.Time{}, 0, 0, false
	}
	return t, version, run, true
}

func parseFileNameInt(value string) (int32, error) {
	var parsed int64
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return 0, fmt.Errorf("invalid integer %q", value)
		}
		parsed = parsed*10 + int64(value[i]-'0')
	}
	if value == "" {
		return 0, fmt.Errorf("empty integer")
	}
	return int32(parsed), nil
}

func readLogFile(path string, configVersion int, since time.Time, till *time.Time) ([]LogLine, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []LogLine
	for {
		line, err := readRecord(f)
		if err != nil {
			if err == io.EOF {
				return lines, nil
			}
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		if configVersion > 0 && line.Version != int32(configVersion) {
			continue
		}
		t := time.Unix(0, line.Time).UTC()
		if t.Before(since) {
			continue
		}
		if till != nil && !t.Before(*till) {
			continue
		}
		lines = append(lines, line)
	}
}

func readRecord(r io.Reader) (LogLine, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return LogLine{}, io.EOF
		}
		return LogLine{}, err
	}
	length := int32(binary.BigEndian.Uint32(prefix[:]))
	if length < logconsumer.SplitRecordPayloadLen {
		return LogLine{}, fmt.Errorf("invalid record length %d", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return LogLine{}, err
	}
	var suffix [4]byte
	if _, err := io.ReadFull(r, suffix[:]); err != nil {
		return LogLine{}, err
	}
	if binary.BigEndian.Uint32(suffix[:]) != uint32(length) {
		return LogLine{}, fmt.Errorf("record length suffix mismatch")
	}
	if binary.BigEndian.Uint64(payload[:8]) == 0 {
		return LogLine{}, io.EOF
	}
	return LogLine{
		Time:    int64(binary.BigEndian.Uint64(payload[:8])),
		Version: int32(binary.BigEndian.Uint32(payload[8:12])),
		Run:     int32(binary.BigEndian.Uint32(payload[12:16])),
		Stream:  int8(payload[16]),
		Line:    append([]byte(nil), payload[17:]...),
	}, nil
}
