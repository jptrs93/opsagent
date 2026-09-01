package state

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func TestDeploymentIdentityUniquenessIsGoLevel(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := Open(dbPath)
	defer store.Close()
	node := store.EnsurePrimaryNode("primary", "primary-id")
	spec := nonEmptySpec()
	store.MustCreateDeploymentForNode(apigen.Context{}, 1, "web", node.ID, spec)

	db := sqlitedb.MustOpen(dbPath)
	defer db.Close()
	var indexes int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index'
		AND tbl_name = 'deployments' AND name LIKE 'idx_%'`).Scan(&indexes); err != nil {
		t.Fatalf("count identity indexes: %v", err)
	}
	if indexes != 0 {
		t.Fatal("the SQL identity index still exists; uniqueness should be Go-level only")
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("duplicate active identity create did not panic")
			}
		}()
		store.MustCreateDeploymentForNode(apigen.Context{}, 1, "web", node.ID, spec)
	}()

	sibling := store.MustCreateDeploymentForNode(apigen.Context{}, 2, "web", node.ID, spec)
	if _, err := store.UpdateDeployment(apigen.Context{}, sibling.ID, DeploymentUpdate{
		ExpectedVersion: sibling.Version + 1,
		SpaceID:         i32ptr(1),
	}); !errors.Is(err, DuplicateDeploymentIdentityErr) {
		t.Fatalf("move onto occupied identity err = %v, want %v", err, DuplicateDeploymentIdentityErr)
	}

	store.DeleteDeployment(apigen.Context{}, sibling.ID, sibling.Version+1)
	recreated := store.MustCreateDeploymentForNode(apigen.Context{}, 2, "web", node.ID, spec)
	if recreated.ID == sibling.ID {
		t.Fatal("recreated deployment reused the deleted identity row")
	}
}
