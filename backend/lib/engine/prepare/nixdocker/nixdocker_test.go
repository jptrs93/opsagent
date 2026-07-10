package nixdocker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckedOutFlakePathRequiresLocalFile(t *testing.T) {
	repoDir := t.TempDir()
	flakePath := filepath.Join(repoDir, "nix", "app", "flake.nix")
	if err := os.MkdirAll(filepath.Dir(flakePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(flakePath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := checkedOutFlakePath(repoDir, "nix/app/flake.nix")
	if err != nil {
		t.Fatal(err)
	}
	if got != flakePath {
		t.Fatalf("path = %q, want %q", got, flakePath)
	}
}

func TestCheckedOutFlakePathRejectsEscapingPath(t *testing.T) {
	if _, err := checkedOutFlakePath(t.TempDir(), "../flake.nix"); err == nil {
		t.Fatal("expected escaping path error")
	}
}

func TestCheckedOutFlakePathRejectsMissingFile(t *testing.T) {
	if _, err := checkedOutFlakePath(t.TempDir(), "flake.nix"); err == nil {
		t.Fatal("expected missing file error")
	}
}
