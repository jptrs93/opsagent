package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// legacyPrimarySchema is the pre-migration shape of the tables the primary
// migration touches: deployment_configs without created_at, and a populated
// deployment_identifiers table. Used to exercise the upgrade path that
// fresh-init never hits.
const legacyPrimarySchema = `
CREATE TABLE deployment_configs (
    deployment_id   INTEGER PRIMARY KEY,
    environment     TEXT    NOT NULL DEFAULT '',
    machine         TEXT    NOT NULL DEFAULT '',
    name            TEXT    NOT NULL DEFAULT '',
    version         INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL,
    updated_by      INTEGER NOT NULL DEFAULT 0,
    spec_blob       BLOB    NOT NULL,
    desired_version TEXT    NOT NULL DEFAULT '',
    desired_running INTEGER NOT NULL DEFAULT 0,
    deleted         INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE deployment_identifiers (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    environment TEXT    NOT NULL,
    machine     TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    UNIQUE(environment, machine, name)
);
CREATE TABLE deployment_config_history (deployment_id INTEGER, version INTEGER, updated_at INTEGER, spec_blob BLOB, PRIMARY KEY(deployment_id, version));
CREATE TABLE deployment_status (deployment_id INTEGER PRIMARY KEY, updated_at INTEGER);
CREATE TABLE deployment_status_history (deployment_id INTEGER, updated_at INTEGER, PRIMARY KEY(deployment_id, updated_at));
`

// TestPrimaryMigrationBackfillsCreatedAtPurgesDeletedAndDropsIdentifiers
// exercises the one-shot upgrade against a legacy database: created_at is
// backfilled (from deployment_identifiers, falling back to updated_at),
// soft-deleted deployments are purged with their history/status, and the
// deployment_identifiers table is dropped.
func TestPrimaryMigrationBackfillsCreatedAtPurgesDeletedAndDropsIdentifiers(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(legacyPrimarySchema); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}

	// Identities: id 5 (live) and id 9 (to be deleted) have first-seen times.
	if _, err := db.Exec(`INSERT INTO deployment_identifiers (id, environment, machine, name, created_at)
		VALUES (5,'prod','m1','api',111), (9,'prod','m1','gone',222)`); err != nil {
		t.Fatalf("seed identifiers: %v", err)
	}
	// Configs: id 5 backfills from its identifier; id 7 has no identifier so it
	// must fall back to updated_at; id 9 is soft-deleted and must be purged.
	if _, err := db.Exec(`INSERT INTO deployment_configs
		(deployment_id, environment, machine, name, version, updated_at, updated_by, spec_blob, desired_version, desired_running, deleted)
		VALUES
		(5,'prod','m1','api',1,1000,0,X'01',' ',0,0),
		(7,'prod','m1','orphan',1,1500,0,X'01',' ',0,0),
		(9,'prod','m1','gone',1,2000,0,X'01',' ',0,1)`); err != nil {
		t.Fatalf("seed configs: %v", err)
	}
	// Dependent rows for the deleted deployment that must be cascaded away.
	if _, err := db.Exec(`INSERT INTO deployment_config_history (deployment_id, version, updated_at, spec_blob) VALUES (9,1,2000,X'01')`); err != nil {
		t.Fatalf("seed config history: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO deployment_status (deployment_id, updated_at) VALUES (9,2000)`); err != nil {
		t.Fatalf("seed status: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO deployment_status_history (deployment_id, updated_at) VALUES (9,2000)`); err != nil {
		t.Fatalf("seed status history: %v", err)
	}

	applyMigrations(db, primaryMigrations)

	// id 5: created_at backfilled from the identifier.
	var createdAt5 int64
	if err := db.QueryRow(`SELECT created_at FROM deployment_configs WHERE deployment_id = 5`).Scan(&createdAt5); err != nil {
		t.Fatalf("read id 5: %v", err)
	}
	if createdAt5 != 111 {
		t.Errorf("id 5 created_at = %d, want 111 (from identifier)", createdAt5)
	}

	// id 7: no identifier row, so created_at falls back to updated_at.
	var createdAt7 int64
	if err := db.QueryRow(`SELECT created_at FROM deployment_configs WHERE deployment_id = 7`).Scan(&createdAt7); err != nil {
		t.Fatalf("read id 7: %v", err)
	}
	if createdAt7 != 1500 {
		t.Errorf("id 7 created_at = %d, want 1500 (fallback to updated_at)", createdAt7)
	}

	// id 9: soft-deleted deployment and all its dependent rows purged.
	for _, tbl := range []string{"deployment_configs", "deployment_config_history", "deployment_status", "deployment_status_history"} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM ` + tbl + ` WHERE deployment_id = 9`).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", tbl, err)
		}
		if n != 0 {
			t.Errorf("%s still has %d rows for purged deployment 9", tbl, n)
		}
	}

	// deployment_identifiers is dropped.
	var name string
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='deployment_identifiers'`).Scan(&name)
	if err != sql.ErrNoRows {
		t.Errorf("deployment_identifiers should be dropped, got err=%v name=%q", err, name)
	}

	// Re-running is idempotent (duplicate column / missing table swallowed or no-op).
	applyMigrations(db, primaryMigrations)
	if err := db.QueryRow(`SELECT created_at FROM deployment_configs WHERE deployment_id = 5`).Scan(&createdAt5); err != nil {
		t.Fatalf("read id 5 after re-run: %v", err)
	}
	if createdAt5 != 111 {
		t.Errorf("id 5 created_at = %d after re-run, want 111", createdAt5)
	}
}
