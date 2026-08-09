package sqlite

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestAssetsAreVersionedAndImmutable(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))

	v1 := store.SetAssetByKey("nginx.conf", []byte("events {}\n"))
	if v1.ID == 0 || v1.AssetID == 0 || v1.Version != 1 || v1.SpaceID != DefaultSpaceID {
		t.Fatalf("first version = id %d asset %d version %d space %d, want nonzero ids version 1 space %d", v1.ID, v1.AssetID, v1.Version, v1.SpaceID, DefaultSpaceID)
	}
	v2 := store.SetAssetByKey("nginx.conf", []byte("events {}\nhttp {}\n"))
	if v2.ID == 0 || v2.ID == v1.ID || v2.Version != 2 || v2.AssetID != v1.AssetID {
		t.Fatalf("second version = id %d asset %d version %d, want new id, same asset, version 2", v2.ID, v2.AssetID, v2.Version)
	}

	latest, ok := store.GetAssetVersion(v1.AssetID, 0)
	if !ok {
		t.Fatal("latest version not found")
	}
	if latest.ID != v2.ID || latest.Version != 2 || latest.SpaceID != DefaultSpaceID || string(latest.Blob) != "events {}\nhttp {}\n" {
		t.Fatalf("latest version = v%d %q", latest.Version, latest.Blob)
	}
	byVersionID, ok := store.GetAssetVersionByID(v2.ID)
	if !ok || byVersionID.Key != "nginx.conf" || byVersionID.AssetID != v1.AssetID || string(byVersionID.Blob) != "events {}\nhttp {}\n" {
		t.Fatalf("version by id = %+v ok=%v", byVersionID, ok)
	}

	old, ok := store.GetAssetVersion(v1.AssetID, 1)
	if !ok {
		t.Fatal("version 1 not found")
	}
	if old.Version != 1 || string(old.Blob) != "events {}\n" {
		t.Fatalf("old version = v%d %q", old.Version, old.Blob)
	}

	items := store.ListAssets()
	if len(items) != 1 {
		t.Fatalf("asset list length = %d, want 1", len(items))
	}
	meta := items[0]
	if meta.ID != v1.AssetID || meta.Key != "nginx.conf" || meta.SpaceID != DefaultSpaceID {
		t.Fatalf("asset meta = %+v", meta)
	}
	// version_refs are newest first: [0] is the latest.
	if len(meta.VersionRefs) != 2 ||
		meta.VersionRefs[0].ID != v2.ID || meta.VersionRefs[0].Version != 2 ||
		meta.VersionRefs[0].SizeBytes != int32(len("events {}\nhttp {}\n")) ||
		!meta.VersionRefs[0].CreatedAt.Equal(v2.CreatedAt) ||
		meta.VersionRefs[1].ID != v1.ID || meta.VersionRefs[1].Version != 1 {
		t.Fatalf("asset version refs = %+v", meta.VersionRefs)
	}
	allItems := store.ListAllAssetVersions()
	if len(allItems) != 2 || allItems[0].ID != v1.ID || allItems[1].ID != v2.ID {
		t.Fatalf("all asset versions = %+v", allItems)
	}

	store.DeleteAsset(v1.AssetID)
	if _, ok := store.GetAssetVersion(v1.AssetID, 0); ok {
		t.Fatal("asset still found after delete")
	}
	if _, ok := store.GetAssetRow(v1.AssetID); ok {
		t.Fatal("asset row still found after delete")
	}
}

func TestRenameAssetPreservesVersions(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	v1 := store.SetAssetByKey("old-name", []byte("one"))
	v2, err := store.AppendAssetVersion(v1.AssetID, 0, "local://2", 12_000_000, []byte{})
	if err != nil {
		t.Fatalf("append version: %v", err)
	}

	renamed, err := store.RenameAssetKey(v1.AssetID, "new-name")
	if err != nil {
		t.Fatalf("rename asset: %v", err)
	}
	if renamed.ID != v1.AssetID || renamed.Key != "new-name" ||
		len(renamed.VersionRefs) != 2 || renamed.VersionRefs[0].ID != v2.ID || renamed.VersionRefs[0].Version != 2 {
		t.Fatalf("renamed meta = %+v", renamed)
	}
	if _, ok := store.GetAssetInRootByKey(DefaultSpaceID, "old-name"); ok {
		t.Fatal("old asset key still exists")
	}

	versions := store.ListAssetVersionsIncludingPending(v1.AssetID)
	if len(versions) != 2 {
		t.Fatalf("renamed versions = %d, want 2", len(versions))
	}
	want := []*apigen.AssetVersion{v1, v2}
	for i, got := range versions {
		if got.ID != want[i].ID || got.Key != "new-name" || got.SpaceID != want[i].SpaceID ||
			got.Version != want[i].Version || got.Location != want[i].Location ||
			got.SizeBytes != want[i].SizeBytes || string(got.Blob) != string(want[i].Blob) ||
			!got.CreatedAt.Equal(want[i].CreatedAt) {
			t.Fatalf("renamed version %d = %+v, want original metadata %+v with new key", i+1, got, want[i])
		}
	}

	v3, err := store.AppendAssetVersion(v1.AssetID, 0, "", 5, []byte("three"))
	if err != nil {
		t.Fatalf("append after rename: %v", err)
	}
	if v3.Version != 3 || v3.Key != "new-name" {
		t.Fatalf("version after rename = v%d key %q, want v3 new-name", v3.Version, v3.Key)
	}
}

func TestRenameAssetRejectsExistingKey(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	source := store.SetAssetByKey("source", []byte("source"))
	destination := store.SetAssetByKey("destination", []byte("destination"))

	if _, err := store.RenameAssetKey(source.AssetID, "destination"); !errors.Is(err, ErrAssetAlreadyExists) {
		t.Fatalf("rename collision error = %v, want %v", err, ErrAssetAlreadyExists)
	}
	if got, ok := store.GetAssetInRootByKey(DefaultSpaceID, "source"); !ok || int32(got.ID) != source.AssetID {
		t.Fatalf("source after collision = %+v, ok=%v", got, ok)
	}
	if got, ok := store.GetAssetInRootByKey(DefaultSpaceID, "destination"); !ok || int32(got.ID) != destination.AssetID {
		t.Fatalf("destination after collision = %+v, ok=%v", got, ok)
	}
	if _, err := store.RenameAssetKey(999_999, "unused"); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("missing source error = %v, want %v", err, ErrAssetNotFound)
	}
	if _, err := store.RenameAssetKey(source.AssetID, "bad/name"); !errors.Is(err, ErrAssetKeyInvalid) {
		t.Fatalf("invalid key error = %v, want %v", err, ErrAssetKeyInvalid)
	}
}

func TestCreateAssetRejectsDuplicateAndInvalidKeys(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	if _, err := store.CreateAssetWithVersion("app.yaml", DefaultSpaceID, 0, "", 1, []byte("x")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.CreateAssetWithVersion("app.yaml", DefaultSpaceID, 0, "", 1, []byte("x")); !errors.Is(err, ErrAssetAlreadyExists) {
		t.Fatalf("duplicate create error = %v, want %v", err, ErrAssetAlreadyExists)
	}
	// Same key in another space is a different file system.
	if _, err := store.CreateAssetWithVersion("app.yaml", 2, 0, "", 1, []byte("x")); err != nil {
		t.Fatalf("create in second space: %v", err)
	}
	for _, key := range []string{"", ".", "..", "a/b", "a\\b", "a\x00b"} {
		if _, err := store.CreateAssetWithVersion(key, DefaultSpaceID, 0, "", 1, []byte("x")); !errors.Is(err, ErrAssetKeyInvalid) {
			t.Fatalf("key %q error = %v, want %v", key, err, ErrAssetKeyInvalid)
		}
	}
}
