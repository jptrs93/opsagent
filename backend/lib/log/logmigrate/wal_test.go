package logmigrate

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	logv2 "github.com/jptrs93/opsagent/backend/lib/log/v2"
)

func modernFrame(t *testing.T, seq int64, line string) []byte {
	t.Helper()
	return logv2.EncodeRecord(time.Unix(0, 1234), logv2.RecordMeta{Version: 1, Run: 2, Deployment: 7, Node: 1}, seq, []byte(line))
}

// legacyFrame hand-builds a RecordMagicLegacy frame: no format byte, no seq,
// 29-byte payload header laid out per logv2.DecodeLegacyPayloadHeader.
func legacyFrame(t *testing.T, line string) []byte {
	t.Helper()
	payloadLen := logv2.RecordLegacyPayloadHeaderLen + len(line)
	rec := make([]byte, logv2.RecordOverheadLen+payloadLen)
	rec[0] = logv2.RecordMagicLegacy
	binary.BigEndian.PutUint32(rec[logv2.RecordMagicLen:], uint32(payloadLen))
	payload := rec[logv2.RecordHeaderLen : logv2.RecordHeaderLen+payloadLen]
	binary.BigEndian.PutUint64(payload[0:], uint64(time.Unix(0, 1234).UnixNano()))
	binary.BigEndian.PutUint32(payload[8:], 1)  // version
	binary.BigEndian.PutUint32(payload[12:], 2) // run
	binary.BigEndian.PutUint32(payload[16:], 7) // deployment
	binary.BigEndian.PutUint32(payload[20:], 1) // node
	copy(payload[logv2.RecordLegacyPayloadHeaderLen:], line)
	crcAt := logv2.RecordHeaderLen + payloadLen
	binary.BigEndian.PutUint32(rec[crcAt:], logv2.PayloadCRC(payload))
	binary.BigEndian.PutUint32(rec[crcAt+logv2.RecordCRCLen:], uint32(payloadLen))
	return rec
}

func writeWAL(t *testing.T, walDir, dep, bucket string, chunks ...[]byte) string {
	t.Helper()
	dir := filepath.Join(walDir, dep)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, bucket+".wal")
	var data []byte
	for _, c := range chunks {
		data = append(data, c...)
	}
	if err := os.WriteFile(path, data, 0o640); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWALAllModern(t *testing.T) {
	walDir, archiveDir := t.TempDir(), t.TempDir()
	writeWAL(t, walDir, "7", "20250101_0000", modernFrame(t, 0, "a\n"), modernFrame(t, 1, "b\n"))
	s := Run(context.Background(), walDir, archiveDir)
	if s.WALFilesScanned != 1 || s.WALFilesLegacy != 0 || s.WALLegacyFrames != 0 || !s.Clean() {
		t.Fatalf("summary = %+v", s)
	}
}

func TestWALMixedFrames(t *testing.T) {
	walDir, archiveDir := t.TempDir(), t.TempDir()
	// The upgrade-window bucket: legacy frames first, modern appended after.
	writeWAL(t, walDir, "7", "20250101_0030",
		legacyFrame(t, "old1\n"), legacyFrame(t, "old2\n"), modernFrame(t, 0, "new\n"))
	s := Run(context.Background(), walDir, archiveDir)
	if s.WALFilesLegacy != 1 || s.WALLegacyFrames != 2 || s.Clean() {
		t.Fatalf("summary = %+v", s)
	}
}

func TestWALTornTail(t *testing.T) {
	walDir, archiveDir := t.TempDir(), t.TempDir()
	full := modernFrame(t, 0, "complete\n")
	partial := modernFrame(t, 1, "cut off mid write\n")
	writeWAL(t, walDir, "7", "20250101_0100", full, partial[:len(partial)-9])
	s := Run(context.Background(), walDir, archiveDir)
	if s.WALFilesTornTail != 1 || s.WALFilesFailed != 0 || !s.Clean() {
		t.Fatalf("summary = %+v", s)
	}
}

func TestWALGarbageResync(t *testing.T) {
	walDir, archiveDir := t.TempDir(), t.TempDir()
	// Junk between two valid frames, including a fake magic byte, must not
	// hide the second frame or miscount it.
	junk := []byte{0x00, logv2.RecordMagicLegacy, 0x01, 0x02, 0x03}
	writeWAL(t, walDir, "7", "20250101_0130", modernFrame(t, 0, "a\n"), junk, legacyFrame(t, "b\n"))
	s := Run(context.Background(), walDir, archiveDir)
	if s.WALFilesLegacy != 1 || s.WALLegacyFrames != 1 {
		t.Fatalf("summary = %+v", s)
	}
}

func TestWALStrayLogbin(t *testing.T) {
	walDir, archiveDir := t.TempDir(), t.TempDir()
	dir := filepath.Join(walDir, "0")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old.logbin"), []byte("legacy"), 0o640); err != nil {
		t.Fatal(err)
	}
	writeWAL(t, walDir, "0", "20250101_0200", modernFrame(t, 0, "sys\n"))
	s := Run(context.Background(), walDir, archiveDir)
	if s.StrayFiles != 1 || s.WALFilesScanned != 1 || !s.Clean() {
		t.Fatalf("summary = %+v", s)
	}
}

func TestWalkWALFramesEmpty(t *testing.T) {
	st := walkWALFrames(nil)
	if st.modern != 0 || st.legacy != 0 || st.torn {
		t.Fatalf("stats = %+v", st)
	}
}
