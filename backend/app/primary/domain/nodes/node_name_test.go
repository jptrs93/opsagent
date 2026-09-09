package nodes

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func TestNodeNameUniquenessEnforcedInGo(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	EnsurePrimaryNode(store, "primary", "primary-id")
	EnsurePrimaryNode(store, "worker", "worker-id")

	if _, err := RenameNode(store, "worker-id", "primary"); !errors.Is(err, ErrDuplicateNodeName) {
		t.Fatalf("rename collision error = %v, want ErrDuplicateNodeName", err)
	}
	if node, err := RenameNode(store, "worker-id", "worker"); err != nil || node.Value.Operator.Name != "worker" {
		t.Fatalf("same-name rename = %+v, %v", node, err)
	}
	if _, err := RenameNode(store, "missing-id", "anything"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing node rename error = %v, want sql.ErrNoRows", err)
	}

	req, expectedVersion := UpsertEnrollmentRequest(store, "127.0.0.1", "v1", apigen.NodeReported{Identifier: "new-id", UnderlayAddress: "10.0.0.9", WgPublicKey: ""})
	if _, err := AcceptEnrollmentRequest(store, req.ID, "primary", req.RequestingMachineID, expectedVersion); !errors.Is(err, ErrDuplicateNodeName) {
		t.Fatalf("accept collision error = %v, want ErrDuplicateNodeName", err)
	}
	if _, err := AcceptEnrollmentRequest(store, req.ID, "fresh-name", req.RequestingMachineID, expectedVersion); err != nil {
		t.Fatalf("accept after collision: %v", err)
	}
}
