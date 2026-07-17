package nixdocker

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestNixBuildArgs(t *testing.T) {
	base := []string{
		"--extra-experimental-features", "nix-command flakes",
		"build", "--no-update-lock-file", "--no-link", "--print-out-paths", "-L",
	}
	if got := nixBuildArgs(""); !reflect.DeepEqual(got, base) {
		t.Fatalf("default args = %q, want %q", got, base)
	}

	want := append(append([]string(nil), base...), ".#radkitRpaClientImage")
	if got := nixBuildArgs(".#radkitRpaClientImage"); !reflect.DeepEqual(got, want) {
		t.Fatalf("target args = %q, want %q", got, want)
	}
}

func TestFormatImageSize(t *testing.T) {
	tests := map[int64]string{
		0:                  "0 B",
		1023:               "1023 B",
		1024:               "1.0 KiB",
		5 * 1024 * 1024:    "5.0 MiB",
		1536 * 1024 * 1024: "1.5 GiB",
	}
	for size, want := range tests {
		if got := formatImageSize(size); got != want {
			t.Errorf("formatImageSize(%d) = %q, want %q", size, got, want)
		}
	}
}
