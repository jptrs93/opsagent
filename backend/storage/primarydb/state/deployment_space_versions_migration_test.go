package state

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func TestDeploymentSpaceVersionMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	db := sqlitedb.MustOpen(dbPath)
	mustExec := func(stmt string, args ...any) {
		t.Helper()
		if _, err := db.Exec(stmt, args...); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	mustExec(`CREATE TABLE deployment_configs (
	    deployment_id   INTEGER PRIMARY KEY CHECK (deployment_id BETWEEN 1 AND 16777215),
	    node_id         INTEGER NOT NULL DEFAULT -1,
	    space_id        INTEGER NOT NULL DEFAULT 1 CHECK (space_id BETWEEN 0 AND 65535),
	    name            TEXT    NOT NULL DEFAULT '',
	    deleted_at      INTEGER NOT NULL DEFAULT 0)`)
	mustExec(`CREATE UNIQUE INDEX idx_deployment_configs_active_node_identity
	    ON deployment_configs(node_id, space_id, name) WHERE deleted_at = 0`)
	mustExec(`CREATE TABLE deployment_versions (
	    id INTEGER PRIMARY KEY AUTOINCREMENT,
	    deployment_id INTEGER NOT NULL,
	    version INTEGER NOT NULL,
	    created_at INTEGER NOT NULL,
	    author INTEGER NOT NULL DEFAULT 0,
	    spec_blob BLOB NOT NULL,
	    UNIQUE (deployment_id, version))`)
	mustExec(`CREATE TABLE scheduled_instances (
	    id INTEGER PRIMARY KEY AUTOINCREMENT,
	    deployment_id INTEGER NOT NULL,
	    deployment_version INTEGER NOT NULL,
	    node_id INTEGER NOT NULL,
	    instance_ordinal INTEGER NOT NULL DEFAULT 0)`)
	mustExec(`CREATE TABLE scheduled_instance_versions (
	    id INTEGER PRIMARY KEY AUTOINCREMENT,
	    scheduled_instance_id INTEGER NOT NULL,
	    version INTEGER NOT NULL,
	    created_at INTEGER NOT NULL,
	    state INTEGER NOT NULL,
	    UNIQUE (scheduled_instance_id, version))`)

	specBlob := nonEmptySpec().Encode()
	mustExec(`INSERT INTO deployment_configs (deployment_id, node_id, space_id, name) VALUES (1, 1, 7, 'web')`)
	mustExec(`INSERT INTO deployment_versions (deployment_id, version, created_at, author, spec_blob) VALUES (1, 1, 1000, 3, ?)`, specBlob)
	mustExec(`INSERT INTO deployment_versions (deployment_id, version, created_at, author, spec_blob) VALUES (1, 2, 2000, 3, ?)`, specBlob)
	mustExec(`INSERT INTO scheduled_instances (id, deployment_id, deployment_version, node_id) VALUES (11, 1, 2, 1)`)
	mustExec(`INSERT INTO scheduled_instance_versions (scheduled_instance_id, version, created_at, state) VALUES (11, 1, 2000, 0)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		store := Open(dbPath)
		cfg := store.FetchDeploymentConfig(1)
		if cfg == nil || cfg.SpaceID != 7 || cfg.SpaceVersion != 1 {
			t.Fatalf("migrated config identity = %+v, want space 7 spaceV1", cfg)
		}
		st := findInstanceState(t, store, 11)
		if st.Instance.SpaceID != 7 {
			t.Fatalf("migrated placement pin = space %d, want space 7", st.Instance.SpaceID)
		}
		if st.Config.SpaceID != 7 {
			t.Fatalf("migrated placement view = space %d, want space 7", st.Config.SpaceID)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}

	db = sqlitedb.MustOpen(dbPath)
	defer db.Close()
	var logRowID, logDeployment, logVersion, logSpace, logCreatedAt, logAuthor int64
	if err := db.QueryRow(`SELECT id, deployment_id, version, space_id, created_at, author FROM deployment_space_versions`).
		Scan(&logRowID, &logDeployment, &logVersion, &logSpace, &logCreatedAt, &logAuthor); err != nil {
		t.Fatalf("read backfilled space version log: %v", err)
	}
	if logDeployment != 1 || logVersion != 1 || logSpace != 7 || logCreatedAt != 1000 || logAuthor != 0 {
		t.Fatalf("backfilled log row = deployment %d v%d space %d created %d author %d, want 1/v1/7/1000/0",
			logDeployment, logVersion, logSpace, logCreatedAt, logAuthor)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM deployment_space_versions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("space version log rows = %d, want exactly 1 after re-runs", count)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('deployment_configs') WHERE name = 'space_id'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("deployment_configs.space_id survived the migration")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index'
		AND name = 'idx_deployment_configs_active_node_identity'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("identity index survived the migration")
	}
	var pinRowID int64
	if err := db.QueryRow(`SELECT deployment_space_version_id FROM scheduled_instances WHERE id = 11`).Scan(&pinRowID); err != nil {
		t.Fatal(err)
	}
	if pinRowID != logRowID {
		t.Fatalf("instance pin = space version row %d, want row %d", pinRowID, logRowID)
	}
}
