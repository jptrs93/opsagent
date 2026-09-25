package sq

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func TestOpenDropsRowIDRuntimeInputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secondary.db")
	legacy := sqlitedb.MustOpen(path)
	if _, err := legacy.Exec(`CREATE TABLE local_runtime_inputs (
		kind INTEGER NOT NULL, ref_id INTEGER NOT NULL, ciphertext BLOB NOT NULL,
		nonce BLOB NOT NULL, fetched_at INTEGER NOT NULL, PRIMARY KEY (kind, ref_id))`); err != nil {
		t.Fatalf("creating legacy table: %v", err)
	}
	if _, err := legacy.Exec(`INSERT INTO local_runtime_inputs VALUES (1, 42, x'00', x'00', 1)`); err != nil {
		t.Fatalf("seeding legacy row: %v", err)
	}
	legacy.Close()

	q := Open(path)
	ctx := context.Background()
	rows, err := q.ListLocalRuntimeInputs(ctx)
	if err != nil {
		t.Fatalf("ListLocalRuntimeInputs: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("legacy rows survived: %v", rows)
	}
	if err := q.UpsertLocalRuntimeInput(ctx, UpsertLocalRuntimeInputParams{Kind: 1, RefID: 42, RefVersion: 3, Ciphertext: []byte{1}, Nonce: []byte{2}, FetchedAt: 1}); err != nil {
		t.Fatalf("UpsertLocalRuntimeInput: %v", err)
	}
	q.Close()

	reopened := Open(path)
	defer reopened.Close()
	rows, err = reopened.ListLocalRuntimeInputs(ctx)
	if err != nil {
		t.Fatalf("ListLocalRuntimeInputs after reopen: %v", err)
	}
	if len(rows) != 1 || rows[0].RefVersion != 3 {
		t.Fatalf("rows after reopen = %v, want the paired row kept", rows)
	}
}
