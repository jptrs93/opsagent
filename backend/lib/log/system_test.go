package log

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	logv2 "github.com/jptrs93/opsagent/backend/lib/log/v2"
)

func TestSystemLogBasePathUsesDeploymentZeroDir(t *testing.T) {
	got := SystemLogBasePath("/var/lib/opendeploy-run-logs")
	want := filepath.Join("/var/lib/opendeploy-run-logs", "0")
	if got != want {
		t.Fatalf("SystemLogBasePath = %q, want %q", got, want)
	}
}

func TestSystemLogWriterWritesMergedBinaryRecords(t *testing.T) {
	base := t.TempDir()
	first := time.Date(2026, 6, 15, 14, 29, 59, 123456789, time.UTC)
	now := first
	w, err := newSystemLogWriterWithClock(SystemLogBasePath(base), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("first\n")); err != nil {
		t.Fatalf("write first: %v", err)
	}
	second := time.Date(2026, 6, 15, 14, 30, 0, 987654321, time.UTC)
	now = second
	if _, err := w.Write([]byte("second\n")); err != nil {
		t.Fatalf("write second: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	assertWalRecords(t, filepath.Join(base, "0", "20260615_1400.wal"), []binaryRecord{{time: first.UnixNano(), version: 0, run: 1, stream: BinaryStreamStdout, line: "first\n"}})
	assertWalRecords(t, filepath.Join(base, "0", "20260615_1430.wal"), []binaryRecord{{time: second.UnixNano(), version: 0, run: 1, stream: BinaryStreamStdout, line: "second\n"}})
}

func TestSystemLogWriterCombinesPartialWrites(t *testing.T) {
	base := t.TempDir()
	now := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	w, err := newSystemLogWriterWithClock(SystemLogBasePath(base), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("line")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(" ends\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	assertWalRecords(t, filepath.Join(base, "0", "20260615_1430.wal"), []binaryRecord{{time: now.UnixNano(), version: 0, run: 1, stream: BinaryStreamStdout, line: "line ends\n"}})
}

func TestSystemLogWriterFlushesPartialLineOnClose(t *testing.T) {
	base := t.TempDir()
	now := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	w, err := newSystemLogWriterWithClock(SystemLogBasePath(base), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	assertWalRecords(t, filepath.Join(base, "0", "20260615_1430.wal"), []binaryRecord{{time: now.UnixNano(), version: 0, run: 1, stream: BinaryStreamStdout, line: "partial"}})
}

func TestLogBucketRoundsToHalfHourUTC(t *testing.T) {
	tests := map[string]string{
		"2026-06-15T14:00:00Z":      "20260615_1400",
		"2026-06-15T14:29:59Z":      "20260615_1400",
		"2026-06-15T14:30:00Z":      "20260615_1430",
		"2026-06-15T14:59:59Z":      "20260615_1430",
		"2026-06-15T14:45:00+02:00": "20260615_1230",
	}
	for input, want := range tests {
		tm, err := time.Parse(time.RFC3339, input)
		if err != nil {
			t.Fatal(err)
		}
		if got := logBucket(tm).Format("20060102_1504"); got != want {
			t.Fatalf("logBucket(%s) = %q, want %q", input, got, want)
		}
	}
}

func TestFileModeForDir(t *testing.T) {
	tests := map[os.FileMode]os.FileMode{
		0o750: 0o640,
		0o770: 0o660,
		0o700: 0o600,
	}
	for dirMode, want := range tests {
		if got := fileModeForDir(dirMode); got != want {
			t.Fatalf("fileModeForDir(%o) = %o, want %o", dirMode, got, want)
		}
	}
}

type binaryRecord struct {
	time    int64
	version int32
	run     int32
	stream  int8
	line    string
}

func assertWalRecords(t *testing.T, path string, want []binaryRecord) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got []binaryRecord
	for len(data) > 0 {
		if len(data) < logv2.RecordMinLen {
			t.Fatalf("%s has truncated record: %d trailing bytes", path, len(data))
		}
		if [4]byte(data[:4]) != logv2.RecordMagic {
			t.Fatalf("%s has bad record magic %x", path, data[:4])
		}
		payloadLen := int(binary.BigEndian.Uint32(data[4:8]))
		total := logv2.RecordOverheadLen + payloadLen
		if len(data) < total {
			t.Fatalf("%s has truncated record: got %d bytes, want %d", path, len(data), total)
		}
		payload := data[8 : 8+payloadLen]
		if binary.BigEndian.Uint32(data[8+payloadLen:12+payloadLen]) != logv2.PayloadCRC(payload) {
			t.Fatalf("%s has crc mismatch", path)
		}
		if int(binary.BigEndian.Uint32(data[12+payloadLen:total])) != payloadLen {
			t.Fatalf("%s has trailer length mismatch", path)
		}
		got = append(got, binaryRecord{
			time:    int64(binary.BigEndian.Uint64(payload[:8])),
			version: int32(binary.BigEndian.Uint32(payload[8:12])),
			run:     int32(binary.BigEndian.Uint32(payload[12:16])),
			stream:  int8(payload[16]),
			line:    string(payload[logv2.RecordPayloadHeaderLen:]),
		})
		data = data[total:]
	}
	if len(got) != len(want) {
		t.Fatalf("records = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("record %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}
