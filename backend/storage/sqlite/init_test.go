package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestConfigTableRenamesAreIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	_, err = db.Exec(`
CREATE TABLE opendeploy_config (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    updated_at  INTEGER NOT NULL,
    config_blob BLOB    NOT NULL
);
INSERT INTO opendeploy_config (id, updated_at, config_blob) VALUES (7, 123, x'0102');

CREATE TABLE user_configs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL,
    config_group TEXT    NOT NULL DEFAULT '',
    space_id     INTEGER NOT NULL DEFAULT 1,
    value        TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    updated_by   INTEGER NOT NULL DEFAULT 0
);
INSERT INTO user_configs (id, name, space_id, value, created_at, updated_at, updated_by)
VALUES (11, 'example', 1, 'value', 456, 456, 2);
`)
	if err != nil {
		t.Fatalf("seed legacy database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	db = mustInitPrimary(dbPath)

	for _, table := range []string{"opendeploy_config", "user_configs"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("look up old table %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("old table %s still exists", table)
		}
	}

	var updatedAt int64
	var configBlob []byte
	if err := db.QueryRow(`SELECT updated_at, config_blob FROM system_config_revisions WHERE id = 7`).Scan(&updatedAt, &configBlob); err != nil {
		t.Fatalf("load migrated system config revision: %v", err)
	}
	if updatedAt != 123 || len(configBlob) != 2 || configBlob[0] != 1 || configBlob[1] != 2 {
		t.Fatalf("system config revision changed: updated_at=%d blob=%v", updatedAt, configBlob)
	}

	var name, value string
	var version, updatedBy int64
	if err := db.QueryRow(`SELECT name, version, value, updated_by FROM configs WHERE id = 11`).Scan(&name, &version, &value, &updatedBy); err != nil {
		t.Fatalf("load migrated config: %v", err)
	}
	if name != "example" || version != 1 || value != "value" || updatedBy != 2 {
		t.Fatalf("config changed: name=%q version=%d value=%q updated_by=%d", name, version, value, updatedBy)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close migrated database: %v", err)
	}
	db = mustInitPrimary(dbPath)
	defer db.Close()
	if err := db.QueryRow(`SELECT value FROM configs WHERE id = 11`).Scan(&value); err != nil {
		t.Fatalf("load config after repeated initialization: %v", err)
	}
	if value != "value" {
		t.Fatalf("config changed after repeated initialization: value=%q", value)
	}
}
