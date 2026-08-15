package state

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

// TestDeploymentConfigSplitMigration builds a pre-split database — the wide
// deployment_configs row plus its deployment_config_history log — and checks
// that opening it copies every version into deployment_config_versions, slims
// the identity table, and reloads the same current config. The second Open
// proves the migration statements are no-ops once applied.
func TestDeploymentConfigSplitMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	db := sqlitedb.MustOpen(dbPath)
	v1Blob := testSpecWithState("v1", true).Encode()
	v2Blob := testSpecWithState("v2", false).Encode()
	for _, statement := range []string{
		`CREATE TABLE deployment_configs (
			deployment_id INTEGER PRIMARY KEY CHECK (deployment_id BETWEEN 1 AND 16777215),
			version INTEGER NOT NULL DEFAULT 0,
			node_id INTEGER NOT NULL DEFAULT -1,
			space_id INTEGER NOT NULL DEFAULT 1 CHECK (space_id BETWEEN 0 AND 65535),
			name TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL,
			updated_by INTEGER NOT NULL DEFAULT 0,
			spec_blob BLOB NOT NULL,
			deleted INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE UNIQUE INDEX idx_deployment_configs_active_node_identity
			ON deployment_configs(node_id, space_id, name) WHERE deleted = 0`,
		`CREATE TABLE deployment_config_history (
			deployment_id INTEGER NOT NULL,
			version INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			updated_by INTEGER NOT NULL DEFAULT 0,
			space_id INTEGER NOT NULL DEFAULT 1,
			node_id INTEGER NOT NULL DEFAULT 0,
			spec_blob BLOB NOT NULL,
			deleted INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (deployment_id, version)
		)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO deployment_configs
		(deployment_id, version, node_id, space_id, name, created_at, updated_at, updated_by, spec_blob)
		VALUES (7, 2, 1, 1, 'api', 1000, 2000, 5, ?)`, v2Blob); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO deployment_config_history
		(deployment_id, version, updated_at, updated_by, space_id, node_id, spec_blob)
		VALUES (7, 1, 1000, 4, 1, 1, ?)`, v1Blob); err != nil {
		t.Fatal(err)
	}
	// The current version is deliberately absent from history: the migration's
	// guard copy from the wide row must supply it.
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
		if got := cfg.UpdatedAt.UnixMilli(); got != 2000 || cfg.UpdatedBy != 5 {
			t.Fatalf("updated_at/by = %d/%d, want 2000/5", got, cfg.UpdatedBy)
		}
		history := store.MustFetchDeploymentHistory(7)
		if len(history) != 2 || history[0].Version != 1 || history[0].WorkloadVersion() != "v1" {
			t.Fatalf("migrated history = %+v", history)
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
		WHERE name = 'deployment_config_history'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("deployment_config_history was not dropped")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('deployment_configs')
		WHERE name IN ('version', 'created_at', 'updated_at', 'updated_by', 'spec_blob')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("retired deployment_configs columns were not dropped")
	}
}
