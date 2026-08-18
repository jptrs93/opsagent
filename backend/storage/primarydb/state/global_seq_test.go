package state

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func TestDeploymentsTableRenameMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	db := sqlitedb.MustOpen(dbPath)
	mustExec := func(stmt string, args ...any) {
		t.Helper()
		if _, err := db.Exec(stmt, args...); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	mustExec(`CREATE TABLE deployment_configs (
	    deployment_id INTEGER PRIMARY KEY CHECK (deployment_id BETWEEN 1 AND 16777215),
	    node_id       INTEGER NOT NULL DEFAULT -1,
	    name          TEXT    NOT NULL DEFAULT '',
	    deleted_at    INTEGER NOT NULL DEFAULT 0
	)`)
	mustExec(`INSERT INTO deployment_configs (deployment_id, node_id, name, deleted_at) VALUES (1, 1, 'web', 0)`)
	mustExec(`INSERT INTO deployment_configs (deployment_id, node_id, name, deleted_at) VALUES (2, 1, 'old', 123)`)
	spec := nonEmptySpec().Encode()
	mustExec(`CREATE TABLE deployment_versions (
	    id INTEGER PRIMARY KEY AUTOINCREMENT, deployment_id INTEGER NOT NULL,
	    version INTEGER NOT NULL, created_at INTEGER NOT NULL,
	    author INTEGER NOT NULL DEFAULT 0, spec_blob BLOB NOT NULL,
	    UNIQUE (deployment_id, version)
	)`)
	mustExec(`INSERT INTO deployment_versions (deployment_id, version, created_at, author, spec_blob) VALUES (1, 1, 5, 0, ?)`, spec)
	mustExec(`INSERT INTO deployment_versions (deployment_id, version, created_at, author, spec_blob) VALUES (2, 1, 5, 0, ?)`, spec)
	mustExec(`CREATE TABLE deployment_space_versions (
	    id INTEGER PRIMARY KEY AUTOINCREMENT, deployment_id INTEGER NOT NULL,
	    version INTEGER NOT NULL, author INTEGER NOT NULL DEFAULT 0,
	    created_at INTEGER NOT NULL, space_id INTEGER NOT NULL
	)`)
	mustExec(`INSERT INTO deployment_space_versions (deployment_id, version, author, created_at, space_id) VALUES (1, 1, 0, 5, 1)`)
	mustExec(`INSERT INTO deployment_space_versions (deployment_id, version, author, created_at, space_id) VALUES (2, 1, 0, 5, 1)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store := Open(dbPath)
	if cfg := store.configCache[1]; cfg == nil || cfg.Name != "web" || cfg.Deleted {
		t.Fatalf("migrated deployment 1 = %+v, want live 'web'", store.configCache[1])
	}
	if cfg := store.configCache[2]; cfg == nil || !cfg.Deleted {
		t.Fatalf("migrated deployment 2 = %+v, want tombstone", store.configCache[2])
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db = sqlitedb.MustOpen(dbPath)
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM deployments`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("deployments row count = %d, want 2", count)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'deployment_configs'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("deployment_configs table survived the rename migration")
	}
}

func TestGlobalSeqStampsVersionWrites(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := Open(dbPath)
	node := testNode(store, "primary")
	cfg := store.MustCreateDeploymentForNode(apigen.Context{}, 1, "api", node.ID, testSpecWithState("v1", false))
	store.MustSetDeploymentWorkloadState(apigen.Context{}, cfg.ID, "v2", false)
	if _, err := store.MoveDeploymentSpace(cfg.ID, 2, cfg.SpaceVersion+1, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db := sqlitedb.MustOpen(dbPath)
	defer db.Close()
	readSeqs := func(query string) []int64 {
		t.Helper()
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []int64
		for rows.Next() {
			var seq int64
			if err := rows.Scan(&seq); err != nil {
				t.Fatal(err)
			}
			out = append(out, seq)
		}
		return out
	}

	versionSeqs := readSeqs(`SELECT global_seq FROM deployment_versions WHERE deployment_id = ` + itoa(cfg.ID) + ` ORDER BY version`)
	if len(versionSeqs) != 2 || versionSeqs[0] <= 0 || versionSeqs[1] <= versionSeqs[0] {
		t.Fatalf("deployment version seqs = %v, want two increasing positive values", versionSeqs)
	}
	spaceSeqs := readSeqs(`SELECT global_seq FROM deployment_space_versions WHERE deployment_id = ` + itoa(cfg.ID) + ` ORDER BY version`)
	if len(spaceSeqs) != 2 || spaceSeqs[0] != versionSeqs[0] || spaceSeqs[1] <= versionSeqs[1] {
		t.Fatalf("space version seqs = %v, want creation seq %d shared then a later value", spaceSeqs, versionSeqs[0])
	}

	var counter int64
	if err := db.QueryRow(`SELECT value FROM global_seq WHERE id = 1`).Scan(&counter); err != nil {
		t.Fatal(err)
	}
	if counter < spaceSeqs[1] {
		t.Fatalf("global_seq counter = %d, below max stamped value %d", counter, spaceSeqs[1])
	}
}

func itoa(v int32) string {
	return fmt.Sprintf("%d", v)
}
