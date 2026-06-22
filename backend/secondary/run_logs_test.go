package secondary

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestLatestRunLogFilePrefersFlatLogs(t *testing.T) {
	old := ainit.StaticConfig.RunOutputDir
	ainit.StaticConfig.RunOutputDir = t.TempDir()
	t.Cleanup(func() { ainit.StaticConfig.RunOutputDir = old })

	dir := apigen.RunOutputDeploymentDir(14)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	olderFlat := filepath.Join(dir, "20260622_0400_45_1.logbin")
	newerFlat := filepath.Join(dir, "20260622_0430_45_2.logbin")
	legacy := filepath.Join(apigen.RunOutputRunDir(14, 45, 3), "stdout0.logbin")
	writeLogFile(t, olderFlat, time.Unix(100, 0))
	writeLogFile(t, newerFlat, time.Unix(200, 0))
	writeLogFile(t, legacy, time.Unix(300, 0))

	got, err := latestRunLogFile(14, 45)
	if err != nil {
		t.Fatal(err)
	}
	if got != newerFlat {
		t.Fatalf("latestRunLogFile = %q, want %q", got, newerFlat)
	}
}

func TestLatestRunLogFileFallsBackToLegacyNestedLogs(t *testing.T) {
	old := ainit.StaticConfig.RunOutputDir
	ainit.StaticConfig.RunOutputDir = t.TempDir()
	t.Cleanup(func() { ainit.StaticConfig.RunOutputDir = old })

	legacy := filepath.Join(apigen.RunOutputRunDir(14, 45, 3), "stdout0.logbin")
	writeLogFile(t, legacy, time.Unix(100, 0))

	got, err := latestRunLogFile(14, 45)
	if err != nil {
		t.Fatal(err)
	}
	if got != legacy {
		t.Fatalf("latestRunLogFile = %q, want %q", got, legacy)
	}
}

func writeLogFile(t *testing.T, path string, modTime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("log"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}
