package logmanager

import (
	"bytes"
	"encoding/binary"

	"github.com/jptrs93/opsagent/backend/apigen"
	logv2 "github.com/jptrs93/opsagent/backend/lib/log/v2"
)

const (
	parseOK = iota
	parseIncomplete
	parseInvalid
)

// parseWalRecord decodes the record at the head of buf. On parseIncomplete the
// returned size is how many bytes the record needs in total; on parseInvalid the
// caller resyncs to the next magic candidate.
func parseWalRecord(buf []byte) (apigen.RawLogLine, int, int) {
	header := logv2.RecordHeaderLen
	if len(buf) < header {
		return apigen.RawLogLine{}, logv2.RecordMinLen, parseIncomplete
	}
	if buf[0] != logv2.RecordMagic {
		return apigen.RawLogLine{}, 0, parseInvalid
	}
	payloadLen := int(binary.BigEndian.Uint32(buf[logv2.RecordMagicLen:header]))
	if payloadLen < logv2.RecordPayloadHeaderLen || payloadLen > logv2.RecordMaxPayloadLen {
		return apigen.RawLogLine{}, 0, parseInvalid
	}
	total := logv2.RecordOverheadLen + payloadLen
	if len(buf) < total {
		return apigen.RawLogLine{}, total, parseIncomplete
	}
	payload := buf[header : header+payloadLen]
	if binary.BigEndian.Uint32(buf[header+payloadLen:header+payloadLen+logv2.RecordCRCLen]) != logv2.PayloadCRC(payload) {
		return apigen.RawLogLine{}, 0, parseInvalid
	}
	if int(binary.BigEndian.Uint32(buf[header+payloadLen+logv2.RecordCRCLen:total])) != payloadLen {
		return apigen.RawLogLine{}, 0, parseInvalid
	}
	if payload[0] != logv2.RecordFormatVersion {
		return apigen.RawLogLine{}, 0, parseInvalid
	}
	nanos, seq, meta := logv2.DecodePayloadHeader(payload)
	return apigen.RawLogLine{
		Time:            nanos,
		Version:         meta.Version,
		Run:             meta.Run,
		Stream:          int32(meta.Stream),
		Line:            bytes.Clone(payload[logv2.RecordPayloadHeaderLen:]),
		Deployment:      meta.Deployment,
		Node:            meta.Node,
		InstanceOrdinal: meta.InstanceOrdinal,
		Seq:             seq,
	}, total, parseOK
}

// nextMagicIndex returns the index of the nearest record magic candidate in
// buf, or -1.
func nextMagicIndex(buf []byte) int {
	return bytes.IndexByte(buf, logv2.RecordMagic)
}
