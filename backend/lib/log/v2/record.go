package logv2

import (
	"encoding/binary"
	"hash/crc32"
	"time"
)

const (
	StreamStdout int8 = 0
	StreamStderr int8 = 1

	RecordMagicLen         = 4
	RecordLengthLen        = 4
	RecordCRCLen           = 4
	RecordPayloadHeaderLen = 8 + 4 + 4 + 1
	RecordOverheadLen      = RecordMagicLen + RecordLengthLen + RecordCRCLen + RecordLengthLen
	RecordMinLen           = RecordOverheadLen + RecordPayloadHeaderLen
)

var (
	RecordMagic = [4]byte{0x9d, 'O', 'L', '2'}
	crcTable    = crc32.MakeTable(crc32.Castagnoli)
)

func EncodeRecord(t time.Time, version int32, run int32, stream int8, line []byte) []byte {
	payloadLen := RecordPayloadHeaderLen + len(line)
	record := make([]byte, RecordMagicLen+RecordLengthLen+payloadLen+RecordCRCLen+RecordLengthLen)
	copy(record[:4], RecordMagic[:])
	binary.BigEndian.PutUint32(record[4:8], uint32(payloadLen))
	payload := record[8 : 8+payloadLen]
	binary.BigEndian.PutUint64(payload[:8], uint64(t.UnixNano()))
	binary.BigEndian.PutUint32(payload[8:12], uint32(version))
	binary.BigEndian.PutUint32(payload[12:16], uint32(run))
	payload[16] = byte(stream)
	copy(payload[RecordPayloadHeaderLen:], line)
	binary.BigEndian.PutUint32(record[8+payloadLen:12+payloadLen], crc32.Checksum(payload, crcTable))
	binary.BigEndian.PutUint32(record[12+payloadLen:], uint32(payloadLen))
	return record
}

func PayloadCRC(payload []byte) uint32 {
	return crc32.Checksum(payload, crcTable)
}

func logBucket(t time.Time) time.Time {
	t = t.UTC()
	minute := 0
	if t.Minute() >= 30 {
		minute = 30
	}
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), minute, 0, 0, time.UTC)
}
