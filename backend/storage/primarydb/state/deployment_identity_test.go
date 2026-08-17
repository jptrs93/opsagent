package state

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func TestDeploymentIdentityIndex(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	if err := Open(dbPath).Close(); err != nil {
		t.Fatal(err)
	}
	db := sqlitedb.MustOpen(dbPath)
	assertNodeDeploymentIdentitySchema(t, db)
	insertDeploymentIdentityRow(t, db, 1, 1, 0)
	assertNodeDeploymentIdentityConstraint(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := Open(dbPath).Close(); err != nil {
		t.Fatal(err)
	}
	db = sqlitedb.MustOpen(dbPath)
	assertNodeDeploymentIdentitySchema(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertNodeDeploymentIdentitySchema(t *testing.T, db *sql.DB) {
	t.Helper()
	var indexSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_deployment_configs_active_node_identity'`).Scan(&indexSQL); err != nil {
		t.Fatalf("read node identity index: %v", err)
	}
	indexSQL = strings.ToLower(indexSQL)
	if !strings.Contains(indexSQL, "node_id, space_id, name") || !strings.Contains(indexSQL, "where deleted_at = 0") {
		t.Fatalf("node identity index = %q", indexSQL)
	}
}

func assertNodeDeploymentIdentityConstraint(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO deployment_configs
		(deployment_id, node_id, space_id, name)
		VALUES (2, 1, 1, 'app')`); err == nil {
		t.Fatal("second active deployment with the same node identity was accepted")
	}
	insertDeploymentIdentityRow(t, db, 3, 2, 0)
	insertDeploymentIdentityRow(t, db, 4, 1, 1)
	if _, err := db.Exec(`UPDATE deployment_configs SET deleted_at = 1755000000000 WHERE deployment_id = 1`); err != nil {
		t.Fatalf("delete active deployment: %v", err)
	}
	insertDeploymentIdentityRow(t, db, 5, 1, 0)
}

func insertDeploymentIdentityRow(t *testing.T, db *sql.DB, id, nodeID, deletedAt int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO deployment_configs
		(deployment_id, node_id, space_id, name, deleted_at)
		VALUES (?, ?, 1, 'app', ?)`, id, nodeID, deletedAt); err != nil {
		t.Fatalf("insert deployment %d: %v", id, err)
	}
}
