package state

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestNodeNameUniquenessEnforcedInGo(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	store.EnsurePrimaryNode("primary", "primary-id")
	store.EnsurePrimaryNode("worker", "worker-id")

	if _, err := store.RenameNode("worker-id", "primary"); !errors.Is(err, ErrDuplicateNodeName) {
		t.Fatalf("rename collision error = %v, want ErrDuplicateNodeName", err)
	}
	if node, err := store.RenameNode("worker-id", "worker"); err != nil || node.Name != "worker" {
		t.Fatalf("same-name rename = %+v, %v", node, err)
	}
	if _, err := store.RenameNode("missing-id", "anything"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing node rename error = %v, want sql.ErrNoRows", err)
	}

	req, expectedVersion := store.MustUpsertEnrollmentRequest("127.0.0.1", "new-id", "v1", "10.0.0.9", "")
	if _, err := store.AcceptEnrollmentRequest(req.ID, "primary", req.RequestingMachineID, req.UnderlayAddress, "", expectedVersion); !errors.Is(err, ErrDuplicateNodeName) {
		t.Fatalf("accept collision error = %v, want ErrDuplicateNodeName", err)
	}
	if _, err := store.AcceptEnrollmentRequest(req.ID, "fresh-name", req.RequestingMachineID, req.UnderlayAddress, "", expectedVersion); err != nil {
		t.Fatalf("accept after collision: %v", err)
	}
}
