package state

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

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

// Every network-map input write must advance the counter, because the map's
// derived_from_seq stamp is read alongside the inputs: a write without a bump
// would let two different map contents share one stamp. Idempotent re-writes
// must not advance it, so restarts replay bootstrap without consuming history.
func TestGlobalSeqAdvancesOnNodeRegistryWrites(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	counter := func() int64 {
		t.Helper()
		_, _, seq := store.FetchNetworkMapInputs()
		return seq
	}

	before := counter()
	node := store.EnsurePrimaryNode("primary", "primary-id")
	if counter() <= before {
		t.Fatal("creating a node did not advance the global sequence")
	}
	before = counter()
	store.EnsurePrimaryNode("primary", "primary-id")
	if counter() != before {
		t.Fatal("re-ensuring an existing node consumed a sequence")
	}

	store.MustSetNodeAddresses(node.ID, []string{"192.0.2.1"})
	if counter() <= before {
		t.Fatal("changing a node underlay did not advance the global sequence")
	}
	before = counter()
	store.MustSetNodeAddresses(node.ID, []string{"192.0.2.1"})
	if counter() != before {
		t.Fatal("re-writing an unchanged underlay consumed a sequence")
	}

	if _, err := store.SetNodeAllowedSpaces(node.Identifier, []int32{1}); err != nil {
		t.Fatal(err)
	}
	if counter() <= before {
		t.Fatal("changing allowed spaces did not advance the global sequence")
	}

	before = counter()
	store.AllowSpaceOnAllNodes(7)
	if counter() <= before {
		t.Fatal("opening a space on all nodes did not advance the global sequence")
	}
	before = counter()
	store.AllowSpaceOnAllNodes(7)
	if counter() != before {
		t.Fatal("re-opening an already-open space consumed a sequence")
	}
}
