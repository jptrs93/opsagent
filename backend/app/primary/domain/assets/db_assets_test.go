package assets

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func contentSha(blob []byte) string {
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])
}

// mustPutAssetContent lands blob in the content store (if absent) as a local
// file and returns its sha and storage key. Test-only convenience.
func mustPutAssetContent(s *state.Service, blob []byte) (sha, storageKey string) {
	sha = contentSha(blob)
	if row, ok := GetAssetStoreRowBySha(s.Queries(), sha); ok {
		return sha, row.ID
	}
	storageKey = uuid.Must(uuid.NewV7()).String()
	if err := os.WriteFile(localPath(storageKey), blob, 0o600); err != nil {
		panic(err)
	}
	InsertAssetStoreRow(s.Queries(), storageKey, sha, int64(len(blob)), 1, 0)
	return sha, storageKey
}

func storedContent(t *testing.T, joined pq.AssetVersionJoined) string {
	t.Helper()
	got, err := os.ReadFile(localPath(joined.Store.ID))
	if err != nil {
		t.Fatalf("read stored content %s: %v", joined.Store.ID, err)
	}
	return string(got)
}

func openTestStore(t *testing.T) *state.Service {
	t.Helper()
	useTempAssetRoot(t)
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// SetAssetByKey creates the asset in spaceID's root on first use and appends a
// version on each later call. Test-only convenience over the public write API.
func setAssetByKey(s *state.Service, key string, blob []byte, spaceIDs ...uint64) *pq.AssetEvent {
	spaceID := nodes.DefaultSpaceID
	if len(spaceIDs) > 0 {
		spaceID = nodes.NormalizedUserSpaceID(spaceIDs[0])
	}
	sha, storageKey := mustPutAssetContent(s, blob)
	if existing, ok := GetAssetInDirectory(s.Queries(), spaceID, 0, key); ok {
		a, err := AppendAssetVersion(s, existing.ID, 0, sha, storageKey, int64(len(blob)))
		if err != nil {
			panic(fmt.Sprintf("SetAssetByKey append: %v", err))
		}
		return a
	}
	a, err := CreateAssetWithVersion(s, key, spaceID, 0, 0, sha, storageKey, int64(len(blob)))
	if err != nil {
		panic(fmt.Sprintf("SetAssetByKey create: %v", err))
	}
	return a
}

func TestAssetsAreVersionedAndImmutable(t *testing.T) {
	store := openTestStore(t)

	a1 := setAssetByKey(store, "nginx.conf", []byte("events {}\n"))
	v1 := statetest.LatestValue(store, a1)
	if a1.AssetID == 0 || v1 == nil || v1.Version != 1 || a1.SpaceID() != nodes.DefaultSpaceID {
		t.Fatalf("first write = asset %d latest %+v space %d, want nonzero ids version 1 space %d", a1.AssetID, v1, a1.SpaceID(), nodes.DefaultSpaceID)
	}
	if v1.Sha256 != contentSha([]byte("events {}\n")) {
		t.Fatalf("first version sha = %q, want content sha", v1.Sha256)
	}
	a2 := setAssetByKey(store, "nginx.conf", []byte("events {}\nhttp {}\n"))
	v2 := statetest.LatestValue(store, a2)
	if v2.Ref == v1.Ref || v2.Version != 2 || a2.AssetID != a1.AssetID {
		t.Fatalf("second version = %s asset %d version %d, want new ref, same asset, version 2", v2.Ref, a2.AssetID, v2.Version)
	}

	latest, ok := GetAsset(store.Queries(), a1.AssetID)
	if !ok {
		t.Fatal("asset not found")
	}
	if lv := statetest.LatestValue(store, latest); lv.Ref != v2.Ref || lv.Version != 2 || latest.SpaceID() != nodes.DefaultSpaceID {
		t.Fatalf("latest version = %+v space %d", lv, latest.SpaceID())
	}
	if joined, ok := GetAssetValueJoined(store.Queries(), v2.Ref); !ok || storedContent(t, joined) != "events {}\nhttp {}\n" {
		t.Fatalf("latest content ok=%v", ok)
	}
	ref, ok := GetAssetVersionRef(store.Queries(), v2.Ref)
	if !ok || ref.Key != "nginx.conf" || ref.Ref != (apigen.ValueRef{ID: a1.AssetID, Version: 2}) || ref.SpaceID != nodes.DefaultSpaceID {
		t.Fatalf("version ref by pair = %+v ok=%v", ref, ok)
	}
	if joined, ok := GetAssetValueJoined(store.Queries(), v1.Ref); !ok || joined.Version.ValueVersion != 1 || storedContent(t, joined) != "events {}\n" {
		t.Fatalf("old value by pair = %+v ok=%v", joined, ok)
	}

	// The old version is immutable: still listed and its content still resolves.
	if old := statetest.ValueVersions(store, latest)[1]; old.Ref != v1.Ref || old.Version != 1 {
		t.Fatalf("old version = %+v", old)
	}
	if joined, ok := GetAssetValueJoined(store.Queries(), v1.Ref); !ok || storedContent(t, joined) != "events {}\n" {
		t.Fatalf("old content ok=%v", ok)
	}

	items := ListAssets(store.Queries())
	if len(items) != 1 {
		t.Fatalf("asset list length = %d, want 1", len(items))
	}
	asset := items[0]
	if asset.AssetID != a1.AssetID || asset.Value.Fs.Key != "nginx.conf" || asset.SpaceID() != nodes.DefaultSpaceID {
		t.Fatalf("asset = %+v", asset)
	}
	// content_versions are newest first: [0] is the latest.
	if len(statetest.ValueVersions(store, asset)) != 2 ||
		statetest.ValueVersions(store, asset)[0].Ref != v2.Ref || statetest.ValueVersions(store, asset)[0].Version != 2 ||
		statetest.ValueVersions(store, asset)[0].SizeBytes != int64(len("events {}\nhttp {}\n")) ||
		statetest.ValueVersions(store, asset)[0].Sha256 != v2.Sha256 ||
		!statetest.ValueVersions(store, asset)[0].CreatedAt.Equal(v2.CreatedAt) ||
		statetest.ValueVersions(store, asset)[1].Ref != v1.Ref || statetest.ValueVersions(store, asset)[1].Version != 1 {
		t.Fatalf("asset content versions = %+v", statetest.ValueVersions(store, asset))
	}

	DeleteAsset(store, a1.AssetID, nil)
	if _, ok := GetAsset(store.Queries(), a1.AssetID); ok {
		t.Fatal("asset still found after delete")
	}
	if _, ok := GetAsset(store.Queries(), a1.AssetID); ok {
		t.Fatal("asset row still found after delete")
	}
}

func TestAssetVersionsShareContentBySha(t *testing.T) {
	store := openTestStore(t)
	blob := []byte("shared content")

	a := setAssetByKey(store, "a.conf", blob)
	b := setAssetByKey(store, "b.conf", blob)
	aSha := statetest.LatestValue(store, a).Sha256
	if aSha != statetest.LatestValue(store, b).Sha256 {
		t.Fatalf("shas differ: %q vs %q", aSha, statetest.LatestValue(store, b).Sha256)
	}
	if CountAssetVersionsBySha(store.Queries(), aSha) != 2 {
		t.Fatalf("versions by sha = %d, want 2", CountAssetVersionsBySha(store.Queries(), aSha))
	}
	if rows := ListAssetStoreRowMetas(store.Queries()); len(rows) != 1 {
		t.Fatalf("store rows = %d, want 1 shared row", len(rows))
	}
}

func TestAssetVersionRequiresStoredContent(t *testing.T) {
	store := openTestStore(t)
	if _, err := CreateAssetWithVersion(store, "app.yaml", nodes.DefaultSpaceID, 0, 0, contentSha([]byte("missing")), "no-such-key", 7); !errors.Is(err, ErrAssetContentMissing) {
		t.Fatalf("create without content err = %v, want ErrAssetContentMissing", err)
	}
	a := setAssetByKey(store, "app.yaml", []byte("x"))
	if _, err := AppendAssetVersion(store, a.AssetID, 0, contentSha([]byte("missing")), "no-such-key", 7); !errors.Is(err, ErrAssetContentMissing) {
		t.Fatalf("append without content err = %v, want ErrAssetContentMissing", err)
	}
	_, key := mustPutAssetContent(store, []byte("x"))
	if _, err := AppendAssetVersion(store, a.AssetID, 0, contentSha([]byte("other")), key, 5); !errors.Is(err, ErrAssetContentMissing) {
		t.Fatalf("append with a key holding different content err = %v, want ErrAssetContentMissing", err)
	}
}

func TestRenameAssetPreservesVersions(t *testing.T) {
	store := openTestStore(t)
	a := setAssetByKey(store, "old-name", []byte("one"))
	v1 := statetest.LatestValue(store, a)
	largeSha := contentSha([]byte("large"))
	InsertAssetStoreRow(store.Queries(), "large-store", largeSha, 12_000_000, 1, 0)
	appended, err := AppendAssetVersion(store, a.AssetID, 0, largeSha, "large-store", 12_000_000)
	if err != nil {
		t.Fatalf("append version: %v", err)
	}
	v2 := statetest.LatestValue(store, appended)
	if v2.Version != 2 || v2.Sha256 != largeSha || v2.SizeBytes != 12_000_000 || appended.Value.StorageKey != "large-store" {
		t.Fatalf("large version = %+v key %q, want version 2 of 12MB with the store sha and key", v2, appended.Value.StorageKey)
	}

	renamed, err := RenameAssetKey(store, a.AssetID, "new-name")
	if err != nil {
		t.Fatalf("rename asset: %v", err)
	}
	if renamed.AssetID != a.AssetID || renamed.Value.Fs.Key != "new-name" ||
		len(statetest.ValueVersions(store, renamed)) != 2 || statetest.ValueVersions(store, renamed)[0].Ref != v2.Ref || statetest.ValueVersions(store, renamed)[0].Version != 2 {
		t.Fatalf("renamed asset = %+v", renamed)
	}
	if _, ok := GetAssetInDirectory(store.Queries(), nodes.DefaultSpaceID, 0, "old-name"); ok {
		t.Fatal("old asset key still exists")
	}

	// Version rows are untouched by the rename: ids, metadata, and content all
	// survive; only the key changed.
	want := []*statetest.ValueVersion{v2, v1} // newest first
	for i, got := range statetest.ValueVersions(store, renamed) {
		if got.Ref != want[i].Ref || got.Version != want[i].Version ||
			got.SizeBytes != want[i].SizeBytes || got.Sha256 != want[i].Sha256 ||
			!got.CreatedAt.Equal(want[i].CreatedAt) {
			t.Fatalf("renamed version %d = %+v, want original metadata %+v", i, got, want[i])
		}
		ref, ok := GetAssetVersionRef(store.Queries(), got.Ref)
		if !ok || ref.Key != "new-name" || ref.Ref.ID != renamed.AssetID || ref.SpaceID != nodes.DefaultSpaceID {
			t.Fatalf("version ref %d = %+v ok=%v, want the new key", i, ref, ok)
		}
	}
	if joined, ok := GetAssetValueJoined(store.Queries(), v1.Ref); !ok || storedContent(t, joined) != "one" {
		t.Fatalf("old content after rename ok=%v", ok)
	}

	threeSha, threeKey := mustPutAssetContent(store, []byte("three"))
	after, err := AppendAssetVersion(store, a.AssetID, 0, threeSha, threeKey, 5)
	if err != nil {
		t.Fatalf("append after rename: %v", err)
	}
	if statetest.LatestValue(store, after).Version != 3 || after.Value.Fs.Key != "new-name" {
		t.Fatalf("version after rename = v%d key %q, want v3 new-name", statetest.LatestValue(store, after).Version, after.Value.Fs.Key)
	}
}

func TestRenameAssetRejectsExistingKey(t *testing.T) {
	store := openTestStore(t)
	source := setAssetByKey(store, "source", []byte("source"))
	destination := setAssetByKey(store, "destination", []byte("destination"))

	if _, err := RenameAssetKey(store, source.AssetID, "destination"); !errors.Is(err, ErrAssetAlreadyExists) {
		t.Fatalf("rename collision error = %v, want %v", err, ErrAssetAlreadyExists)
	}
	if got, ok := GetAssetInDirectory(store.Queries(), nodes.DefaultSpaceID, 0, "source"); !ok || got.ID != source.AssetID {
		t.Fatalf("source after collision = %+v, ok=%v", got, ok)
	}
	if got, ok := GetAssetInDirectory(store.Queries(), nodes.DefaultSpaceID, 0, "destination"); !ok || got.ID != destination.AssetID {
		t.Fatalf("destination after collision = %+v, ok=%v", got, ok)
	}
	if _, err := RenameAssetKey(store, 999_999, "unused"); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("missing source error = %v, want %v", err, ErrAssetNotFound)
	}
	if _, err := RenameAssetKey(store, source.AssetID, "bad/name"); !errors.Is(err, ErrAssetKeyInvalid) {
		t.Fatalf("invalid key error = %v, want %v", err, ErrAssetKeyInvalid)
	}
}

func TestCreateAssetRejectsDuplicateAndInvalidKeys(t *testing.T) {
	store := openTestStore(t)
	sha, storageKey := mustPutAssetContent(store, []byte("x"))
	if _, err := CreateAssetWithVersion(store, "app.yaml", nodes.DefaultSpaceID, 0, 0, sha, storageKey, 1); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := CreateAssetWithVersion(store, "app.yaml", nodes.DefaultSpaceID, 0, 0, sha, storageKey, 1); !errors.Is(err, ErrAssetAlreadyExists) {
		t.Fatalf("duplicate create error = %v, want %v", err, ErrAssetAlreadyExists)
	}
	// Same key in another space is a different file system.
	if _, err := CreateAssetWithVersion(store, "app.yaml", 2, 0, 0, sha, storageKey, 1); err != nil {
		t.Fatalf("create in second space: %v", err)
	}
	for _, key := range []string{"", ".", "..", "a/b", "a\\b", "a\x00b"} {
		if _, err := CreateAssetWithVersion(store, key, nodes.DefaultSpaceID, 0, 0, sha, storageKey, 1); !errors.Is(err, ErrAssetKeyInvalid) {
			t.Fatalf("key %q error = %v, want %v", key, err, ErrAssetKeyInvalid)
		}
	}
}

func TestSoftDeleteHidesRowAndFreesName(t *testing.T) {
	store := openTestStore(t)

	v := setAssetByKey(store, "app.conf", []byte("v1"))
	DeleteAsset(store, v.AssetID, nil)
	if _, ok := GetAsset(store.Queries(), v.AssetID); ok {
		t.Fatal("deleted asset still resolves by id")
	}
	if _, ok := GetAssetInDirectory(store.Queries(), nodes.DefaultSpaceID, 0, "app.conf"); ok {
		t.Fatal("deleted asset still resolves by key")
	}
	if got := ListAssets(store.Queries()); len(got) != 0 {
		t.Fatalf("ListAssets after delete = %d items, want 0", len(got))
	}
	// The name is reusable, and the old asset's version rows go with it.
	replacement := setAssetByKey(store, "app.conf", []byte("v2"))
	if replacement.AssetID == v.AssetID {
		t.Fatal("recreated asset reused the deleted identity")
	}
	if joined, ok := GetAssetValueJoined(store.Queries(), apigen.ValueRef{ID: v.AssetID, Version: 1}); ok {
		t.Fatalf("deleted asset version still resolves: %+v", joined)
	}
}
