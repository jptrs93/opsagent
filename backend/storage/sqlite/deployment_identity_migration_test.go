package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentIdentityIndex(t *testing.T) {
	openers := map[string]func(string) *sql.DB{
		"primary":   func(path string) *sql.DB { return NewPrimaryStorage(path).db },
		"secondary": func(path string) *sql.DB { return NewSecondaryStorage(path).db },
	}
	for role, open := range openers {
		t.Run(role, func(t *testing.T) {
			for _, legacy := range []bool{false, true} {
				name := "fresh"
				if legacy {
					name = "migrated"
				}
				t.Run(name, func(t *testing.T) {
					dbPath := filepath.Join(t.TempDir(), role+".db")
					if legacy {
						seedMachineIdentitySchema(t, dbPath)
					}

					db := open(dbPath)
					assertNodeDeploymentIdentitySchema(t, db)
					if legacy {
						var nodeID, spaceID int
						var deploymentName string
						if err := db.QueryRow(`SELECT node_id, space_id, name FROM deployment_configs WHERE deployment_id = 1`).Scan(&nodeID, &spaceID, &deploymentName); err != nil {
							t.Fatalf("read migrated deployment: %v", err)
						}
						if nodeID != 1 || spaceID != 1 || deploymentName != "app" {
							t.Fatalf("migrated deployment = (%d, %d, %q), want (1, 1, %q)", nodeID, spaceID, deploymentName, "app")
						}
					} else {
						insertDeploymentIdentityRow(t, db, 1, 1, 0)
					}
					assertNodeDeploymentIdentityConstraint(t, db)
					if err := db.Close(); err != nil {
						t.Fatal(err)
					}

					db = open(dbPath)
					assertNodeDeploymentIdentitySchema(t, db)
					if err := db.Close(); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}

func seedMachineIdentitySchema(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE deployment_configs (
			deployment_id INTEGER PRIMARY KEY,
			node_id INTEGER NOT NULL DEFAULT -1,
			space_id INTEGER NOT NULL DEFAULT 1,
			machine TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT 0,
			version INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL,
			updated_by INTEGER NOT NULL DEFAULT 0,
			spec_blob BLOB NOT NULL,
			desired_version TEXT NOT NULL DEFAULT '',
			desired_running INTEGER NOT NULL DEFAULT 0,
			deleted INTEGER NOT NULL DEFAULT 0
		);
		CREATE UNIQUE INDEX idx_deployment_configs_active_identity
			ON deployment_configs(space_id, machine, name)
			WHERE deleted = 0;
		INSERT INTO deployment_configs
			(deployment_id, node_id, space_id, machine, name, updated_at, spec_blob)
			VALUES (1, 1, 1, 'node-1', 'app', 1, x'');
	`); err != nil {
		db.Close()
		t.Fatalf("seed machine identity schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertNodeDeploymentIdentitySchema(t *testing.T, db *sql.DB) {
	t.Helper()
	if tableHasColumn(db, "deployment_configs", "machine") {
		t.Fatal("deployment_configs.machine still exists")
	}

	var indexSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_deployment_configs_active_node_identity'`).Scan(&indexSQL); err != nil {
		t.Fatalf("read node identity index: %v", err)
	}
	indexSQL = strings.ToLower(indexSQL)
	if !strings.Contains(indexSQL, "node_id, space_id, name") || !strings.Contains(indexSQL, "where deleted = 0") {
		t.Fatalf("node identity index = %q", indexSQL)
	}

	var legacyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name IN ('idx_deployment_configs_identity', 'idx_deployment_configs_active_identity')`).Scan(&legacyCount); err != nil {
		t.Fatal(err)
	}
	if legacyCount != 0 {
		t.Fatal("machine-based deployment identity index still exists")
	}
}

func assertNodeDeploymentIdentityConstraint(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO deployment_configs
		(deployment_id, node_id, space_id, name, updated_at, spec_blob)
		VALUES (2, 1, 1, 'app', 2, x'')`); err == nil {
		t.Fatal("second active deployment with the same node identity was accepted")
	}
	insertDeploymentIdentityRow(t, db, 3, 2, 0)
	insertDeploymentIdentityRow(t, db, 4, 1, 1)
	if _, err := db.Exec(`UPDATE deployment_configs SET deleted = 1 WHERE deployment_id = 1`); err != nil {
		t.Fatalf("delete active deployment: %v", err)
	}
	insertDeploymentIdentityRow(t, db, 5, 1, 0)
}

func insertDeploymentIdentityRow(t *testing.T, db *sql.DB, id, nodeID, deleted int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO deployment_configs
		(deployment_id, node_id, space_id, name, updated_at, spec_blob, deleted)
		VALUES (?, ?, 1, 'app', ?, x'', ?)`, id, nodeID, id, deleted); err != nil {
		t.Fatalf("insert deployment %d: %v", id, err)
	}
}
