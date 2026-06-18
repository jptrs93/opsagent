package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestPrimaryMigratesEnvironmentIdentityToSpaceID(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open old db: %v", err)
	}
	_, err = db.Exec(`
CREATE TABLE deployment_configs (
    deployment_id   INTEGER PRIMARY KEY,
    environment     TEXT    NOT NULL DEFAULT '',
    machine         TEXT    NOT NULL DEFAULT '',
    name            TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL DEFAULT 0,
    version         INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL,
    updated_by      INTEGER NOT NULL DEFAULT 0,
    spec_blob       BLOB    NOT NULL,
    desired_version TEXT    NOT NULL DEFAULT '',
    desired_running INTEGER NOT NULL DEFAULT 0,
    deleted         INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX idx_deployment_configs_identity
    ON deployment_configs(environment, machine, name);
INSERT INTO deployment_configs (
    deployment_id, environment, machine, name, created_at, version, updated_at,
    updated_by, spec_blob, desired_version, desired_running, deleted
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`,
		7, "prod", "m1", "api", time.UnixMilli(1000).UnixMilli(), 1, time.UnixMilli(2000).UnixMilli(),
		0, nonEmptySpec().Encode(), "v1", 1, 0,
		8, "OPENDEPLOY", "primary", "opendeploy", time.UnixMilli(1000).UnixMilli(), 1, time.UnixMilli(2000).UnixMilli(),
		0, nonEmptySpec().Encode(), "v1", 1, 0,
	)
	if err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close old db: %v", err)
	}

	store := NewPrimaryStorage(dbPath)
	defer store.db.Close()

	columns := tableColumns(t, store.db, "deployment_configs")
	if columns["environment"] {
		t.Fatalf("environment column still exists after migration")
	}
	if !columns["space_id"] {
		t.Fatalf("space_id column missing after migration")
	}

	spaces := store.ListSpaces()
	if len(spaces) < 2 || spaces[0].ID != 0 || spaces[0].Name != "opendeploy" || spaces[1].ID != 1 || spaces[1].Name != "default" {
		t.Fatalf("seeded spaces missing: %+v", spaces)
	}

	configs := store.ListActiveDeploymentConfigs()
	if len(configs) != 2 {
		t.Fatalf("expected migrated deployments, got %d", len(configs))
	}
	byName := map[string]int32{}
	for _, cfg := range configs {
		byName[cfg.ConfigID.Name] = cfg.ConfigID.SpaceID
	}
	if byName["api"] != 1 {
		t.Fatalf("regular deployment not migrated to default space: %+v", byName)
	}
	if byName["opendeploy"] != 0 {
		t.Fatalf("opendeploy deployment not migrated to opendeploy space: %+v", byName)
	}
}

func tableColumns(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("table_info %s: %v", table, err)
	}
	defer rows.Close()

	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name string
		var typ string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table_info %s: %v", table, err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info %s: %v", table, err)
	}
	return columns
}
