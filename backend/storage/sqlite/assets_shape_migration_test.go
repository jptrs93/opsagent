package sqlite

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

// buildOldShapeAssetDB creates a database with the pre-directories assets
// table: one row per version, grouped only by key.
func buildOldShapeAssetDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{
		`CREATE TABLE assets (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			key         TEXT    NOT NULL,
			space_id    INTEGER NOT NULL DEFAULT 1,
			created_at  INTEGER NOT NULL,
			version     INTEGER NOT NULL,
			format      TEXT    NOT NULL DEFAULT '',
			location    TEXT    NOT NULL DEFAULT '',
			size_bytes  INTEGER NOT NULL DEFAULT 0,
			blob        BLOB    NOT NULL,
			UNIQUE (key, version)
		)`,
		`CREATE INDEX idx_assets_key_created_at ON assets(key, created_at)`,
		// nginx.conf: two versions whose space diverged; the group must take
		// the newest version's space (2).
		`INSERT INTO assets (id, key, space_id, created_at, version, format, location, size_bytes, blob)
		 VALUES (1, 'nginx.conf', 1, 100, 1, 'nginx', '', 9, x'6576656e7473207b7d0a')`,
		`INSERT INTO assets (id, key, space_id, created_at, version, format, location, size_bytes, blob)
		 VALUES (3, 'nginx.conf', 2, 300, 2, 'nginx', '', 4, x'68747470')`,
		`INSERT INTO assets (id, key, space_id, created_at, version, format, location, size_bytes, blob)
		 VALUES (2, 'big.bin', 1, 200, 1, '', 'local://2', 12000000, x'')`,
		// An interrupted upload: pending rows migrate too, so startup recovery
		// still finds its row by the preserved id.
		`INSERT INTO assets (id, key, space_id, created_at, version, format, location, size_bytes, blob)
		 VALUES (7, 'staged.bin', 1, 700, 1, '', 'pending://.upload-x', 15000000, x'')`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("build old shape: %v\n%s", err, stmt)
		}
	}
}

func TestAssetShapeMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary.db")
	buildOldShapeAssetDB(t, path)

	store := NewPrimaryStorage(path)

	// Version row ids are preserved verbatim: deployment configs pin them.
	for _, want := range []struct {
		id      int32
		key     string
		version int32
		space   int32
		loc     string
	}{
		{1, "nginx.conf", 1, 2, ""},
		{3, "nginx.conf", 2, 2, ""},
		{2, "big.bin", 1, 1, "local://2"},
	} {
		v, ok := store.GetAssetVersionByID(want.id)
		if !ok {
			t.Fatalf("version %d missing after migration", want.id)
		}
		if v.Key != want.key || v.Version != want.version || v.SpaceID != want.space || v.Location != want.loc {
			t.Fatalf("version %d = %+v, want %+v", want.id, v, want)
		}
	}
	if _, ok := store.GetAssetVersionByID(7); ok {
		t.Fatal("pending version visible to published lookups")
	}
	if v, ok := store.GetAssetVersionByIDIncludingPending(7); !ok || v.Key != "staged.bin" || v.Location != "pending://.upload-x" {
		t.Fatalf("pending version = %+v ok=%v", v, ok)
	}

	// One asset per key, and the new asset id space sits above every migrated
	// version id so the two id spaces cannot be silently cross-joined.
	items := store.ListAssets()
	if len(items) != 2 { // staged.bin has no published version yet
		t.Fatalf("asset list = %d entries, want 2", len(items))
	}
	for _, meta := range items {
		if meta.ID <= 7 {
			t.Fatalf("asset id %d for %s overlaps preserved version ids", meta.ID, meta.Key)
		}
	}
	byKey := map[string]int32{}
	for _, meta := range items {
		byKey[meta.Key] = meta.VersionRefs[0].Version
	}
	if byKey["nginx.conf"] != 2 || byKey["big.bin"] != 1 {
		t.Fatalf("asset latest versions = %+v", byKey)
	}

	// Appending after migration continues the version sequence.
	nginx, _ := store.GetAssetInRootByKey(2, "nginx.conf")
	v3, err := store.AppendAssetVersion(int32(nginx.ID), 0, "", 1, []byte("x"))
	if err != nil {
		t.Fatalf("append after migration: %v", err)
	}
	if v3.Version != 3 {
		t.Fatalf("appended version = %d, want 3", v3.Version)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Idempotence: a second startup sees the new shape and changes nothing.
	store = NewPrimaryStorage(path)
	defer store.Close()
	if len(store.ListAssets()) != 2 {
		t.Fatal("second startup changed asset count")
	}
	if v, ok := store.GetAssetVersionByID(1); !ok || v.Key != "nginx.conf" {
		t.Fatal("second startup lost version 1")
	}
}

func TestAssetShapeMigrationOnSecondary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secondary.db")
	buildOldShapeAssetDB(t, path)

	// Secondaries share the schema files, so their (normally empty, here
	// deliberately populated) old-shape table must be transformed the same way.
	store := NewSecondaryStorage(path)
	defer store.Close()

	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM asset_versions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("asset_versions rows = %d, want 4", count)
	}
	var hasBlob int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('assets') WHERE name = 'blob'`).Scan(&hasBlob); err != nil {
		t.Fatal(err)
	}
	if hasBlob != 0 {
		t.Fatal("assets table still has old shape on secondary")
	}
}

func TestFreshInstallHasTargetAssetShape(t *testing.T) {
	store := NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	for _, table := range []string{"assets", "asset_versions", "asset_directories"} {
		var n int
		if err := store.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, table)).Scan(&n); err != nil {
			t.Fatalf("query %s: %v", table, err)
		}
		if n != 0 {
			t.Fatalf("%s has %d rows on fresh install", table, n)
		}
	}
}
