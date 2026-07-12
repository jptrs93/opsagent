package githubreleaseimage

import (
	"archive/tar"
	"bytes"
	"io"
	"runtime"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/lib/repo/github"
)

func TestPickAssetSelectsCurrentArchitecture(t *testing.T) {
	want := "opendeploy-linux-" + runtime.GOARCH
	assets := []github.Asset{
		{Name: "opendeploy-linux-other"},
		{Name: want},
		{Name: "sha256sums.txt"},
	}

	asset := pickAsset(assets)
	if asset == nil || asset.Name != want {
		t.Fatalf("pickAsset() = %#v, want %q", asset, want)
	}
	if missingAsset := pickAsset([]github.Asset{{Name: "opendeploy-linux-other"}}); missingAsset != nil {
		t.Fatalf("pickAsset() = %#v, want nil", missingAsset)
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
