package logreader

import (
	"encoding/binary"
	"io"
	"os"
	"time"

	logv2 "github.com/jptrs93/opsagent/backend/lib/log/v2"
)

const maxWalPayloadLen = 1 << 24

type BackwardWalLineReader struct {
	file          *os.File
	buf           *BackwardBufferedReader
	path          string
	offset        int64
	configVersion int
	since         time.Time
	till          *time.Time
}

func NewBackwardWalLineReader(path string, configVersion int, since time.Time, till *time.Time) (*BackwardWalLineReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &BackwardWalLineReader{
		file:          f,
		buf:           NewBackwardBufferedReader(f),
		path:          path,
		offset:        info.Size(),
		configVersion: configVersion,
		since:         since,
		till:          till,
	}, nil
}

func (r *BackwardWalLineReader) Close() error {
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

func (r *BackwardWalLineReader) Next() (LogLine, error) {
	for r.offset > 0 {
		line, ok, err := r.readPreviousRecord()
		if err != nil {
			return LogLine{}, err
		}
		if !ok {
			continue
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

func (r *BackwardWalLineReader) readPreviousRecord() (LogLine, bool, error) {
	if r.offset >= int64(logv2.RecordMinLen) {
		var trailer [4]byte
		if err := r.buf.ReadAt(trailer[:], r.offset-4); err != nil {
			return LogLine{}, false, err
		}
		payloadLen := int64(binary.BigEndian.Uint32(trailer[:]))
		if payloadLen >= int64(logv2.RecordPayloadHeaderLen) && payloadLen <= maxWalPayloadLen {
			start := r.offset - int64(logv2.RecordOverheadLen) - payloadLen
			if start >= 0 {
				line, ok, err := r.parseRecordAt(start)
				if err != nil {
					return LogLine{}, false, err
				}
				if ok {
					r.offset = start
					return line, true, nil
				}
			}
		}
	}
	return r.resyncBackward()
}

func (r *BackwardWalLineReader) resyncBackward() (LogLine, bool, error) {
	for p := r.offset - int64(logv2.RecordMinLen); p >= 0; p-- {
		var magic [4]byte
		if err := r.buf.ReadAt(magic[:], p); err != nil {
			return LogLine{}, false, err
		}
		if magic != logv2.RecordMagic {
			continue
		}
		line, ok, err := r.parseRecordAt(p)
		if err != nil {
			return LogLine{}, false, err
		}
		if ok {
			r.offset = p
			return line, true, nil
		}
	}
	r.offset = 0
	return LogLine{}, false, nil
}

func (r *BackwardWalLineReader) parseRecordAt(start int64) (LogLine, bool, error) {
	var header [8]byte
	if err := r.buf.ReadAt(header[:], start); err != nil {
		return LogLine{}, false, err
	}
	if [4]byte(header[:4]) != logv2.RecordMagic {
		return LogLine{}, false, nil
	}
	payloadLen := int64(binary.BigEndian.Uint32(header[4:8]))
	if payloadLen < int64(logv2.RecordPayloadHeaderLen) || payloadLen > maxWalPayloadLen {
		return LogLine{}, false, nil
	}
	if start+int64(logv2.RecordOverheadLen)+payloadLen > r.offset {
		return LogLine{}, false, nil
	}
	payload := make([]byte, payloadLen)
	if err := r.buf.ReadAt(payload, start+8); err != nil {
		return LogLine{}, false, err
	}
	var suffix [8]byte
	if err := r.buf.ReadAt(suffix[:], start+8+payloadLen); err != nil {
		return LogLine{}, false, err
	}
	if binary.BigEndian.Uint32(suffix[:4]) != logv2.PayloadCRC(payload) {
		return LogLine{}, false, nil
	}
	if int64(binary.BigEndian.Uint32(suffix[4:8])) != payloadLen {
		return LogLine{}, false, nil
	}
	return LogLine{
		Time:    int64(binary.BigEndian.Uint64(payload[:8])),
		Version: int32(binary.BigEndian.Uint32(payload[8:12])),
		Run:     int32(binary.BigEndian.Uint32(payload[12:16])),
		Stream:  int8(payload[16]),
		Line:    append([]byte(nil), payload[logv2.RecordPayloadHeaderLen:]...),
	}, true, nil
}
