package logv2

import (
	"encoding/binary"
	"hash/crc32"
	"time"
)

const (
	StreamStdout int8 = 0
	StreamStderr int8 = 1

	// RecordMagic is never a valid byte in well formed utf-8, in any position.
	// Log lines are arbitrary bytes though, so it is a resync hint only: a
	// candidate is confirmed by the length range, the crc and the trailer.
	RecordMagic byte = 0xfe

	RecordMagicLen  = 1
	RecordLengthLen = 4
	RecordCRCLen    = 4
	RecordHeaderLen = RecordMagicLen + RecordLengthLen

	payloadTimeOff       = 0
	payloadVersionOff    = 8
	payloadRunOff        = 12
	payloadDeploymentOff = 16
	payloadNodeOff       = 20
	payloadOrdinalOff    = 24
	payloadStreamOff     = 28

	RecordPayloadHeaderLen = payloadStreamOff + 1
	RecordOverheadLen      = RecordHeaderLen + RecordCRCLen + RecordLengthLen
	RecordMinLen           = RecordOverheadLen + RecordPayloadHeaderLen
	MaxLineLen             = 64 * 1024
	RecordMaxPayloadLen    = RecordPayloadHeaderLen + MaxLineLen
	RecordMaxLen           = RecordOverheadLen + RecordMaxPayloadLen
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// RecordMeta is every field of a log record other than the timestamp and the
// line itself. All of it is written into the payload header so a wal record is
// self describing: a reader needs no context beyond the bytes to reconstruct
// the line.
type RecordMeta struct {
	Version         int32
	Run             int32
	Deployment      int32
	Node            int32
	InstanceOrdinal int32
	Stream          int8
}

func EncodeRecord(t time.Time, m RecordMeta, line []byte) []byte {
	payloadLen := RecordPayloadHeaderLen + len(line)
	record := make([]byte, RecordOverheadLen+payloadLen)
	record[0] = RecordMagic
	binary.BigEndian.PutUint32(record[RecordMagicLen:RecordHeaderLen], uint32(payloadLen))
	payload := record[RecordHeaderLen : RecordHeaderLen+payloadLen]
	binary.BigEndian.PutUint64(payload[payloadTimeOff:], uint64(t.UnixNano()))
	binary.BigEndian.PutUint32(payload[payloadVersionOff:], uint32(m.Version))
	binary.BigEndian.PutUint32(payload[payloadRunOff:], uint32(m.Run))
	binary.BigEndian.PutUint32(payload[payloadDeploymentOff:], uint32(m.Deployment))
	binary.BigEndian.PutUint32(payload[payloadNodeOff:], uint32(m.Node))
	binary.BigEndian.PutUint32(payload[payloadOrdinalOff:], uint32(m.InstanceOrdinal))
	payload[payloadStreamOff] = byte(m.Stream)
	copy(payload[RecordPayloadHeaderLen:], line)
	crcAt := RecordHeaderLen + payloadLen
	binary.BigEndian.PutUint32(record[crcAt:crcAt+RecordCRCLen], crc32.Checksum(payload, crcTable))
	binary.BigEndian.PutUint32(record[crcAt+RecordCRCLen:], uint32(payloadLen))
	return record
}

// DecodePayloadHeader reads the fixed header of a payload whose length has
// already been validated as at least RecordPayloadHeaderLen. It is the single
// decode path shared by every reader of the format.
func DecodePayloadHeader(payload []byte) (int64, RecordMeta) {
	return int64(binary.BigEndian.Uint64(payload[payloadTimeOff:])), RecordMeta{
		Version:         int32(binary.BigEndian.Uint32(payload[payloadVersionOff:])),
		Run:             int32(binary.BigEndian.Uint32(payload[payloadRunOff:])),
		Deployment:      int32(binary.BigEndian.Uint32(payload[payloadDeploymentOff:])),
		Node:            int32(binary.BigEndian.Uint32(payload[payloadNodeOff:])),
		InstanceOrdinal: int32(binary.BigEndian.Uint32(payload[payloadOrdinalOff:])),
		Stream:          int8(payload[payloadStreamOff]),
	}
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
