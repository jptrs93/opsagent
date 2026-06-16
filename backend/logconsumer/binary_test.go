package logconsumer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHourlyWriterRotatesByUTCHour(t *testing.T) {
	base := filepath.Join(t.TempDir(), "12", "34", "1")
	if err := os.MkdirAll(base, 0o750); err != nil {
		t.Fatal(err)
	}
	w := &hourlyWriter{basePath: base}
	defer w.Close()

	first := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	if _, err := w.writeAt(first, []byte("first\n")); err != nil {
		t.Fatalf("write first bucket: %v", err)
	}

	second := first.Add(time.Hour)
	if _, err := w.writeAt(second, []byte("second\n")); err != nil {
		t.Fatalf("write second bucket: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	assertFileContent(t, filepath.Join(base, "20260615_14.logbin"), "first\n")
	assertFileContent(t, filepath.Join(base, "20260615_15.logbin"), "second\n")
}

func TestHourlyWriterDoesNotSplitLineAcrossBuckets(t *testing.T) {
	base := filepath.Join(t.TempDir(), "12", "34", "1")
	if err := os.MkdirAll(base, 0o750); err != nil {
		t.Fatal(err)
	}
	w := &hourlyWriter{basePath: base}
	defer w.Close()

	first := time.Date(2026, 6, 15, 14, 59, 59, 0, time.UTC)
	if _, err := w.writeAt(first, []byte("line starts")); err != nil {
		t.Fatalf("write first part: %v", err)
	}
	second := first.Add(time.Second)
	if _, err := w.writeAt(second, []byte(" and ends\nnext line\n")); err != nil {
		t.Fatalf("write second part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	assertFileContent(t, filepath.Join(base, "20260615_14.logbin"), "line starts and ends\n")
	assertFileContent(t, filepath.Join(base, "20260615_15.logbin"), "next line\n")
}

func TestHourlyWriterRequiresExistingBaseDir(t *testing.T) {
	base := filepath.Join(t.TempDir(), "12", "34", "1")
	w := &hourlyWriter{basePath: base}
	if _, err := w.writeAt(time.Now().UTC(), []byte("line\n")); err == nil {
		t.Fatal("expected missing base dir error")
	}
}

func TestProcessLinesPassesThroughStructuredLines(t *testing.T) {
	now := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	line := "time=2026-06-15T14:30:00Z level=INFO message=ready\n"
	lines := processLinesForTest(t, line, now)

	if len(lines) != 1 {
		t.Fatalf("len(lines) = %d, want 1", len(lines))
	}
	if string(lines[0]) != line {
		t.Fatalf("line = %q, want %q", string(lines[0]), line)
	}
}

func TestProcessLinesWritesEachUnformattedLineSeparately(t *testing.T) {
	first := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	lines := processLinesForTest(t, "panic: bad\n\nstack \"line\"\n", first)

	want := []string{
		"time=2026-06-15T14:30:00Z level=ERROR fmt=unformatted msg=\"panic: bad\"\n",
		"time=2026-06-15T14:30:00Z level=ERROR fmt=unformatted msg=\"\"\n",
		"time=2026-06-15T14:30:00Z level=ERROR fmt=unformatted msg=\"stack \\\"line\\\"\"\n",
	}
	if len(lines) != len(want) {
		t.Fatalf("len(lines) = %d, want %d", len(lines), len(want))
	}
	for i := range want {
		if string(lines[i]) != want[i] {
			t.Fatalf("line %d = %q, want %q", i, string(lines[i]), want[i])
		}
	}
}

func TestProcessLinesFlushesUnformattedBeforeStructuredLine(t *testing.T) {
	first := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	structured := "time=2026-06-15T14:30:01Z level=INFO message=ready\n"
	lines := processLinesForTest(t, "panic: bad\n"+structured, first)

	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2", len(lines))
	}
	if got, want := string(lines[0]), "time=2026-06-15T14:30:00Z level=ERROR fmt=unformatted msg=\"panic: bad\"\n"; got != want {
		t.Fatalf("first line = %q, want %q", got, want)
	}
	if got := string(lines[1]); got != structured {
		t.Fatalf("second line = %q, want %q", got, structured)
	}
}

func TestProcessLinesUsesErrorLevelForUnformattedLines(t *testing.T) {
	now := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	lines := processLinesForTest(t, "not logfmt\n", now)

	want := "time=2026-06-15T14:30:00Z level=ERROR fmt=unformatted msg=\"not logfmt\"\n"
	if len(lines) != 1 || string(lines[0]) != want {
		t.Fatalf("lines = %#v, want one line %q", lines, want)
	}
}

func TestProcessLinesUsesErrorLevelForStdoutUnformattedLines(t *testing.T) {
	now := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	lines := processLinesForTest(t, "stdout line\n", now)

	want := "time=2026-06-15T14:30:00Z level=ERROR fmt=unformatted msg=\"stdout line\"\n"
	if len(lines) != 1 || string(lines[0]) != want {
		t.Fatalf("lines = %#v, want one line %q", lines, want)
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

func processLinesForTest(t *testing.T, input string, now time.Time) [][]byte {
	t.Helper()
	out := make(chan []byte, 10)
	if err := processLinesWithClock(strings.NewReader(input), out, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	close(out)
	var lines [][]byte
	for line := range out {
		lines = append(lines, line)
	}
	return lines
}

func assertFileContent(t *testing.T, path string, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, string(got), want)
	}
}

func waitForFileContent(t *testing.T, path string, want string) {
	t.Helper()
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		got, err := os.ReadFile(path)
		if err == nil && string(got) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	assertFileContent(t, path, want)
}
