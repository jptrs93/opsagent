package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentIdentityIndexMigration(t *testing.T) {
	openers := map[string]func(string) *sql.DB{
		"primary":   func(path string) *sql.DB { return NewPrimaryStorage(path).db },
		"secondary": func(path string) *sql.DB { return NewSecondaryStorage(path).db },
	}
	for name, open := range openers {
		t.Run(name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), name+".db")
			seedLegacyDeploymentIdentityIndex(t, dbPath)
			db := open(dbPath)
			assertActiveDeploymentIdentityIndex(t, db)

			insertDeploymentIdentityRow(t, db, 1, 1)
			insertDeploymentIdentityRow(t, db, 2, 0)
			if _, err := db.Exec(`INSERT INTO deployment_configs
				(deployment_id, node_id, space_id, machine, name, created_at, version, updated_at, spec_blob, deleted)
				VALUES (3, 1, 1, 'node', 'app', 3, 1, 3, x'', 0)`); err == nil {
				t.Fatal("second active deployment with the same identity was accepted")
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			db = open(dbPath)
			defer db.Close()
			assertActiveDeploymentIdentityIndex(t, db)
		})
	}
}

func assertActiveDeploymentIdentityIndex(t *testing.T, db *sql.DB) {
	t.Helper()
	var indexSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_deployment_configs_active_identity'`).Scan(&indexSQL); err != nil {
		t.Fatalf("read active identity index: %v", err)
	}
	if !strings.Contains(strings.ToLower(indexSQL), "where deleted = 0") {
		t.Fatalf("active identity index = %q, want deleted-row predicate", indexSQL)
	}
	var legacyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_deployment_configs_identity'`).Scan(&legacyCount); err != nil {
		t.Fatal(err)
	}
	if legacyCount != 0 {
		t.Fatal("legacy deployment identity index still exists")
	}
}

func seedLegacyDeploymentIdentityIndex(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := db.Exec(`
		DROP INDEX idx_deployment_configs_active_identity;
		CREATE UNIQUE INDEX idx_deployment_configs_identity
		ON deployment_configs(space_id, machine, name);`); err != nil {
		t.Fatalf("seed legacy index: %v", err)
	}
}

func insertDeploymentIdentityRow(t *testing.T, db *sql.DB, id, deleted int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO deployment_configs
		(deployment_id, node_id, space_id, machine, name, created_at, version, updated_at, spec_blob, deleted)
		VALUES (?, 1, 1, 'node', 'app', ?, 1, ?, x'', ?)`, id, id, id, deleted); err != nil {
		t.Fatalf("insert deployment %d: %v", id, err)
	}
}
