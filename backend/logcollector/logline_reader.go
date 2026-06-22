package logcollector

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/logconsumer"
)

const (
	LogLineMarkerNone   int32 = -1
	LogLineMarkerEnd    int32 = int32(logconsumer.SplitMarkerEnd)
	LogLineMarkerRotate int32 = int32(logconsumer.SplitMarkerRotate)
)

type LogLineReader struct {
	r       *bufio.Reader
	pending *LogLine
}

type SourceFileLogLineReader struct {
	path string
	file *os.File
	r    *LogLineReader
}

func NewLogLineReader(r io.Reader) *LogLineReader {
	if br, ok := r.(*bufio.Reader); ok {
		return &LogLineReader{r: br}
	}
	return &LogLineReader{r: bufio.NewReader(r)}
}

func NewSourceFileLogLineReader(deploymentID int32, configVersion int32, runNumber int32, source string) (*SourceFileLogLineReader, error) {
	dir := filepath.Join(ainit.StaticConfig.RunOutputDir, strconv.Itoa(int(deploymentID)), strconv.Itoa(int(configVersion)), strconv.Itoa(int(runNumber)))
	files, err := streamSourceFiles(dir, source)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, &os.PathError{Op: "open", Path: filepath.Join(dir, source+"0.logbin"), Err: os.ErrNotExist}
	}
	r := &SourceFileLogLineReader{}
	if err := r.open(files[0].path); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *SourceFileLogLineReader) ReadLine() (LogLine, int32, error) {
	for {
		line, marker, err := r.r.ReadLine()
		if !errors.Is(err, io.EOF) {
			return line, marker, err
		}
		if ok, openErr := r.openNext(); openErr != nil {
			return LogLine{}, LogLineMarkerNone, openErr
		} else if !ok {
			return line, marker, err
		}
	}
}

func (r *SourceFileLogLineReader) SeekToNextLineAfter(l LogLine) (marker int32, err error) {
	for {
		line, marker, err := r.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) && marker != LogLineMarkerNone {
				return marker, nil
			}
			return LogLineMarkerNone, err
		}
		if compareLogLine(line, l) > 0 {
			r.r.pending = &line
			return LogLineMarkerNone, nil
		}
	}
}

func (r *SourceFileLogLineReader) Close() error {
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

func (r *SourceFileLogLineReader) open(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	if r.file != nil {
		_ = r.file.Close()
	}
	r.path = path
	r.file = file
	r.r = NewLogLineReader(file)
	return nil
}

func (r *SourceFileLogLineReader) openNext() (bool, error) {
	next, ok := nextSourceFilePath(r.path)
	if !ok {
		return false, nil
	}
	// The log writer contract guarantees the next log file is created before the
	// marker is written, so EOF can probe the next indexed file immediately.
	if _, err := os.Stat(next); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, r.open(next)
}

func (r *LogLineReader) ReadLine() (LogLine, int32, error) {
	if r.pending != nil {
		line := *r.pending
		r.pending = nil
		return line, LogLineMarkerNone, nil
	}

	var prefix [logconsumer.SplitRecordLengthLen]byte
	if _, err := io.ReadFull(r.r, prefix[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return LogLine{}, LogLineMarkerNone, io.EOF
		}
		return LogLine{}, LogLineMarkerNone, err
	}

	length := int(binary.BigEndian.Uint32(prefix[:]))
	if length < logconsumer.SplitRecordPayloadLen {
		return LogLine{}, LogLineMarkerNone, fmt.Errorf("invalid record length %d", length)
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(r.r, payload); err != nil {
		return LogLine{}, LogLineMarkerNone, err
	}

	var suffix [logconsumer.SplitRecordTrailerLen]byte
	if _, err := io.ReadFull(r.r, suffix[:]); err != nil {
		return LogLine{}, LogLineMarkerNone, err
	}
	if binary.BigEndian.Uint32(suffix[:]) != uint32(length) {
		return LogLine{}, LogLineMarkerNone, fmt.Errorf("record length suffix mismatch")
	}

	raw := make([]byte, len(prefix)+len(payload)+len(suffix))
	copy(raw[:len(prefix)], prefix[:])
	copy(raw[len(prefix):], payload)
	copy(raw[len(raw)-len(suffix):], suffix[:])

	line := LogLine{
		Time:    int64(binary.BigEndian.Uint64(payload[:8])),
		Version: int32(binary.BigEndian.Uint32(payload[8:12])),
		Run:     int32(binary.BigEndian.Uint32(payload[12:16])),
		Stream:  int8(payload[16]),
		Line:    append([]byte(nil), payload[17:]...),
		Raw:     raw,
	}
	if line.Time == 0 {
		return LogLine{}, markerForLogLine(line), io.EOF
	}
	return line, LogLineMarkerNone, nil
}

func (r *LogLineReader) SeekToNextLineAfter(l LogLine) (marker int32, err error) {
	for {
		line, marker, err := r.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) && marker != LogLineMarkerNone {
				return marker, nil
			}
			return LogLineMarkerNone, err
		}
		if compareLogLine(line, l) > 0 {
			r.pending = &line
			return LogLineMarkerNone, nil
		}
	}
}

func markerForLogLine(line LogLine) int32 {
	if line.Time != 0 {
		return LogLineMarkerNone
	}
	if line.Stream == logconsumer.SplitMarkerRotate {
		return LogLineMarkerRotate
	}
	if line.Stream == logconsumer.SplitMarkerEnd {
		return LogLineMarkerEnd
	}
	return LogLineMarkerNone
}

func nextSourceFilePath(path string) (string, bool) {
	name := filepath.Base(path)
	if !strings.HasSuffix(name, ".logbin") {
		return "", false
	}
	base := strings.TrimSuffix(name, ".logbin")
	stream := ""
	indexPart := ""
	if strings.HasPrefix(base, "stdout") {
		stream = "stdout"
		indexPart = strings.TrimPrefix(base, stream)
	} else if strings.HasPrefix(base, "stderr") {
		stream = "stderr"
		indexPart = strings.TrimPrefix(base, stream)
	} else {
		return "", false
	}
	index, err := strconv.Atoi(indexPart)
	if err != nil {
		return "", false
	}
	return filepath.Join(filepath.Dir(path), stream+strconv.Itoa(index+1)+".logbin"), true
}
