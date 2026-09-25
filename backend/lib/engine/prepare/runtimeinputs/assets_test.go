package runtimeinputs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
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
	dir := withAssetCacheDir(t, "5@1", "5@2", "6@1", "7@3_x")

	removed, err := RetainAssets(map[apigen.ValueRef]struct{}{{ID: 5, Version: 2}: {}, {ID: 7, Version: 3}: {}})
	if err != nil {
		t.Fatalf("RetainAssets: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	for _, name := range []string{"5@2", "7@3_x"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("referenced asset %s was removed: %v", name, err)
		}
	}
	for _, name := range []string{"5@1", "6@1"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("unreferenced asset %s survived", name)
		}
	}
}

// Earlier layouts named files "<row id>" and "<old asset id>_<version>". Both
// must go even when their numbers coincide with a kept ref, or a stale file
// would be served as a cache hit.
func TestRetainAssetsRemovesEarlierLayoutEntries(t *testing.T) {
	dir := withAssetCacheDir(t, "5", "7_x", "5_1", "18_2_x")

	removed, err := RetainAssets(map[apigen.ValueRef]struct{}{{ID: 5, Version: 1}: {}, {ID: 18, Version: 2}: {}, {}: {}})
	if err != nil {
		t.Fatalf("RetainAssets: %v", err)
	}
	if removed != 4 {
		t.Fatalf("removed = %d, want 4", removed)
	}
	for _, name := range []string{"5", "7_x", "5_1", "18_2_x"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("earlier layout asset %s survived", name)
		}
	}
}

// EnsureAssetsReady stages downloads as "<name>.tmp" in the same directory. A
// sweep that collected those would delete an asset out from under an in-flight
// prepare, so anything that is not exactly a cache name is left alone.
func TestRetainAssetsIgnoresInFlightDownloadsAndForeignNames(t *testing.T) {
	dir := withAssetCacheDir(t, "6@1.tmp", "6@1_x.tmp", "notanid", "0", "6_0", "6@0", "@1", "6@x")

	removed, err := RetainAssets(map[apigen.ValueRef]struct{}{})
	if err != nil {
		t.Fatalf("RetainAssets: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0", removed)
	}
	for _, name := range []string{"6@1.tmp", "6@1_x.tmp", "notanid", "0", "6_0", "6@0", "@1", "6@x"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s was removed: %v", name, err)
		}
	}
}
