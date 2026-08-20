package logreader

import (
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
		dir := filepath.Join(ainit.StaticConfig.RunOutputDir, fmt.Sprintf("%d", deploymentID))
		files, err := candidateLogFiles(dir, since, till)
		if err != nil {
			yield(LogLine{}, err)
			return
		}
		streamBackwardLogFiles(files, configVersion, since, till, yield)
	}
}

func streamBackwardLogFiles(paths []string, configVersion int, since time.Time, till *time.Time, yield func(LogLine, error) bool) {
	for _, path := range paths {
		reader, err := NewBackwardWalLineReader(path, configVersion, since, till)
		if err != nil {
			yield(LogLine{}, err)
			return
		}
		for {
			line, err := reader.Next()
			if err != nil {
				_ = reader.Close()
				if errors.Is(err, io.EOF) {
					break
				}
				yield(LogLine{}, err)
				return
			}
			if !yield(line, nil) {
				_ = reader.Close()
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
		if entry.IsDir() {
			continue
		}
		bucket, ok := parseWalFileName(entry.Name())
		if !ok {
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

func parseWalFileName(name string) (time.Time, bool) {
	if filepath.Ext(name) != ".wal" {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("20060102_1504", strings.TrimSuffix(name, ".wal"), time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
