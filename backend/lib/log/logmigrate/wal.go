package logmigrate

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	logv2 "github.com/jptrs93/opsagent/backend/lib/log/v2"
)

const walExt = ".wal"

type walStats struct {
	modern  int64
	legacy  int64
	invalid int64
	torn    bool
}

func checkWALDir(ctx context.Context, dir string, deploymentID int, s *Summary) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		slog.WarnContext(ctx, "listing wal deployment dir failed", "dep", deploymentID, "err", err)
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, walExt) {
			s.StrayFiles++
			slog.WarnContext(ctx, "stray entry in wal deployment dir: "+name, "dep", deploymentID)
			continue
		}
		s.WALFilesScanned++
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			s.WALFilesFailed++
			slog.WarnContext(ctx, "reading wal file "+name+" failed", "dep", deploymentID, "err", err)
			continue
		}
		st := walkWALFrames(data)
		if st.torn {
			// Normal for the live bucket the agent is appending to right now.
			s.WALFilesTornTail++
		}
		if st.legacy > 0 {
			s.WALFilesLegacy++
			s.WALLegacyFrames += st.legacy
			slog.WarnContext(ctx, fmt.Sprintf("wal file %s contains %d legacy-format frames (%d modern)", name, st.legacy, st.modern), "dep", deploymentID)
		}
		if st.invalid > 0 {
			slog.WarnContext(ctx, fmt.Sprintf("wal file %s contains %d invalid regions", name, st.invalid), "dep", deploymentID)
		}
	}
}

// walkWALFrames classifies every frame in a WAL file by magic. It validates
// each candidate fully (length bounds, trailer length, crc, and the format
// byte for current-layout frames) so a magic byte inside a log line can never
// be miscounted; on an invalid candidate it resyncs at the next magic byte,
// mirroring the reader's recovery behaviour. A truncated final frame is
// reported as a torn tail, not an error: the live bucket is appended to while
// we read.
func walkWALFrames(data []byte) walStats {
	var st walStats
	i := 0
	resync := func(from int) {
		st.invalid++
		next := nextMagicIndex(data, from)
		if next < 0 {
			i = len(data)
			return
		}
		i = next
	}
	for i < len(data) {
		magic := data[i]
		if magic != logv2.RecordMagic && magic != logv2.RecordMagicLegacy {
			resync(i + 1)
			continue
		}
		if i+logv2.RecordHeaderLen > len(data) {
			st.torn = true
			return st
		}
		payloadLen := int(binary.BigEndian.Uint32(data[i+logv2.RecordMagicLen : i+logv2.RecordHeaderLen]))
		minPayload, maxPayload := logv2.RecordPayloadHeaderLen, logv2.RecordMaxPayloadLen
		if magic == logv2.RecordMagicLegacy {
			minPayload = logv2.RecordLegacyPayloadHeaderLen
			maxPayload = logv2.RecordLegacyPayloadHeaderLen + logv2.MaxLineLen
		}
		if payloadLen < minPayload || payloadLen > maxPayload {
			resync(i + 1)
			continue
		}
		end := i + logv2.RecordOverheadLen + payloadLen
		if end > len(data) {
			st.torn = true
			return st
		}
		payload := data[i+logv2.RecordHeaderLen : i+logv2.RecordHeaderLen+payloadLen]
		crc := binary.BigEndian.Uint32(data[i+logv2.RecordHeaderLen+payloadLen:])
		trailer := int(binary.BigEndian.Uint32(data[end-logv2.RecordLengthLen:]))
		if trailer != payloadLen || crc != logv2.PayloadCRC(payload) ||
			(magic == logv2.RecordMagic && payload[0] != logv2.RecordFormatVersion) {
			resync(i + 1)
			continue
		}
		if magic == logv2.RecordMagic {
			st.modern++
		} else {
			st.legacy++
		}
		i = end
	}
	return st
}

func nextMagicIndex(data []byte, from int) int {
	if from >= len(data) {
		return -1
	}
	modern := bytes.IndexByte(data[from:], logv2.RecordMagic)
	legacy := bytes.IndexByte(data[from:], logv2.RecordMagicLegacy)
	switch {
	case modern < 0 && legacy < 0:
		return -1
	case modern < 0:
		return from + legacy
	case legacy < 0:
		return from + modern
	default:
		return from + min(modern, legacy)
	}
}
