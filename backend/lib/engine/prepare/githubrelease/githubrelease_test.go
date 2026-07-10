package githubrelease

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jptrs93/opsagent/backend/lib/repo/github"
)

func TestPrepareReleaseDirCreatesUsablePath(t *testing.T) {
	releasesDir := filepath.Join(t.TempDir(), "releases")
	p := &Preparer{releasesDir: filepath.Clean(releasesDir)}
	dir, err := p.prepareReleaseDir("jptrs93/opsagent", "v1.2.3")
	if err != nil {
		t.Fatalf("prepareReleaseDir: %v", err)
	}
	want := filepath.Join(releasesDir, "jptrs93", "opsagent", "v1.2.3")
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

func TestPickAssetPrefersCurrentArchitecture(t *testing.T) {
	assets := []github.Asset{
		{Name: "opendeploy-linux-amd64"},
		{Name: "sha256sums.txt"},
		{Name: "opendeploy-linux-" + runtime.GOARCH},
	}

	asset := pickAsset(assets, "")
	if asset == nil {
		t.Fatal("asset is nil")
	}
	if got, want := asset.Name, "opendeploy-linux-"+runtime.GOARCH; got != want {
		t.Fatalf("asset = %q, want %q", got, want)
	}
}

func TestPickAssetUsesRequestedAsset(t *testing.T) {
	assets := []github.Asset{{Name: "first"}, {Name: "requested"}}

	asset := pickAsset(assets, "requested")
	if asset == nil {
		t.Fatal("asset is nil")
	}
	if got, want := asset.Name, "requested"; got != want {
		t.Fatalf("asset = %q, want %q", got, want)
	}
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
