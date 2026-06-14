package preparer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/ainit"
)

func TestPrepareReleaseDirCreatesUsablePath(t *testing.T) {
	prev := ainit.StaticConfig
	ainit.StaticConfig.ReleasesDir = filepath.Join(t.TempDir(), "releases")
	defer func() { ainit.StaticConfig = prev }()

	g := &GithubReleaseDownloader{}
	dir, err := g.prepareReleaseDir("jptrs93/opsagent", "v1.2.3")
	if err != nil {
		t.Fatalf("prepareReleaseDir: %v", err)
	}
	want := filepath.Join(ainit.StaticConfig.ReleasesDir, "jptrs93", "opsagent", "v1.2.3")
	if dir != want {
		t.Fatalf("dir = %q, want %q", dir, want)
	}
	assertMode(t, dir, 0o755)
	assertMode(t, filepath.Dir(dir), 0o755)
}

func TestEnsureReleaseDirModeCorrectsRestrictiveDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "release")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := ensureReleaseDirMode(dir); err != nil {
		t.Fatalf("ensureReleaseDirMode: %v", err)
	}
	assertMode(t, dir, 0o755)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %o, want %o", path, got, want)
	}
}
