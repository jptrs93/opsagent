package state

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func TestDeploymentIdentityHasNoSQLIndex(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := Open(dbPath)
	defer store.Close()
	node := store.EnsurePrimaryNode("primary", "primary-id")
	spec := nonEmptySpec()
	mustCreateDeploymentForNode(store, apigen.Context{}, 1, "web", node.ID, spec)

	db := sqlitedb.MustOpen(dbPath)
	defer db.Close()
	var indexes int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index'
		AND tbl_name = 'deployments' AND name LIKE 'idx_%'`).Scan(&indexes); err != nil {
		t.Fatalf("count identity indexes: %v", err)
	}
	if indexes != 0 {
		t.Fatal("the SQL identity index still exists; uniqueness is enforced in handler validation only")
	}

	sibling := mustCreateDeploymentForNode(store, apigen.Context{}, 2, "web", node.ID, spec)

	deleteDeployment(store, apigen.Context{}, sibling.ID)
	recreated := mustCreateDeploymentForNode(store, apigen.Context{}, 2, "web", node.ID, spec)
	if recreated.ID == sibling.ID {
		t.Fatal("recreated deployment reused the deleted identity row")
	}
}
