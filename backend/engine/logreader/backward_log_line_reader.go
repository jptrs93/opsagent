package logreader

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jptrs93/opsagent/backend/logconsumer"
)

type BackwardLogLineReader struct {
	file          *os.File
	path          string
	offset        int64
	configVersion int
	since         time.Time
	till          *time.Time
}

func NewBackwardLogLineReader(path string, configVersion int, since time.Time, till *time.Time) (*BackwardLogLineReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &BackwardLogLineReader{
		file:          f,
		path:          path,
		offset:        info.Size(),
		configVersion: configVersion,
		since:         since,
		till:          till,
	}, nil
}

func (r *BackwardLogLineReader) Close() error {
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

func (r *BackwardLogLineReader) Next() (LogLine, error) {
	for r.offset > 0 {
		line, err := r.readPreviousRecord()
		if err != nil {
			return LogLine{}, err
		}
		if line.Time == 0 {
			continue
		}
		if r.configVersion > 0 && line.Version != int32(r.configVersion) {
			continue
		}
		t := time.Unix(0, line.Time).UTC()
		if t.Before(r.since) {
			continue
		}
		if r.till != nil && !t.Before(*r.till) {
			continue
		}
		return line, nil
	}
	return LogLine{}, io.EOF
}

func (r *BackwardLogLineReader) readPreviousRecord() (LogLine, error) {
	if r.offset < logconsumer.SplitRecordTrailerLen {
		return LogLine{}, fmt.Errorf("read %s: truncated record trailer", r.path)
	}
	var suffix [logconsumer.SplitRecordTrailerLen]byte
	if _, err := r.file.ReadAt(suffix[:], r.offset-logconsumer.SplitRecordTrailerLen); err != nil {
		return LogLine{}, fmt.Errorf("read %s: %w", r.path, err)
	}
	length := int64(binary.BigEndian.Uint32(suffix[:]))
	if length < logconsumer.SplitRecordPayloadLen {
		return LogLine{}, fmt.Errorf("read %s: invalid record length %d", r.path, length)
	}
	recordStart := r.offset - logconsumer.SplitRecordTrailerLen - length - logconsumer.SplitRecordLengthLen
	if recordStart < 0 {
		return LogLine{}, fmt.Errorf("read %s: truncated record length %d", r.path, length)
	}
	var prefix [logconsumer.SplitRecordLengthLen]byte
	if _, err := r.file.ReadAt(prefix[:], recordStart); err != nil {
		return LogLine{}, fmt.Errorf("read %s: %w", r.path, err)
	}
	if binary.BigEndian.Uint32(prefix[:]) != uint32(length) {
		return LogLine{}, fmt.Errorf("read %s: record length prefix mismatch", r.path)
	}
	payload := make([]byte, length)
	if _, err := r.file.ReadAt(payload, recordStart+logconsumer.SplitRecordLengthLen); err != nil {
		return LogLine{}, fmt.Errorf("read %s: %w", r.path, err)
	}
	r.offset = recordStart
	return LogLine{
		Time:    int64(binary.BigEndian.Uint64(payload[:8])),
		Version: int32(binary.BigEndian.Uint32(payload[8:12])),
		Run:     int32(binary.BigEndian.Uint32(payload[12:16])),
		Stream:  int8(payload[16]),
		Line:    append([]byte(nil), payload[17:]...),
	}, nil
}
