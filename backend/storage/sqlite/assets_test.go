package sqlite

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestAssetsAreVersionedAndImmutable(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))

	v1 := store.SetAsset("nginx.conf", "nginx", []byte("events {}\n"))
	if v1.ID == 0 || v1.Version != 1 || v1.SpaceID != DefaultSpaceID {
		t.Fatalf("first asset = id %d version %d space %d, want nonzero id version 1 space %d", v1.ID, v1.Version, v1.SpaceID, DefaultSpaceID)
	}
	v2 := store.SetAsset("nginx.conf", "nginx", []byte("events {}\nhttp {}\n"))
	if v2.ID == 0 || v2.ID == v1.ID || v2.Version != 2 {
		t.Fatalf("second asset = id %d version %d, want new id version 2", v2.ID, v2.Version)
	}

	latest, ok := store.GetAsset("nginx.conf", 0)
	if !ok {
		t.Fatal("latest asset not found")
	}
	if latest.ID != v2.ID || latest.Version != 2 || latest.SpaceID != DefaultSpaceID || string(latest.Blob) != "events {}\nhttp {}\n" {
		t.Fatalf("latest asset = v%d %q", latest.Version, latest.Blob)
	}
	byID, ok := store.GetAssetByID(v2.ID)
	if !ok || byID.Key != "nginx.conf" || string(byID.Blob) != "events {}\nhttp {}\n" {
		t.Fatalf("asset by id = %+v ok=%v", byID, ok)
	}

	old, ok := store.GetAsset("nginx.conf", 1)
	if !ok {
		t.Fatal("version 1 asset not found")
	}
	if old.Version != 1 || string(old.Blob) != "events {}\n" {
		t.Fatalf("old asset = v%d %q", old.Version, old.Blob)
	}

	items := store.ListAssets()
	if len(items) != 1 {
		t.Fatalf("asset list length = %d, want 1", len(items))
	}
	if items[0].ID != v2.ID || items[0].Key != "nginx.conf" || items[0].SpaceID != DefaultSpaceID || items[0].Version != 2 || items[0].SizeBytes != int32(len("events {}\nhttp {}\n")) {
		t.Fatalf("asset meta = %+v", items[0])
	}
	allItems := store.ListAllAssetVersions()
	if len(allItems) != 2 || allItems[0].ID != v1.ID || allItems[1].ID != v2.ID {
		t.Fatalf("all asset versions = %+v", allItems)
	}

	store.DeleteAsset("nginx.conf")
	if _, ok := store.GetAsset("nginx.conf", 0); ok {
		t.Fatal("asset still found after delete")
	}
}

func TestRenameAssetPreservesVersions(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	v1 := store.SetAsset("old-name", "text", []byte("one"))
	v2 := store.SetAssetStored("old-name", "binary", "local://2", 12_000_000, []byte{})

	renamed, err := store.RenameAsset("old-name", "new-name")
	if err != nil {
		t.Fatalf("rename asset: %v", err)
	}
	if renamed.ID != v2.ID || renamed.Key != "new-name" || renamed.Version != 2 {
		t.Fatalf("renamed latest = %+v", renamed)
	}
	if _, ok := store.GetAsset("old-name", 0); ok {
		t.Fatal("old asset key still exists")
	}

	versions := store.ListAssetVersionsByKeyIncludingPending("new-name")
	if len(versions) != 2 {
		t.Fatalf("renamed versions = %d, want 2", len(versions))
	}
	want := []*apigen.Asset{v1, v2}
	for i, got := range versions {
		if got.ID != want[i].ID || got.Key != "new-name" || got.SpaceID != want[i].SpaceID ||
			got.Version != want[i].Version || got.Format != want[i].Format || got.Location != want[i].Location ||
			got.SizeBytes != want[i].SizeBytes || string(got.Blob) != string(want[i].Blob) ||
			!got.CreatedAt.Equal(want[i].CreatedAt) {
			t.Fatalf("renamed version %d = %+v, want original metadata %+v with new key", i+1, got, want[i])
		}
	}

	v3 := store.SetAsset("new-name", "text", []byte("three"))
	if v3.Version != 3 {
		t.Fatalf("version after rename = %d, want 3", v3.Version)
	}
}

func TestRenameAssetRejectsExistingKey(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	source := store.SetAsset("source", "text", []byte("source"))
	destination := store.SetAsset("destination", "text", []byte("destination"))

	if _, err := store.RenameAsset("source", "destination"); !errors.Is(err, ErrAssetAlreadyExists) {
		t.Fatalf("rename collision error = %v, want %v", err, ErrAssetAlreadyExists)
	}
	if got, ok := store.GetAsset("source", 0); !ok || got.ID != source.ID {
		t.Fatalf("source after collision = %+v, ok=%v", got, ok)
	}
	if got, ok := store.GetAsset("destination", 0); !ok || got.ID != destination.ID {
		t.Fatalf("destination after collision = %+v, ok=%v", got, ok)
	}
	if _, err := store.RenameAsset("missing", "unused"); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("missing source error = %v, want %v", err, ErrAssetNotFound)
	}
}
