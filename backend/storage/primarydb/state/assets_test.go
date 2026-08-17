package state

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jptrs93/opsagent/backend/apigen"
)

func contentSha(blob []byte) string {
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])
}

// MustPutInlineAssetContent stores blob inline in the content store (if
// absent) and returns its sha. Test-only convenience.
func (s *Service) MustPutInlineAssetContent(blob []byte) string {
	sha := contentSha(blob)
	if _, ok := s.GetAssetStoreRowBySha(sha); !ok {
		s.InsertAssetStoreRow(uuid.Must(uuid.NewV7()).String(), sha, int64(len(blob)), blob, 0, 0)
	}
	return sha
}

// SetAssetByKey creates the asset in spaceID's root on first use and appends a
// version on each later call. Test-only convenience over the public write API.
func (s *Service) SetAssetByKey(key string, blob []byte, spaceIDs ...int32) *apigen.AssetVersion {
	spaceID := DefaultSpaceID
	if len(spaceIDs) > 0 {
		spaceID = normalizedUserSpaceID(spaceIDs[0])
	}
	sha := s.MustPutInlineAssetContent(blob)
	if existing, ok := s.GetAssetInRootByKey(spaceID, key); ok {
		v, err := s.AppendAssetVersion(int32(existing.ID), 0, sha, int64(len(blob)))
		if err != nil {
			panic(fmt.Sprintf("SetAssetByKey append: %v", err))
		}
		return v
	}
	v, err := s.CreateAssetWithVersion(key, spaceID, 0, 0, sha, int64(len(blob)))
	if err != nil {
		panic(fmt.Sprintf("SetAssetByKey create: %v", err))
	}
	return v
}

func TestAssetsAreVersionedAndImmutable(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))

	v1 := store.SetAssetByKey("nginx.conf", []byte("events {}\n"))
	if v1.ID == 0 || v1.AssetID == 0 || v1.Version != 1 || v1.SpaceID != DefaultSpaceID {
		t.Fatalf("first version = id %d asset %d version %d space %d, want nonzero ids version 1 space %d", v1.ID, v1.AssetID, v1.Version, v1.SpaceID, DefaultSpaceID)
	}
	if v1.Sha256 != contentSha([]byte("events {}\n")) {
		t.Fatalf("first version sha = %q, want content sha", v1.Sha256)
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
		meta.VersionRefs[0].Sha256 != v2.Sha256 ||
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

func TestAssetVersionsShareContentBySha(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	blob := []byte("shared content")

	a := store.SetAssetByKey("a.conf", blob)
	b := store.SetAssetByKey("b.conf", blob)
	if a.Sha256 != b.Sha256 {
		t.Fatalf("shas differ: %q vs %q", a.Sha256, b.Sha256)
	}
	if store.CountAssetVersionsBySha(a.Sha256) != 2 {
		t.Fatalf("versions by sha = %d, want 2", store.CountAssetVersionsBySha(a.Sha256))
	}
	if rows := store.ListAssetStoreRowMetas(); len(rows) != 1 {
		t.Fatalf("store rows = %d, want 1 shared row", len(rows))
	}
}

func TestAssetVersionRequiresStoredContent(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	if _, err := store.CreateAssetWithVersion("app.yaml", DefaultSpaceID, 0, 0, contentSha([]byte("missing")), 7); !errors.Is(err, ErrAssetContentMissing) {
		t.Fatalf("create without content err = %v, want ErrAssetContentMissing", err)
	}
	v := store.SetAssetByKey("app.yaml", []byte("x"))
	if _, err := store.AppendAssetVersion(v.AssetID, 0, contentSha([]byte("missing")), 7); !errors.Is(err, ErrAssetContentMissing) {
		t.Fatalf("append without content err = %v, want ErrAssetContentMissing", err)
	}
}

func TestRenameAssetPreservesVersions(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	v1 := store.SetAssetByKey("old-name", []byte("one"))
	largeSha := contentSha([]byte("large"))
	store.InsertAssetStoreRow("large-store", largeSha, 12_000_000, nil, 1, 0)
	v2, err := store.AppendAssetVersion(v1.AssetID, 0, largeSha, 12_000_000)
	if err != nil {
		t.Fatalf("append version: %v", err)
	}
	if v2.Location != "local://large-store" {
		t.Fatalf("large version location = %q, want local://large-store", v2.Location)
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

	versions := store.ListAssetVersions(v1.AssetID)
	if len(versions) != 2 {
		t.Fatalf("renamed versions = %d, want 2", len(versions))
	}
	want := []*apigen.AssetVersion{v1, v2}
	for i, got := range versions {
		if got.ID != want[i].ID || got.Key != "new-name" || got.SpaceID != want[i].SpaceID ||
			got.Version != want[i].Version || got.Location != want[i].Location ||
			got.SizeBytes != want[i].SizeBytes || got.Sha256 != want[i].Sha256 ||
			string(got.Blob) != string(want[i].Blob) ||
			!got.CreatedAt.Equal(want[i].CreatedAt) {
			t.Fatalf("renamed version %d = %+v, want original metadata %+v with new key", i+1, got, want[i])
		}
	}

	v3, err := store.AppendAssetVersion(v1.AssetID, 0, store.MustPutInlineAssetContent([]byte("three")), 5)
	if err != nil {
		t.Fatalf("append after rename: %v", err)
	}
	if v3.Version != 3 || v3.Key != "new-name" {
		t.Fatalf("version after rename = v%d key %q, want v3 new-name", v3.Version, v3.Key)
	}
}

func TestRenameAssetRejectsExistingKey(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
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
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	sha := store.MustPutInlineAssetContent([]byte("x"))
	if _, err := store.CreateAssetWithVersion("app.yaml", DefaultSpaceID, 0, 0, sha, 1); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.CreateAssetWithVersion("app.yaml", DefaultSpaceID, 0, 0, sha, 1); !errors.Is(err, ErrAssetAlreadyExists) {
		t.Fatalf("duplicate create error = %v, want %v", err, ErrAssetAlreadyExists)
	}
	// Same key in another space is a different file system.
	if _, err := store.CreateAssetWithVersion("app.yaml", 2, 0, 0, sha, 1); err != nil {
		t.Fatalf("create in second space: %v", err)
	}
	for _, key := range []string{"", ".", "..", "a/b", "a\\b", "a\x00b"} {
		if _, err := store.CreateAssetWithVersion(key, DefaultSpaceID, 0, 0, sha, 1); !errors.Is(err, ErrAssetKeyInvalid) {
			t.Fatalf("key %q error = %v, want %v", key, err, ErrAssetKeyInvalid)
		}
	}
}
