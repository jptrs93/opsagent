package logconsumer

import (
	"os"
	"path/filepath"
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

func TestLogfmtWriterPassesThroughStructuredLines(t *testing.T) {
	base := filepath.Join(t.TempDir(), "12", "34", "1")
	if err := os.MkdirAll(base, 0o750); err != nil {
		t.Fatal(err)
	}
	w := newLogfmtWriter(&hourlyWriter{basePath: base})

	now := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	line := "time=2026-06-15T14:30:00Z level=INFO message=ready\n"
	if _, err := w.writeAt(now, []byte(line)); err != nil {
		t.Fatalf("write structured line: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	assertFileContent(t, filepath.Join(base, "20260615_14.logbin"), line)
}

func TestLogfmtWriterFlushesUnformattedBeforeStructuredLine(t *testing.T) {
	base := filepath.Join(t.TempDir(), "12", "34", "1")
	if err := os.MkdirAll(base, 0o750); err != nil {
		t.Fatal(err)
	}
	w := newLogfmtWriter(&hourlyWriter{basePath: base})
	w.flushDelay = time.Hour

	first := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	if _, err := w.writeAt(first, []byte("panic: bad\nstack \"line\"\n")); err != nil {
		t.Fatalf("write unformatted lines: %v", err)
	}
	structured := "time=2026-06-15T14:30:01Z level=INFO message=ready\n"
	if _, err := w.writeAt(first.Add(time.Second), []byte(structured)); err != nil {
		t.Fatalf("write structured line: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	want := "time=2026-06-15T14:30:00Z level=ERROR fmt=unformatted msg=\"panic: bad\\nstack \\\"line\\\"\"\n" + structured
	assertFileContent(t, filepath.Join(base, "20260615_14.logbin"), want)
}

func TestLogfmtWriterFlushesUnformattedAfterDelay(t *testing.T) {
	base := filepath.Join(t.TempDir(), "12", "34", "1")
	if err := os.MkdirAll(base, 0o750); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	w := newLogfmtWriter(&hourlyWriter{basePath: base})
	w.now = func() time.Time { return now }
	w.flushDelay = 5 * time.Millisecond

	if _, err := w.writeAt(now, []byte("not logfmt\n")); err != nil {
		t.Fatalf("write unformatted line: %v", err)
	}
	want := "time=2026-06-15T14:30:00Z level=ERROR fmt=unformatted msg=\"not logfmt\"\n"
	waitForFileContent(t, filepath.Join(base, "20260615_14.logbin"), want)
	if err := w.Close(); err != nil {
		t.Fatal(err)
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
