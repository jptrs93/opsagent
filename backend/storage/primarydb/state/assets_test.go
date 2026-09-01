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
func (s *Service) SetAssetByKey(key string, blob []byte, spaceIDs ...int32) *apigen.Asset {
	spaceID := DefaultSpaceID
	if len(spaceIDs) > 0 {
		spaceID = normalizedUserSpaceID(spaceIDs[0])
	}
	sha := s.MustPutInlineAssetContent(blob)
	if existing, ok := getAssetInRootByKey(s, spaceID, key); ok {
		a, err := s.AppendAssetVersion(int32(existing.ID), 0, sha, int64(len(blob)))
		if err != nil {
			panic(fmt.Sprintf("SetAssetByKey append: %v", err))
		}
		return a
	}
	a, err := s.CreateAssetWithVersion(key, spaceID, 0, 0, sha, int64(len(blob)))
	if err != nil {
		panic(fmt.Sprintf("SetAssetByKey create: %v", err))
	}
	return a
}

func TestAssetsAreVersionedAndImmutable(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))

	a1 := store.SetAssetByKey("nginx.conf", []byte("events {}\n"))
	v1 := a1.LatestContentVersion()
	if a1.ID == 0 || v1 == nil || v1.ID == 0 || v1.Version != 1 || a1.SpaceID() != DefaultSpaceID {
		t.Fatalf("first write = asset %d latest %+v space %d, want nonzero ids version 1 space %d", a1.ID, v1, a1.SpaceID(), DefaultSpaceID)
	}
	if v1.Sha256 != contentSha([]byte("events {}\n")) {
		t.Fatalf("first version sha = %q, want content sha", v1.Sha256)
	}
	a2 := store.SetAssetByKey("nginx.conf", []byte("events {}\nhttp {}\n"))
	v2 := a2.LatestContentVersion()
	if v2.ID == 0 || v2.ID == v1.ID || v2.Version != 2 || a2.ID != a1.ID {
		t.Fatalf("second version = id %d asset %d version %d, want new id, same asset, version 2", v2.ID, a2.ID, v2.Version)
	}

	latest, ok := store.GetAsset(a1.ID)
	if !ok {
		t.Fatal("asset not found")
	}
	if lv := latest.LatestContentVersion(); lv.ID != v2.ID || lv.Version != 2 || latest.SpaceID() != DefaultSpaceID {
		t.Fatalf("latest version = %+v space %d", lv, latest.SpaceID())
	}
	if joined, ok := store.GetAssetVersionJoined(v2.ID); !ok || string(joined.Store.InlineBlob) != "events {}\nhttp {}\n" {
		t.Fatalf("latest blob = %q ok=%v", joined.Store.InlineBlob, ok)
	}
	ref, ok := store.GetAssetVersionRef(v2.ID)
	if !ok || ref.Key != "nginx.conf" || ref.AssetID != a1.ID || ref.SpaceID != DefaultSpaceID || ref.VersionID != v2.ID {
		t.Fatalf("version ref by id = %+v ok=%v", ref, ok)
	}

	// The old version is immutable: still listed and its content still resolves.
	if old := latest.ContentVersions[1]; old.ID != v1.ID || old.Version != 1 {
		t.Fatalf("old version = %+v", old)
	}
	if joined, ok := store.GetAssetVersionJoined(v1.ID); !ok || string(joined.Store.InlineBlob) != "events {}\n" {
		t.Fatalf("old blob = %q ok=%v", joined.Store.InlineBlob, ok)
	}

	items := store.ListAssets()
	if len(items) != 1 {
		t.Fatalf("asset list length = %d, want 1", len(items))
	}
	asset := items[0]
	if asset.ID != a1.ID || asset.Fs.Key != "nginx.conf" || asset.SpaceID() != DefaultSpaceID {
		t.Fatalf("asset = %+v", asset)
	}
	// content_versions are newest first: [0] is the latest.
	if len(asset.ContentVersions) != 2 ||
		asset.ContentVersions[0].ID != v2.ID || asset.ContentVersions[0].Version != 2 ||
		asset.ContentVersions[0].SizeBytes != int64(len("events {}\nhttp {}\n")) ||
		asset.ContentVersions[0].Sha256 != v2.Sha256 ||
		!asset.ContentVersions[0].CreatedAt.Equal(v2.CreatedAt) ||
		asset.ContentVersions[1].ID != v1.ID || asset.ContentVersions[1].Version != 1 {
		t.Fatalf("asset content versions = %+v", asset.ContentVersions)
	}
	allRows := store.ListAssetVersionsJoinedOfAsset(a1.ID)
	if len(allRows) != 2 || int32(allRows[0].Version.ID) != v1.ID || int32(allRows[1].Version.ID) != v2.ID {
		t.Fatalf("all asset versions = %+v", allRows)
	}

	store.DeleteAssetLocked(a1.ID)
	if _, ok := store.GetAsset(a1.ID); ok {
		t.Fatal("asset still found after delete")
	}
	if _, ok := store.GetAssetRow(a1.ID); ok {
		t.Fatal("asset row still found after delete")
	}
}

func TestAssetVersionsShareContentBySha(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	blob := []byte("shared content")

	a := store.SetAssetByKey("a.conf", blob)
	b := store.SetAssetByKey("b.conf", blob)
	aSha := a.LatestContentVersion().Sha256
	if aSha != b.LatestContentVersion().Sha256 {
		t.Fatalf("shas differ: %q vs %q", aSha, b.LatestContentVersion().Sha256)
	}
	if store.CountAssetVersionsBySha(aSha) != 2 {
		t.Fatalf("versions by sha = %d, want 2", store.CountAssetVersionsBySha(aSha))
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
	a := store.SetAssetByKey("app.yaml", []byte("x"))
	if _, err := store.AppendAssetVersion(a.ID, 0, contentSha([]byte("missing")), 7); !errors.Is(err, ErrAssetContentMissing) {
		t.Fatalf("append without content err = %v, want ErrAssetContentMissing", err)
	}
}

func TestRenameAssetPreservesVersions(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	a := store.SetAssetByKey("old-name", []byte("one"))
	v1 := a.LatestContentVersion()
	largeSha := contentSha([]byte("large"))
	store.InsertAssetStoreRow("large-store", largeSha, 12_000_000, nil, 1, 0)
	appended, err := store.AppendAssetVersion(a.ID, 0, largeSha, 12_000_000)
	if err != nil {
		t.Fatalf("append version: %v", err)
	}
	v2 := appended.LatestContentVersion()
	if v2.Version != 2 || v2.Sha256 != largeSha || v2.SizeBytes != 12_000_000 {
		t.Fatalf("large version = %+v, want version 2 of 12MB with the store sha", v2)
	}

	renamed, err := store.RenameAssetKey(a.ID, "new-name")
	if err != nil {
		t.Fatalf("rename asset: %v", err)
	}
	if renamed.ID != a.ID || renamed.Fs.Key != "new-name" ||
		len(renamed.ContentVersions) != 2 || renamed.ContentVersions[0].ID != v2.ID || renamed.ContentVersions[0].Version != 2 {
		t.Fatalf("renamed asset = %+v", renamed)
	}
	if _, ok := getAssetInRootByKey(store, DefaultSpaceID, "old-name"); ok {
		t.Fatal("old asset key still exists")
	}

	// Version rows are untouched by the rename: ids, metadata, and content all
	// survive; only the key changed.
	want := []*apigen.AssetContentVersion{v2, v1} // newest first
	for i, got := range renamed.ContentVersions {
		if got.ID != want[i].ID || got.Version != want[i].Version ||
			got.SizeBytes != want[i].SizeBytes || got.Sha256 != want[i].Sha256 ||
			!got.CreatedAt.Equal(want[i].CreatedAt) {
			t.Fatalf("renamed version %d = %+v, want original metadata %+v", i, got, want[i])
		}
		ref, ok := store.GetAssetVersionRef(got.ID)
		if !ok || ref.Key != "new-name" || ref.AssetID != renamed.ID || ref.SpaceID != DefaultSpaceID {
			t.Fatalf("version ref %d = %+v ok=%v, want the new key", i, ref, ok)
		}
	}
	if joined, ok := store.GetAssetVersionJoined(v1.ID); !ok || string(joined.Store.InlineBlob) != "one" {
		t.Fatalf("old blob after rename = %q ok=%v", joined.Store.InlineBlob, ok)
	}

	after, err := store.AppendAssetVersion(a.ID, 0, store.MustPutInlineAssetContent([]byte("three")), 5)
	if err != nil {
		t.Fatalf("append after rename: %v", err)
	}
	if after.LatestContentVersion().Version != 3 || after.Fs.Key != "new-name" {
		t.Fatalf("version after rename = v%d key %q, want v3 new-name", after.LatestContentVersion().Version, after.Fs.Key)
	}
}

func TestRenameAssetRejectsExistingKey(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	source := store.SetAssetByKey("source", []byte("source"))
	destination := store.SetAssetByKey("destination", []byte("destination"))

	if _, err := store.RenameAssetKey(source.ID, "destination"); !errors.Is(err, ErrAssetAlreadyExists) {
		t.Fatalf("rename collision error = %v, want %v", err, ErrAssetAlreadyExists)
	}
	if got, ok := getAssetInRootByKey(store, DefaultSpaceID, "source"); !ok || int32(got.ID) != source.ID {
		t.Fatalf("source after collision = %+v, ok=%v", got, ok)
	}
	if got, ok := getAssetInRootByKey(store, DefaultSpaceID, "destination"); !ok || int32(got.ID) != destination.ID {
		t.Fatalf("destination after collision = %+v, ok=%v", got, ok)
	}
	if _, err := store.RenameAssetKey(999_999, "unused"); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("missing source error = %v, want %v", err, ErrAssetNotFound)
	}
	if _, err := store.RenameAssetKey(source.ID, "bad/name"); !errors.Is(err, ErrAssetKeyInvalid) {
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
