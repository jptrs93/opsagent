package logcollector

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/logconsumer"
)

func TestConsumeGreaterThanFollowsRotateMarker(t *testing.T) {
	oldRunOutputDir := ainit.StaticConfig.RunOutputDir
	defer func() { ainit.StaticConfig.RunOutputDir = oldRunOutputDir }()
	ainit.StaticConfig.RunOutputDir = t.TempDir()

	dir := filepath.Join(ainit.StaticConfig.RunOutputDir, "12", "34", "5")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 6, 15, 14, 0, 0, 1, time.UTC)
	second := time.Date(2026, 6, 15, 14, 30, 0, 2, time.UTC)
	writeSourceRecords(t, filepath.Join(dir, "stdout0.logbin"),
		logconsumer.EncodeSplitRecord(first, 34, 5, logconsumer.SplitStreamStdout, []byte("first\n")),
		logconsumer.EncodeSplitRotateMarker(),
	)
	writeSourceRecords(t, filepath.Join(dir, "stdout1.logbin"),
		logconsumer.EncodeSplitRecord(second, 34, 5, logconsumer.SplitStreamStdout, []byte("second\n")),
		logconsumer.EncodeSplitEndMarker(),
	)

	var got []string
	for line, err := range consumeGreaterThan(LogLine{}, 12, 34, 5) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(line.Line))
	}
	want := []string{"first\n", "second\n"}
	if len(got) != len(want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func writeSourceRecords(t *testing.T, path string, records ...[]byte) {
	t.Helper()
	var data []byte
	for _, record := range records {
		data = append(data, record...)
	}
	if err := os.WriteFile(path, data, 0o640); err != nil {
		t.Fatal(err)
	}
}
