package log

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
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

	assertBinaryRecords(t, filepath.Join(base, "0", "20260615_1400_0_1.logbin"), []binaryRecord{{time: first.UnixNano(), version: 0, run: 1, stream: BinaryStreamStdout, line: "first\n"}})
	assertBinaryRecords(t, filepath.Join(base, "0", "20260615_1430_0_1.logbin"), []binaryRecord{{time: second.UnixNano(), version: 0, run: 1, stream: BinaryStreamStdout, line: "second\n"}})
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

	assertBinaryRecords(t, filepath.Join(base, "0", "20260615_1430_0_1.logbin"), []binaryRecord{{time: now.UnixNano(), version: 0, run: 1, stream: BinaryStreamStdout, line: "line ends\n"}})
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

	assertBinaryRecords(t, filepath.Join(base, "0", "20260615_1430_0_1.logbin"), []binaryRecord{{time: now.UnixNano(), version: 0, run: 1, stream: BinaryStreamStdout, line: "partial"}})
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

func assertBinaryRecords(t *testing.T, path string, want []binaryRecord) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got []binaryRecord
	for len(data) > 0 {
		if len(data) < BinaryRecordMinLen {
			t.Fatalf("%s has truncated header: %d trailing bytes", path, len(data))
		}
		length := int(binary.BigEndian.Uint32(data[:4]))
		if length < BinaryRecordPayloadLen {
			t.Fatalf("%s has invalid record length %d", path, length)
		}
		recordLen := BinaryRecordLengthLen + length + BinaryRecordTrailerLen
		if len(data) < recordLen {
			t.Fatalf("%s has truncated record: got %d bytes, want %d", path, len(data), recordLen)
		}
		timestamp := int64(binary.BigEndian.Uint64(data[4:12]))
		version := int32(binary.BigEndian.Uint32(data[12:16]))
		run := int32(binary.BigEndian.Uint32(data[16:20]))
		stream := int8(data[20])
		lineLen := length - BinaryRecordPayloadLen
		if gotSuffix := int(binary.BigEndian.Uint32(data[BinaryRecordLengthLen+length : recordLen])); gotSuffix != length {
			t.Fatalf("suffix length = %d, want %d", gotSuffix, length)
		}
		data = data[21:]
		if len(data) < lineLen {
			t.Fatalf("%s has truncated line: got %d bytes, want %d", path, len(data), lineLen)
		}
		got = append(got, binaryRecord{time: timestamp, version: version, run: run, stream: stream, line: string(data[:lineLen])})
		data = data[lineLen+BinaryRecordTrailerLen:]
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
