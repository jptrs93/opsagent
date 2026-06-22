package logconsumer

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSplitLogBucketUsesUTCThirtyMinuteWindows(t *testing.T) {
	tests := map[time.Time]string{
		time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC):       "20260615_1400",
		time.Date(2026, 6, 15, 14, 29, 59, 0, time.UTC):     "20260615_1400",
		time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC):      "20260615_1430",
		time.Date(2026, 6, 15, 14, 59, 59, 0, time.UTC):     "20260615_1430",
		time.Date(2026, 6, 15, 14, 29, 59, 0, fixedZone(2)): "20260615_1200",
	}
	for input, want := range tests {
		if got := splitLogBucket(input).Format("20060102_1504"); got != want {
			t.Fatalf("splitLogBucket(%s) = %q, want %q", input, got, want)
		}
	}
}

func TestSplitRotatingWriterWritesBinaryRecordsAndRotates(t *testing.T) {
	base := filepath.Join(t.TempDir(), "12", "34", "1")
	if err := os.MkdirAll(base, 0o750); err != nil {
		t.Fatal(err)
	}
	w := &splitRotatingWriter{basePath: base, stream: "stdout", streamID: SplitStreamStdout, version: 34, run: 1}
	defer w.Close()

	first := time.Date(2026, 6, 15, 14, 29, 59, 123456789, time.UTC)
	if err := w.writeLineAt(first, []byte("first\n")); err != nil {
		t.Fatalf("write first record: %v", err)
	}
	second := time.Date(2026, 6, 15, 14, 30, 0, 987654321, time.UTC)
	if err := w.writeLineAt(second, []byte("second\n")); err != nil {
		t.Fatalf("write second record: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	assertSplitRecords(t, filepath.Join(base, "stdout0.logbin"), []splitRecord{{time: first.UnixNano(), version: 34, run: 1, stream: SplitStreamStdout, line: "first\n"}, {stream: SplitMarkerRotate, marker: SplitMarkerRotate}})
	assertSplitRecords(t, filepath.Join(base, "stdout1.logbin"), []splitRecord{{time: second.UnixNano(), version: 34, run: 1, stream: SplitStreamStdout, line: "second\n"}, {end: true}})
}

func TestSplitRotatingWriterKeepsStreamsSeparate(t *testing.T) {
	base := filepath.Join(t.TempDir(), "12", "34", "1")
	if err := os.MkdirAll(base, 0o750); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 15, 14, 30, 0, 1, time.UTC)
	stdout := &splitRotatingWriter{basePath: base, stream: "stdout", streamID: SplitStreamStdout, version: 34, run: 1}
	stderr := &splitRotatingWriter{basePath: base, stream: "stderr", streamID: SplitStreamStderr, version: 34, run: 1}
	if err := stdout.writeLineAt(now, []byte("out\n")); err != nil {
		t.Fatalf("write stdout: %v", err)
	}
	if err := stderr.writeLineAt(now, []byte("err\n")); err != nil {
		t.Fatalf("write stderr: %v", err)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stderr.Close(); err != nil {
		t.Fatal(err)
	}

	assertSplitRecords(t, filepath.Join(base, "stdout0.logbin"), []splitRecord{{time: now.UnixNano(), version: 34, run: 1, stream: SplitStreamStdout, line: "out\n"}, {end: true}})
	assertSplitRecords(t, filepath.Join(base, "stderr0.logbin"), []splitRecord{{time: now.UnixNano(), version: 34, run: 1, stream: SplitStreamStderr, line: "err\n"}, {end: true}})
}

func TestSplitRotatingWriterStartsAfterExistingFiles(t *testing.T) {
	base := filepath.Join(t.TempDir(), "12", "34", "1")
	if err := os.MkdirAll(base, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "stdout0.logbin"), nil, 0o640); err != nil {
		t.Fatal(err)
	}
	w, err := newSplitRotatingWriter(base, "stdout", SplitStreamStdout, 34, 1)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := w.writeLineAt(now, []byte("new\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	assertSplitRecords(t, filepath.Join(base, "stdout1.logbin"), []splitRecord{{time: now.UnixNano(), version: 34, run: 1, stream: SplitStreamStdout, line: "new\n"}, {end: true}})
}

func TestProcessSplitLinesPreservesRawLineBytes(t *testing.T) {
	base := filepath.Join(t.TempDir(), "12", "34", "1")
	if err := os.MkdirAll(base, 0o750); err != nil {
		t.Fatal(err)
	}
	w := &splitRotatingWriter{basePath: base, stream: "stdout", streamID: SplitStreamStdout, version: 34, run: 1}
	now := time.Date(2026, 6, 15, 14, 30, 0, 42, time.UTC)
	if err := processSplitLinesWithClock(strings.NewReader("first\npartial"), w, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	assertSplitRecords(t, filepath.Join(base, "stdout0.logbin"), []splitRecord{
		{time: now.UnixNano(), version: 34, run: 1, stream: SplitStreamStdout, line: "first\n"},
		{time: now.UnixNano(), version: 34, run: 1, stream: SplitStreamStdout, line: "partial"},
		{end: true},
	})
}

func TestSplitVersionRunFromBasePath(t *testing.T) {
	version, run, err := splitVersionRunFromBasePath(filepath.Join("/var/lib/opendeploy-run-logs", "12", "34", "5"))
	if err != nil {
		t.Fatal(err)
	}
	if version != 34 || run != 5 {
		t.Fatalf("version, run = %d, %d; want 34, 5", version, run)
	}
}

type splitRecord struct {
	time    int64
	version int32
	run     int32
	stream  int8
	line    string
	marker  int8
	end     bool
}

func assertSplitRecords(t *testing.T, path string, want []splitRecord) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got []splitRecord
	for len(data) > 0 {
		if len(data) < SplitRecordMinLen {
			t.Fatalf("%s has truncated header: %d trailing bytes", path, len(data))
		}
		length := int(binary.BigEndian.Uint32(data[:4]))
		if length < SplitRecordPayloadLen {
			t.Fatalf("%s has invalid record length %d", path, length)
		}
		recordLen := SplitRecordLengthLen + length + SplitRecordTrailerLen
		if len(data) < recordLen {
			t.Fatalf("%s has truncated record: got %d bytes, want %d", path, len(data), recordLen)
		}
		timestamp := int64(binary.BigEndian.Uint64(data[4:12]))
		version := int32(binary.BigEndian.Uint32(data[12:16]))
		run := int32(binary.BigEndian.Uint32(data[16:20]))
		stream := int8(data[20])
		lineLen := length - SplitRecordPayloadLen
		if gotSuffix := int(binary.BigEndian.Uint32(data[SplitRecordLengthLen+length : recordLen])); gotSuffix != length {
			t.Fatalf("suffix length = %d, want %d", gotSuffix, length)
		}
		data = data[21:]
		if len(data) < lineLen {
			t.Fatalf("%s has truncated line: got %d bytes, want %d", path, len(data), lineLen)
		}
		gotRecord := splitRecord{time: timestamp, version: version, run: run, stream: stream, line: string(data[:lineLen])}
		if timestamp == 0 {
			gotRecord.marker = stream
			gotRecord.end = stream == SplitMarkerEnd
		}
		got = append(got, gotRecord)
		data = data[lineLen+SplitRecordTrailerLen:]
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

func fixedZone(hours int) *time.Location {
	return time.FixedZone("fixed", hours*60*60)
}
