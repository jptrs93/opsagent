package logreader

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/logconsumer"
)

func TestStreamLogsReadsMergedRecordsNewestFirst(t *testing.T) {
	base := t.TempDir()
	old := ainit.StaticConfig.RunOutputDir
	ainit.StaticConfig.RunOutputDir = base
	t.Cleanup(func() { ainit.StaticConfig.RunOutputDir = old })

	writeMergedLog(t, base, 42, "20260615_1430_1_1.logbin",
		logconsumer.EncodeSplitRecord(mustTime("2026-06-15T14:30:01Z"), 1, 2, logconsumer.SplitStreamStdout, []byte("second\n")),
		logconsumer.EncodeSplitRecord(mustTime("2026-06-15T14:30:02Z"), 1, 1, logconsumer.SplitStreamStdout, []byte("third\n")),
		logconsumer.EncodeSplitRecord(mustTime("2026-06-15T14:30:03Z"), 1, 1, logconsumer.SplitStreamStderr, []byte("fourth\n")),
	)
	writeMergedLog(t, base, 42, "20260615_1330_2_1.logbin",
		logconsumer.EncodeSplitRecord(mustTime("2026-06-15T13:59:59Z"), 2, 1, logconsumer.SplitStreamStdout, []byte("first\n")),
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

func TestStreamLogsMergesSameBucketReadersByNewestPeek(t *testing.T) {
	base := t.TempDir()
	old := ainit.StaticConfig.RunOutputDir
	ainit.StaticConfig.RunOutputDir = base
	t.Cleanup(func() { ainit.StaticConfig.RunOutputDir = old })

	writeMergedLog(t, base, 42, "20260615_1430_1_1.logbin",
		logconsumer.EncodeSplitRecord(mustTime("2026-06-15T14:30:01Z"), 1, 1, logconsumer.SplitStreamStdout, []byte("first\n")),
		logconsumer.EncodeSplitRecord(mustTime("2026-06-15T14:30:04Z"), 1, 1, logconsumer.SplitStreamStdout, []byte("fourth\n")),
	)
	writeMergedLog(t, base, 42, "20260615_1430_1_2.logbin",
		logconsumer.EncodeSplitRecord(mustTime("2026-06-15T14:30:02Z"), 1, 2, logconsumer.SplitStreamStdout, []byte("second\n")),
		logconsumer.EncodeSplitRecord(mustTime("2026-06-15T14:30:03Z"), 1, 2, logconsumer.SplitStreamStdout, []byte("third\n")),
	)

	var got []string
	for line, err := range StreamLogs(42, 0, mustTime("2026-06-15T14:30:00Z"), nil) {
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
	base := t.TempDir()
	old := ainit.StaticConfig.RunOutputDir
	ainit.StaticConfig.RunOutputDir = base
	t.Cleanup(func() { ainit.StaticConfig.RunOutputDir = old })

	writeMergedLog(t, base, 42, "20260615_1400_1_1.logbin",
		logconsumer.EncodeSplitRecord(mustTime("2026-06-15T14:00:00Z"), 1, 1, logconsumer.SplitStreamStdout, []byte("old\n")),
	)
	writeMergedLog(t, base, 42, "20260615_1430_2_1.logbin",
		logconsumer.EncodeSplitRecord(mustTime("2026-06-15T14:30:00Z"), 2, 1, logconsumer.SplitStreamStdout, []byte("current\n")),
		logconsumer.EncodeSplitRecord(mustTime("2026-06-15T14:59:59Z"), 2, 1, logconsumer.SplitStreamStdout, []byte("late\n")),
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

func TestStreamLogsReadsDeploymentZeroMergedRecords(t *testing.T) {
	base := t.TempDir()
	old := ainit.StaticConfig.RunOutputDir
	ainit.StaticConfig.RunOutputDir = base
	t.Cleanup(func() { ainit.StaticConfig.RunOutputDir = old })

	writeMergedLog(t, base, 0, "20260615_1430_0_1.logbin",
		logconsumer.EncodeSplitRecord(mustTime("2026-06-15T14:30:02Z"), 0, 1, logconsumer.SplitStreamStdout, []byte("third\n")),
		logconsumer.EncodeSplitRecord(mustTime("2026-06-15T14:30:03Z"), 0, 1, logconsumer.SplitStreamStdout, []byte("fourth\n")),
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

func writeMergedLog(t *testing.T, base string, deploymentID int, name string, records ...[]byte) {
	t.Helper()
	dir := filepath.Join(base, intName(deploymentID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var data []byte
	for _, record := range records {
		data = append(data, record...)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func intName(v int) string {
	return strconv.Itoa(v)
}

func mustTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		panic(err)
	}
	return t
}
