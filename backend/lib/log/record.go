package log

import (
	"encoding/binary"
	"time"
)

const (
	BinaryStreamStdout int8 = 0
	BinaryStreamStderr int8 = 1

	BinaryRecordLengthLen  = 4
	BinaryRecordPayloadLen = 8 + 4 + 4 + 1
	BinaryRecordHeaderLen  = BinaryRecordLengthLen + BinaryRecordPayloadLen
	BinaryRecordTrailerLen = 4
	BinaryRecordMinLen     = BinaryRecordHeaderLen + BinaryRecordTrailerLen
)

func logBucket(t time.Time) time.Time {
	t = t.UTC()
	minute := 0
	if t.Minute() >= 30 {
		minute = 30
	}
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), minute, 0, 0, time.UTC)
}

func EncodeBinaryRecord(t time.Time, version int32, run int32, stream int8, line []byte) []byte {
	length := BinaryRecordPayloadLen + len(line)
	record := make([]byte, BinaryRecordLengthLen+length+BinaryRecordTrailerLen)
	binary.BigEndian.PutUint32(record[:4], uint32(length))
	binary.BigEndian.PutUint64(record[4:12], uint64(t.UnixNano()))
	binary.BigEndian.PutUint32(record[12:16], uint32(version))
	binary.BigEndian.PutUint32(record[16:20], uint32(run))
	record[20] = byte(stream)
	copy(record[21:], line)
	binary.BigEndian.PutUint32(record[len(record)-4:], uint32(length))
	return record
}
