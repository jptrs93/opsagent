package state

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func TestAssetStoreMigrationFromLegacyShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary.db")
	db := sqlitedb.MustOpen(path)
	stmts := []string{
		`CREATE TABLE assets (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			space_id INTEGER NOT NULL DEFAULT 1,
			key TEXT NOT NULL,
			asset_directory_id INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			created_by INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE asset_versions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			asset_id INTEGER NOT NULL,
			version INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			created_by INTEGER NOT NULL DEFAULT 0,
			location TEXT NOT NULL DEFAULT '',
			size_bytes INTEGER NOT NULL DEFAULT 0,
			blob BLOB NOT NULL,
			UNIQUE (asset_id, version)
		)`,
		`INSERT INTO assets (id, space_id, key, asset_directory_id, created_at) VALUES
			(1, 1, 'a.conf', 0, 100), (2, 1, 'big.bin', 0, 200), (3, 1, 'pending.bin', 0, 300)`,
		`INSERT INTO asset_versions (id, asset_id, version, created_at, location, size_bytes, blob) VALUES
			(1, 1, 1, 100, '', 5, X'68656C6C6F'),
			(2, 2, 1, 200, 'local://2', 12000000, X''),
			(3, 2, 2, 210, 's3://bucket/prefix/3', 12000001, X''),
			(4, 3, 1, 300, 'pending://.upload-x', 9, X'')`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed legacy shape: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store := Open(path)

	if _, ok := store.GetAssetRow(3); ok {
		t.Fatal("asset with only a pending upload survived the migration")
	}
	inline, ok := store.GetAssetVersionJoined(1)
	if !ok || inline.Version.Sha256 != "legacy:1" || inline.Store.ID != "1" ||
		string(inline.Store.InlineBlob) != "hello" || inline.Store.LocalStatus != 0 || inline.Store.RemoteStatus != 0 {
		t.Fatalf("inline version after migration = %+v ok=%v", inline, ok)
	}
	local, ok := store.GetAssetVersionJoined(2)
	if !ok || local.Version.Sha256 != "legacy:2" || local.Store.ID != "2" ||
		local.Store.LocalStatus != 1 || local.Store.RemoteStatus != 0 {
		t.Fatalf("local version after migration = %+v ok=%v", local, ok)
	}
	remote, ok := store.GetAssetVersionJoined(3)
	if !ok || remote.Version.Sha256 != "legacy:3" || remote.Store.ID != "3" ||
		remote.Store.LocalStatus != 0 || remote.Store.RemoteStatus != 1 {
		t.Fatalf("s3 version after migration = %+v ok=%v", remote, ok)
	}

	items := store.ListAssets()
	if len(items) != 2 {
		t.Fatalf("assets after migration = %d, want 2", len(items))
	}
	if v, ok := store.GetAssetVersionByID(1); !ok || v.Sha256 != "" || v.Location != "" || string(v.Blob) != "hello" {
		t.Fatalf("inline wire version = %+v ok=%v, want empty sha and inline blob", v, ok)
	}
	if v, ok := store.GetAssetVersionByID(3); !ok || v.Location != "s3://3" {
		t.Fatalf("s3 wire version = %+v ok=%v, want derived s3 location", v, ok)
	}

	newVersion, err := store.AppendAssetVersion(1, 0, store.MustPutInlineAssetContent([]byte("fresh")), 5)
	if err != nil {
		t.Fatalf("append after migration: %v", err)
	}
	if newVersion.Version != 2 {
		t.Fatalf("appended version = %d, want 2", newVersion.Version)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := Open(path)
	defer reopened.Close()
	if rows := reopened.ListAssetStoreRowMetas(); len(rows) != 4 {
		t.Fatalf("store rows after re-open = %d, want 4 (re-run must be a no-op)", len(rows))
	}
	if len(reopened.ListAssets()) != 2 {
		t.Fatal("assets changed after re-running migrations")
	}
}
