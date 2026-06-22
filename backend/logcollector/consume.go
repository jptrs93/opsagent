package logcollector

import (
	"errors"
	"io"
	"iter"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/logconsumer"
)

type indexedSourceFile struct {
	index int
	path  string
}

func consumeGreaterThan(lastProcessed LogLine, deployment int32, version int32, runNumber int32) iter.Seq2[LogLine, error] {
	return func(yield func(LogLine, error) bool) {
		dir := filepath.Join(ainit.StaticConfig.RunOutputDir, strconv.Itoa(int(deployment)), strconv.Itoa(int(version)), strconv.Itoa(int(runNumber)))
		for _, stream := range []string{"stdout", "stderr"} {
			if !consumeStreamGreaterThan(lastProcessed, dir, stream, yield) {
				return
			}
		}
	}
}

func consumeStreamGreaterThan(lastProcessed LogLine, dir string, stream string, yield func(LogLine, error) bool) bool {
	files, err := streamSourceFiles(dir, stream)
	if err != nil {
		return yield(LogLine{}, err)
	}
	if len(files) == 0 {
		return true
	}
	byIndex := map[int]string{}
	for _, file := range files {
		byIndex[file.index] = file.path
	}
	index := files[0].index
	for {
		path, ok := byIndex[index]
		if !ok {
			return true
		}
		marker, ok := consumeSourceFileGreaterThan(lastProcessed, path, yield)
		if !ok {
			return false
		}
		if marker != sourceMarkerRotate {
			return true
		}
		index++
	}
}

func consumeSourceFileGreaterThan(lastProcessed LogLine, path string, yield func(LogLine, error) bool) (sourceMarker, bool) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return sourceMarkerNone, true
		}
		return sourceMarkerNone, yield(LogLine{}, err)
	}
	defer f.Close()
	for {
		line, err := readRecord(f)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return sourceMarkerNone, true
			}
			return sourceMarkerNone, yield(LogLine{}, err)
		}
		if line.Time == 0 {
			if line.Stream == logconsumer.SplitMarkerRotate {
				return sourceMarkerRotate, true
			}
			return sourceMarkerEnd, true
		}
		if compareLogLine(line, lastProcessed) > 0 && !yield(line, nil) {
			return sourceMarkerNone, false
		}
	}
}

func streamSourceFiles(dir string, stream string) ([]indexedSourceFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []indexedSourceFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, stream) || !strings.HasSuffix(name, ".logbin") {
			continue
		}
		index, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, stream), ".logbin"))
		if err != nil {
			continue
		}
		files = append(files, indexedSourceFile{index: index, path: filepath.Join(dir, name)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].index < files[j].index })
	return files, nil
}

func parsePathInt32(value string) (int32, error) {
	parsed, err := strconv.ParseInt(value, 10, 32)
	return int32(parsed), err
}
