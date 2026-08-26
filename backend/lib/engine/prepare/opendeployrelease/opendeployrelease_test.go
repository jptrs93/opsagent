package opendeployrelease

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
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

func TestFindAssetSelectsCurrentArchitecture(t *testing.T) {
	assets := []github.Asset{
		{Name: "opendeploy-linux-other"},
		{Name: internaldeploy.ReleaseAsset},
		{Name: "sha256sums.txt"},
	}

	asset := findAsset(assets)
	if asset == nil || asset.Name != internaldeploy.ReleaseAsset {
		t.Fatalf("findAsset() = %#v, want %q", asset, internaldeploy.ReleaseAsset)
	}
	if asset := findAsset([]github.Asset{{Name: "opendeploy-linux-other"}}); asset != nil {
		t.Fatalf("findAsset() = %#v, want nil", asset)
	}
}

func TestImageTagSanitizesVersion(t *testing.T) {
	if got, want := imageTag(" refs/tags/v1.2.3+build "), "refs-tags-v1.2.3-build"; got != want {
		t.Fatalf("imageTag() = %q, want %q", got, want)
	}
}

func TestOpendeployBinaryOCIIsDeterministic(t *testing.T) {
	first := buildOCI(t, "opendeploy-net:v1.2.3", []byte("binary contents"))
	second := buildOCI(t, "opendeploy-net:v1.2.3", []byte("binary contents"))
	if !bytes.Equal(first, second) {
		t.Fatal("OCI archives differ for identical inputs")
	}
}

func TestOpendeployImageLayerContainsExecutable(t *testing.T) {
	binary := []byte("binary contents")
	layer, err := opendeployImageLayer(binary)
	if err != nil {
		t.Fatal(err)
	}

	tr := tar.NewReader(bytes.NewReader(layer))
	header, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "opendeploy" || header.Mode != 0o755 || !header.ModTime.Equal(time.Unix(0, 0)) {
		t.Fatalf("layer header = {Name: %q, Mode: %o, ModTime: %s}", header.Name, header.Mode, header.ModTime)
	}
	got, err := io.ReadAll(tr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, binary) {
		t.Fatalf("binary = %q, want %q", got, binary)
	}
}

func buildOCI(t *testing.T, ref string, binary []byte) []byte {
	t.Helper()
	reader, err := opendeployBinaryOCI(ref, binary)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return archive
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
