package logreader

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	logv2 "github.com/jptrs93/opsagent/backend/lib/log/v2"
)

func TestStreamLogsReadsWalRecordsNewestFirst(t *testing.T) {
	base := setRunOutputDir(t)

	writeLogFile(t, base, 42, "20260615_1430.wal",
		logv2.EncodeRecord(mustTime("2026-06-15T14:30:01Z"), 1, 2, logv2.StreamStdout, []byte("second\n")),
		logv2.EncodeRecord(mustTime("2026-06-15T14:30:02Z"), 1, 1, logv2.StreamStdout, []byte("third\n")),
		logv2.EncodeRecord(mustTime("2026-06-15T14:30:03Z"), 1, 1, logv2.StreamStderr, []byte("fourth\n")),
	)
	writeLogFile(t, base, 42, "20260615_1330.wal",
		logv2.EncodeRecord(mustTime("2026-06-15T13:59:59Z"), 2, 1, logv2.StreamStdout, []byte("first\n")),
	)

	var got []string
	for line, err := range StreamLogs(42, 0, mustTime("2026-06-15T13:00:00Z"), nil) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(line.Line))
	}
	want := []string{"fourth\n", "third\n", "second\n", "first\n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
}

func TestStreamLogsFiltersTimeRangeAndConfigVersion(t *testing.T) {
	base := setRunOutputDir(t)

	writeLogFile(t, base, 42, "20260615_1400.wal",
		logv2.EncodeRecord(mustTime("2026-06-15T14:00:00Z"), 1, 1, logv2.StreamStdout, []byte("old\n")),
	)
	writeLogFile(t, base, 42, "20260615_1430.wal",
		logv2.EncodeRecord(mustTime("2026-06-15T14:30:00Z"), 2, 1, logv2.StreamStdout, []byte("current\n")),
		logv2.EncodeRecord(mustTime("2026-06-15T14:59:59Z"), 2, 1, logv2.StreamStdout, []byte("late\n")),
	)

	till := mustTime("2026-06-15T14:59:00Z")
	var got []string
	for line, err := range StreamLogs(42, 2, mustTime("2026-06-15T14:00:00Z"), &till) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(line.Line))
	}
	want := []string{"current\n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
}

func TestStreamLogsReadsDeploymentZeroRecords(t *testing.T) {
	base := setRunOutputDir(t)

	writeLogFile(t, base, 0, "20260615_1430.wal",
		logv2.EncodeRecord(mustTime("2026-06-15T14:30:02Z"), 0, 1, logv2.StreamStdout, []byte("third\n")),
		logv2.EncodeRecord(mustTime("2026-06-15T14:30:03Z"), 0, 1, logv2.StreamStdout, []byte("fourth\n")),
	)

	var got []string
	for line, err := range StreamLogs(0, 0, mustTime("2026-06-15T13:00:00Z"), nil) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(line.Line))
	}
	want := []string{"fourth\n", "third\n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
}

func TestStreamLogsIgnoresNonWalFiles(t *testing.T) {
	base := setRunOutputDir(t)

	writeLogFile(t, base, 42, "20260615_1430_1_1.logbin", []byte("not a wal record"))
	writeLogFile(t, base, 42, "20260615_1430.wal",
		logv2.EncodeRecord(mustTime("2026-06-15T14:30:01Z"), 1, 1, logv2.StreamStdout, []byte("only\n")),
	)

	var got []string
	for line, err := range StreamLogs(42, 0, mustTime("2026-06-15T14:00:00Z"), nil) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(line.Line))
	}
	want := []string{"only\n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
}

func TestStreamLogsResyncsPastTornRecords(t *testing.T) {
	base := setRunOutputDir(t)

	truncated := logv2.EncodeRecord(mustTime("2026-06-15T14:30:03Z"), 1, 1, logv2.StreamStdout, []byte("gamma\n"))
	writeLogFile(t, base, 42, "20260615_1430.wal",
		logv2.EncodeRecord(mustTime("2026-06-15T14:30:01Z"), 1, 1, logv2.StreamStdout, []byte("alpha\n")),
		bytes.Repeat([]byte{0xff}, 37),
		logv2.EncodeRecord(mustTime("2026-06-15T14:30:02Z"), 1, 1, logv2.StreamStdout, []byte("beta\n")),
		truncated[:len(truncated)/2],
	)

	var got []string
	for line, err := range StreamLogs(42, 0, mustTime("2026-06-15T14:00:00Z"), nil) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(line.Line))
	}
	want := []string{"beta\n", "alpha\n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
}

func setRunOutputDir(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	old := ainit.StaticConfig.RunOutputDir
	ainit.StaticConfig.RunOutputDir = base
	t.Cleanup(func() { ainit.StaticConfig.RunOutputDir = old })
	return base
}

func writeLogFile(t *testing.T, base string, deploymentID int, name string, chunks ...[]byte) {
	t.Helper()
	dir := filepath.Join(base, strconv.Itoa(deploymentID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var data []byte
	for _, chunk := range chunks {
		data = append(data, chunk...)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		panic(err)
	}
	return t
}
