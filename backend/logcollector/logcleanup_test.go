package logcollector

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/ainit"
)

func TestDeleteLegacyRunLogDirsOnceRemovesSubdirsAndKeepsFlatLogs(t *testing.T) {
	old := ainit.StaticConfig.RunOutputDir
	ainit.StaticConfig.RunOutputDir = t.TempDir()
	t.Cleanup(func() { ainit.StaticConfig.RunOutputDir = old })

	flat := filepath.Join(ainit.StaticConfig.RunOutputDir, "14", "20260622_0430_45_2.logbin")
	legacy := filepath.Join(ainit.StaticConfig.RunOutputDir, "14", "45", "2", "stdout0.logbin")
	legacySystem := filepath.Join(ainit.StaticConfig.RunOutputDir, "0", "v0.0.54", "opendeploy", "20260622_05.logbin")
	writeCleanupTestFile(t, flat)
	writeCleanupTestFile(t, legacy)
	writeCleanupTestFile(t, legacySystem)

	if err := DeleteLegacyRunLogDirsOnce(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(flat); err != nil {
		t.Fatalf("flat log was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(filepath.Dir(legacy))); !os.IsNotExist(err) {
		t.Fatalf("legacy deployment version dir still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(filepath.Dir(legacySystem))); !os.IsNotExist(err) {
		t.Fatalf("legacy system log version dir still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ainit.StaticConfig.RunOutputDir, legacyRunLogDirCleanupMarkerName)); err != nil {
		t.Fatalf("cleanup marker missing: %v", err)
	}
}

func writeCleanupTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("log"), 0o640); err != nil {
		t.Fatal(err)
	}
}
