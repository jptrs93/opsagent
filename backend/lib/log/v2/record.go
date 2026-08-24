package logv2

import (
	"encoding/binary"
	"hash/crc32"
	"time"
)

const (
	StreamStdout int8 = 0
	StreamStderr int8 = 1

	// Record magics are never a valid byte in well formed utf-8, in any
	// position. Log lines are arbitrary bytes though, so they are a resync hint
	// only: a candidate is confirmed by the length range, the crc and the
	// trailer. RecordMagicLegacy frames the original layout (no format byte, no
	// seq); RecordMagic frames the current layout, whose payload leads with a
	// format version byte so future layout changes do not need another magic.
	RecordMagicLegacy byte = 0xfe
	RecordMagic       byte = 0xfd

	RecordFormatVersion byte = 1

	RecordMagicLen  = 1
	RecordLengthLen = 4
	RecordCRCLen    = 4
	RecordHeaderLen = RecordMagicLen + RecordLengthLen

	payloadFormatOff     = 0
	payloadTimeOff       = payloadFormatOff + 1
	payloadVersionOff    = payloadTimeOff + 8
	payloadRunOff        = payloadVersionOff + 4
	payloadDeploymentOff = payloadRunOff + 4
	payloadNodeOff       = payloadDeploymentOff + 4
	payloadOrdinalOff    = payloadNodeOff + 4
	payloadStreamOff     = payloadOrdinalOff + 4
	payloadSeqOff        = payloadStreamOff + 1

	RecordPayloadHeaderLen       = payloadSeqOff + 8
	RecordLegacyPayloadHeaderLen = RecordPayloadHeaderLen - 8 - 1

	RecordOverheadLen   = RecordHeaderLen + RecordCRCLen + RecordLengthLen
	RecordMinLen        = RecordOverheadLen + RecordLegacyPayloadHeaderLen
	MaxLineLen          = 64 * 1024
	RecordMaxPayloadLen = RecordPayloadHeaderLen + MaxLineLen
	RecordMaxLen        = RecordOverheadLen + RecordMaxPayloadLen
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// RecordMeta is every field of a log record other than the timestamp, the seq
// and the line itself. All of it is written into the payload header so a wal
// record is self describing: a reader needs no context beyond the bytes to
// reconstruct the line.
type RecordMeta struct {
	Version         int32
	Run             int32
	Deployment      int32
	Node            int32
	InstanceOrdinal int32
	Stream          int8
}

func EncodeRecord(t time.Time, m RecordMeta, seq int64, line []byte) []byte {
	payloadLen := RecordPayloadHeaderLen + len(line)
	record := make([]byte, RecordOverheadLen+payloadLen)
	record[0] = RecordMagic
	binary.BigEndian.PutUint32(record[RecordMagicLen:RecordHeaderLen], uint32(payloadLen))
	payload := record[RecordHeaderLen : RecordHeaderLen+payloadLen]
	payload[payloadFormatOff] = RecordFormatVersion
	binary.BigEndian.PutUint64(payload[payloadTimeOff:], uint64(t.UnixNano()))
	binary.BigEndian.PutUint32(payload[payloadVersionOff:], uint32(m.Version))
	binary.BigEndian.PutUint32(payload[payloadRunOff:], uint32(m.Run))
	binary.BigEndian.PutUint32(payload[payloadDeploymentOff:], uint32(m.Deployment))
	binary.BigEndian.PutUint32(payload[payloadNodeOff:], uint32(m.Node))
	binary.BigEndian.PutUint32(payload[payloadOrdinalOff:], uint32(m.InstanceOrdinal))
	payload[payloadStreamOff] = byte(m.Stream)
	binary.BigEndian.PutUint64(payload[payloadSeqOff:], uint64(seq))
	copy(payload[RecordPayloadHeaderLen:], line)
	crcAt := RecordHeaderLen + payloadLen
	binary.BigEndian.PutUint32(record[crcAt:crcAt+RecordCRCLen], crc32.Checksum(payload, crcTable))
	binary.BigEndian.PutUint32(record[crcAt+RecordCRCLen:], uint32(payloadLen))
	return record
}

// DecodePayloadHeader reads the fixed header of a current-layout payload whose
// length has already been validated as at least RecordPayloadHeaderLen and
// whose format byte is RecordFormatVersion.
func DecodePayloadHeader(payload []byte) (int64, int64, RecordMeta) {
	return int64(binary.BigEndian.Uint64(payload[payloadTimeOff:])),
		int64(binary.BigEndian.Uint64(payload[payloadSeqOff:])),
		RecordMeta{
			Version:         int32(binary.BigEndian.Uint32(payload[payloadVersionOff:])),
			Run:             int32(binary.BigEndian.Uint32(payload[payloadRunOff:])),
			Deployment:      int32(binary.BigEndian.Uint32(payload[payloadDeploymentOff:])),
			Node:            int32(binary.BigEndian.Uint32(payload[payloadNodeOff:])),
			InstanceOrdinal: int32(binary.BigEndian.Uint32(payload[payloadOrdinalOff:])),
			Stream:          int8(payload[payloadStreamOff]),
		}
}

// DecodeLegacyPayloadHeader reads the fixed header of a RecordMagicLegacy
// payload (no format byte, no seq). Legacy records report seq 0.
func DecodeLegacyPayloadHeader(payload []byte) (int64, RecordMeta) {
	const (
		timeOff       = 0
		versionOff    = 8
		runOff        = 12
		deploymentOff = 16
		nodeOff       = 20
		ordinalOff    = 24
		streamOff     = 28
	)
	return int64(binary.BigEndian.Uint64(payload[timeOff:])), RecordMeta{
		Version:         int32(binary.BigEndian.Uint32(payload[versionOff:])),
		Run:             int32(binary.BigEndian.Uint32(payload[runOff:])),
		Deployment:      int32(binary.BigEndian.Uint32(payload[deploymentOff:])),
		Node:            int32(binary.BigEndian.Uint32(payload[nodeOff:])),
		InstanceOrdinal: int32(binary.BigEndian.Uint32(payload[ordinalOff:])),
		Stream:          int8(payload[streamOff]),
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
