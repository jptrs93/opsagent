package logv2

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type decodedRecord struct {
	t       time.Time
	version int32
	run     int32
	stream  int8
	line    string
}

func decodeAll(t *testing.T, data []byte) []decodedRecord {
	t.Helper()
	var records []decodedRecord
	for len(data) > 0 {
		if len(data) < RecordMinLen {
			t.Fatalf("trailing %d bytes shorter than min record length", len(data))
		}
		if !bytes.Equal(data[:4], RecordMagic[:]) {
			t.Fatalf("bad magic %x", data[:4])
		}
		payloadLen := int(binary.BigEndian.Uint32(data[4:8]))
		total := RecordMagicLen + RecordLengthLen + payloadLen + RecordCRCLen + RecordLengthLen
		if len(data) < total {
			t.Fatalf("record length %d exceeds remaining %d bytes", total, len(data))
		}
		payload := data[8 : 8+payloadLen]
		crc := binary.BigEndian.Uint32(data[8+payloadLen : 12+payloadLen])
		if crc != PayloadCRC(payload) {
			t.Fatalf("crc mismatch")
		}
		trailer := int(binary.BigEndian.Uint32(data[12+payloadLen : total]))
		if trailer != payloadLen {
			t.Fatalf("trailer length %d != payload length %d", trailer, payloadLen)
		}
		records = append(records, decodedRecord{
			t:       time.Unix(0, int64(binary.BigEndian.Uint64(payload[:8]))).UTC(),
			version: int32(binary.BigEndian.Uint32(payload[8:12])),
			run:     int32(binary.BigEndian.Uint32(payload[12:16])),
			stream:  int8(payload[16]),
			line:    string(payload[RecordPayloadHeaderLen:]),
		})
		data = data[total:]
	}
	return records
}

func readBucketFile(t *testing.T, dir string, bucket time.Time) []decodedRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, bucket.Format("20060102_1504")+".wal"))
	if err != nil {
		t.Fatalf("reading bucket file: %v", err)
	}
	return decodeAll(t, data)
}

func TestAppendersInterleaveIntoSharedBucketFile(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, time.July, 17, 12, 10, 0, 0, time.UTC)

	stdout, err := NewAppender(dir, 3, 7, StreamStdout)
	if err != nil {
		t.Fatalf("new stdout appender: %v", err)
	}
	stderr, err := NewAppender(dir, 3, 7, StreamStderr)
	if err != nil {
		t.Fatalf("new stderr appender: %v", err)
	}

	stdout.Append(ts, []byte("out one\n"))
	stderr.Append(ts.Add(time.Second), []byte("err one\n"))
	stdout.Append(ts.Add(2*time.Second), []byte("out two"))

	if err := stdout.Close(); err != nil {
		t.Fatalf("closing stdout appender: %v", err)
	}
	if err := stderr.Close(); err != nil {
		t.Fatalf("closing stderr appender: %v", err)
	}

	records := readBucketFile(t, dir, time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC))
	if len(records) != 3 {
		t.Fatalf("got %d records, want 3", len(records))
	}
	want := []decodedRecord{
		{t: ts, version: 3, run: 7, stream: StreamStdout, line: "out one\n"},
		{t: ts.Add(time.Second), version: 3, run: 7, stream: StreamStderr, line: "err one\n"},
		{t: ts.Add(2 * time.Second), version: 3, run: 7, stream: StreamStdout, line: "out two"},
	}
	for i, w := range want {
		if records[i] != w {
			t.Errorf("record %d = %+v, want %+v", i, records[i], w)
		}
	}
}

func TestAppendRollsBucketFiles(t *testing.T) {
	dir := t.TempDir()
	a, err := NewAppender(dir, 1, 1, StreamStdout)
	if err != nil {
		t.Fatalf("new appender: %v", err)
	}
	defer a.Close()

	a.Append(time.Date(2026, time.July, 17, 12, 29, 59, 0, time.UTC), []byte("first bucket\n"))
	a.Append(time.Date(2026, time.July, 17, 12, 30, 1, 0, time.UTC), []byte("second bucket\n"))

	first := readBucketFile(t, dir, time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC))
	second := readBucketFile(t, dir, time.Date(2026, time.July, 17, 12, 30, 0, 0, time.UTC))
	if len(first) != 1 || first[0].line != "first bucket\n" {
		t.Fatalf("first bucket records = %+v", first)
	}
	if len(second) != 1 || second[0].line != "second bucket\n" {
		t.Fatalf("second bucket records = %+v", second)
	}
}

func TestAppendDropAndResumeEmitsMarker(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based failure injection does not work as root")
	}
	dir := t.TempDir()
	a, err := NewAppender(dir, 2, 4, StreamStderr)
	if err != nil {
		t.Fatalf("new appender: %v", err)
	}
	defer a.Close()

	a.Append(time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC), []byte("before\n"))

	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	restored := false
	restore := func() {
		if !restored {
			restored = true
			if err := os.Chmod(dir, 0o750); err != nil {
				t.Fatalf("restoring dir perms: %v", err)
			}
		}
	}
	defer restore()

	dropOne := time.Date(2026, time.July, 17, 12, 31, 0, 0, time.UTC)
	dropTwo := time.Date(2026, time.July, 17, 12, 32, 0, 0, time.UTC)
	a.Append(dropOne, []byte("lost one\n"))
	a.Append(dropTwo, []byte("lost two\n"))
	if a.dropped != 2 {
		t.Fatalf("dropped = %d, want 2", a.dropped)
	}

	restore()
	after := time.Date(2026, time.July, 17, 12, 33, 0, 0, time.UTC)
	a.Append(after, []byte("after\n"))

	records := readBucketFile(t, dir, time.Date(2026, time.July, 17, 12, 30, 0, 0, time.UTC))
	if len(records) != 2 {
		t.Fatalf("got %d records, want marker + line", len(records))
	}
	marker := records[0]
	if !strings.HasPrefix(marker.line, "opendeploy: dropped 2 log lines between ") {
		t.Fatalf("marker line = %q", marker.line)
	}
	if !strings.Contains(marker.line, dropOne.Format(time.RFC3339Nano)) || !strings.Contains(marker.line, dropTwo.Format(time.RFC3339Nano)) {
		t.Fatalf("marker line missing drop window: %q", marker.line)
	}
	if !marker.t.Equal(after) || marker.stream != StreamStderr {
		t.Fatalf("marker record = %+v", marker)
	}
	if records[1].line != "after\n" {
		t.Fatalf("resumed line = %q", records[1].line)
	}
	if a.dropped != 0 {
		t.Fatalf("dropped counter not reset: %d", a.dropped)
	}
}
