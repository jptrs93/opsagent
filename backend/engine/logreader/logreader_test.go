package logreader

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
)

func TestParseLogfmtLine(t *testing.T) {
	line, err := ParseLogfmtLine(`time=2026-06-15T14:30:00.123Z level=ERROR fmt=unformatted message="bad \"thing\"" service=api`)
	if err != nil {
		t.Fatal(err)
	}
	if line.Level != "ERROR" || line.Msg != `bad "thing"` {
		t.Fatalf("unexpected line: %#v", line)
	}
	if got := line.Props["fmt"]; got != "unformatted" {
		t.Fatalf("fmt prop = %q", got)
	}
	if got := line.Props["service"]; got != "api" {
		t.Fatalf("service prop = %q", got)
	}
}

func TestStreamLogsMergesRunDirsByTimestamp(t *testing.T) {
	base := t.TempDir()
	old := ainit.StaticConfig.RunOutputDir
	ainit.StaticConfig.RunOutputDir = base
	t.Cleanup(func() { ainit.StaticConfig.RunOutputDir = old })

	writeLog(t, base, 42, 1, 1, "20260615_14.logbin", "time=2026-06-15T14:30:02Z level=INFO msg=third run=1\ntime=2026-06-15T14:30:03Z level=INFO msg=fourth run=1\n")
	writeLog(t, base, 42, 1, 2, "20260615_14.logbin", "time=2026-06-15T14:30:01Z level=INFO msg=second run=2\n")
	writeLog(t, base, 42, 2, 1, "20260615_14.logbin", "time=2026-06-15T14:30:00Z level=INFO msg=first run=3\n")

	since := mustTime("2026-06-15T14:29:00Z")
	var got []string
	for line, err := range StreamLogs(42, 0, since, nil) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, line.Msg)
	}
	want := []string{"fourth", "third", "second", "first"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("messages = %#v, want %#v", got, want)
	}
}

func TestStreamLogsFiltersTimeRange(t *testing.T) {
	base := t.TempDir()
	old := ainit.StaticConfig.RunOutputDir
	ainit.StaticConfig.RunOutputDir = base
	t.Cleanup(func() { ainit.StaticConfig.RunOutputDir = old })

	writeLog(t, base, 42, 1, 1, "20260615_13.logbin", "time=2026-06-15T13:59:59Z level=INFO msg=before\n")
	writeLog(t, base, 42, 1, 1, "20260615_14.logbin", "time=2026-06-15T14:00:00Z level=INFO msg=inside\n")
	writeLog(t, base, 42, 1, 1, "20260615_15.logbin", "time=2026-06-15T15:00:00Z level=INFO msg=after\n")

	since := mustTime("2026-06-15T14:00:00Z")
	till := mustTime("2026-06-15T15:00:00Z")
	var got []string
	for line, err := range StreamLogs(42, 0, since, &till) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, line.Msg)
	}
	want := []string{"inside"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("messages = %#v, want %#v", got, want)
	}
}

func TestStreamLogsFiltersConfigVersion(t *testing.T) {
	base := t.TempDir()
	old := ainit.StaticConfig.RunOutputDir
	ainit.StaticConfig.RunOutputDir = base
	t.Cleanup(func() { ainit.StaticConfig.RunOutputDir = old })

	writeLog(t, base, 42, 1, 1, "20260615_14.logbin", "time=2026-06-15T14:30:00Z level=INFO msg=old\n")
	writeLog(t, base, 42, 2, 1, "20260615_14.logbin", "time=2026-06-15T14:30:01Z level=INFO msg=current\n")

	since := mustTime("2026-06-15T14:00:00Z")
	var got []string
	for line, err := range StreamLogs(42, 2, since, nil) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, line.Msg)
	}
	want := []string{"current"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("messages = %#v, want %#v", got, want)
	}
}

func writeLog(t *testing.T, base string, deploymentID, version, run int, name string, content string) {
	t.Helper()
	dir := filepath.Join(base, intName(deploymentID), intName(version), intName(run))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
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
