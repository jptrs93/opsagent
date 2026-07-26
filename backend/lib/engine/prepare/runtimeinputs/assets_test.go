package runtimeinputs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/ainit"
)

func withAssetCacheDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	previous := ainit.StaticConfig.AssetCacheDir
	ainit.StaticConfig.AssetCacheDir = dir
	t.Cleanup(func() { ainit.StaticConfig.AssetCacheDir = previous })
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("seeding %s: %v", name, err)
		}
	}
	return dir
}

func TestRetainAssetsRemovesOnlyUnreferencedCacheEntries(t *testing.T) {
	dir := withAssetCacheDir(t, "5", "6", "7_x")

	removed, err := RetainAssets(map[int32]struct{}{5: {}, 7: {}})
	if err != nil {
		t.Fatalf("RetainAssets: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	for _, name := range []string{"5", "7_x"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("referenced asset %s was removed: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "6")); !os.IsNotExist(err) {
		t.Fatal("unreferenced asset 6 survived")
	}
}

// EnsureAssetsReady stages downloads as "<name>.tmp" in the same directory. A
// sweep that collected those would delete an asset out from under an in-flight
// prepare, so anything that is not exactly a cache name is left alone.
func TestRetainAssetsIgnoresInFlightDownloadsAndForeignNames(t *testing.T) {
	dir := withAssetCacheDir(t, "6.tmp", "6_x.tmp", "notanid", "0")

	removed, err := RetainAssets(map[int32]struct{}{})
	if err != nil {
		t.Fatalf("RetainAssets: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0", removed)
	}
	for _, name := range []string{"6.tmp", "6_x.tmp", "notanid", "0"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s was removed: %v", name, err)
		}
	}
}
