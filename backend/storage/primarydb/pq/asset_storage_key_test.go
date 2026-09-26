package pq

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestStorageKeyBackfillLinksLegacyEventRowsToTheirStoreRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary.db")
	q := Open(path)
	ctx := context.Background()
	if _, err := q.db.ExecContext(ctx, `INSERT INTO asset_store (id, sha256, size_bytes, local_status, remote_status, created_at)
		VALUES ('store-a', 'sha-a', 3, 1, 0, 1), ('store-b', 'sha-b', 4, 1, 0, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := q.db.ExecContext(ctx, `INSERT INTO asset_event_log (
		global_seq, event_time, created_time, author, asset_id, version, value_version, value_changed,
		key, asset_directory_id, space_id, size_bytes, sha256, event_type
	) VALUES (1, 1, 1, 0, 7, 1, 1, 1, 'a.conf', 0, 1, 3, 'sha-a', 1),
	         (2, 2, 1, 0, 7, 2, 2, 1, 'a.conf', 0, 1, 4, 'sha-b', 2),
	         (3, 3, 1, 0, 7, 3, 2, 0, 'b.conf', 0, 1, 4, 'sha-b', 2),
	         (4, 4, 4, 0, 8, 1, 1, 1, 'gone.conf', 0, 1, 5, 'sha-missing', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}

	q = Open(path)
	defer q.Close()
	events, err := q.ListAllAssetEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int32]string{1: "store-a", 2: "store-b", 3: "store-b"}
	for _, e := range events {
		if e.AssetID != 7 {
			continue
		}
		if e.Value.StorageKey != want[e.Version] {
			t.Fatalf("asset 7 version %d storage key = %q, want %q", e.Version, e.Value.StorageKey, want[e.Version])
		}
	}
	gone, err := q.GetLatestAssetEvent(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	if gone.Value.StorageKey != "" {
		t.Fatalf("row without a store row got storage key %q", gone.Value.StorageKey)
	}
	joined, err := q.GetAssetVersionJoinedByRef(ctx, apigen.ValueRef{ID: 7, Version: 2})
	if err != nil || joined.Store.ID != "store-b" || joined.Version.StorageKey != "store-b" {
		t.Fatalf("joined value = %+v err=%v, want store-b", joined, err)
	}
}
