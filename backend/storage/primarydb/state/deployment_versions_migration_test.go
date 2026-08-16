package state

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

// TestDeploymentVersionsRebuildMigration builds a v0.0.432-shape database —
// the deployment_config_versions log keyed only by (deployment_id, version) —
// and checks that opening it copies every row into deployment_versions with
// chronologically assigned ids, drops the old table, and reloads the same
// configs. The second Open proves the migration is a no-op once applied.
func TestDeploymentVersionsRebuildMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	db := sqlitedb.MustOpen(dbPath)
	apiV1Blob := testSpecWithState("v1", true).Encode()
	apiV2Blob := testSpecWithState("v2", false).Encode()
	webV1Blob := testSpecWithState("w1", true).Encode()
	for _, statement := range []string{
		`CREATE TABLE deployment_configs (
			deployment_id INTEGER PRIMARY KEY CHECK (deployment_id BETWEEN 1 AND 16777215),
			node_id INTEGER NOT NULL DEFAULT -1,
			space_id INTEGER NOT NULL DEFAULT 1 CHECK (space_id BETWEEN 0 AND 65535),
			name TEXT NOT NULL DEFAULT '',
			deleted INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE UNIQUE INDEX idx_deployment_configs_active_node_identity
			ON deployment_configs(node_id, space_id, name) WHERE deleted = 0`,
		`CREATE TABLE deployment_config_versions (
			deployment_id INTEGER NOT NULL,
			version INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			created_by INTEGER NOT NULL DEFAULT 0,
			spec_blob BLOB NOT NULL,
			PRIMARY KEY (deployment_id, version)
		)`,
		`INSERT INTO deployment_configs (deployment_id, node_id, space_id, name) VALUES (7, 1, 1, 'api')`,
		`INSERT INTO deployment_configs (deployment_id, node_id, space_id, name) VALUES (9, 1, 1, 'web')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	// Version rows are inserted out of chronological order so that the
	// migration's ORDER BY, not the source rowid order, drives id assignment.
	for _, row := range []struct {
		deploymentID, version, createdAt, createdBy int64
		blob                                        []byte
	}{
		{7, 2, 3000, 5, apiV2Blob},
		{9, 1, 2000, 4, webV1Blob},
		{7, 1, 1000, 4, apiV1Blob},
	} {
		if _, err := db.Exec(`INSERT INTO deployment_config_versions
			(deployment_id, version, created_at, created_by, spec_blob)
			VALUES (?, ?, ?, ?, ?)`,
			row.deploymentID, row.version, row.createdAt, row.createdBy, row.blob); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		store := Open(dbPath)
		cfg := store.configCache[7]
		if cfg == nil {
			t.Fatal("migrated deployment missing from cache")
		}
		if cfg.Version != 2 || cfg.NodeID != 1 || cfg.Identity.Name != "api" ||
			cfg.WorkloadVersion() != "v2" || cfg.WorkloadRunning() {
			t.Fatalf("migrated config = %+v", cfg)
		}
		if got := cfg.CreatedAt.UnixMilli(); got != 1000 {
			t.Fatalf("created_at = %d, want v1 row's 1000", got)
		}
		if got := cfg.UpdatedAt.UnixMilli(); got != 3000 || cfg.UpdatedBy != 5 {
			t.Fatalf("updated_at/by = %d/%d, want 3000/5", got, cfg.UpdatedBy)
		}
		history := store.MustFetchDeploymentHistory(7)
		if len(history) != 2 || history[0].Version != 1 || history[0].WorkloadVersion() != "v1" {
			t.Fatalf("migrated history = %+v", history)
		}
		if web := store.configCache[9]; web == nil || web.Version != 1 || web.Identity.Name != "web" {
			t.Fatalf("migrated web config = %+v", web)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE name = 'deployment_config_versions'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("deployment_config_versions was not dropped")
	}
	rows, err := db.Query(`SELECT id, deployment_id, version FROM deployment_versions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got [][3]int64
	for rows.Next() {
		var r [3]int64
		if err := rows.Scan(&r[0], &r[1], &r[2]); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := [][3]int64{{1, 7, 1}, {2, 9, 1}, {3, 7, 2}}
	if len(got) != len(want) {
		t.Fatalf("deployment_versions rows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("deployment_versions rows = %v, want %v (ids must be chronological and stable across reopens)", got, want)
		}
	}
}
