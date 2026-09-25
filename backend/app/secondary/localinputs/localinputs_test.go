package localinputs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/machinekey"
	"github.com/jptrs93/opsagent/backend/storage/secondarydb/state"
)

// memDB is an in-memory stand-in for the local_runtime_inputs table.
type memDB struct {
	rows map[[3]int64]state.LocalRuntimeInput
}

func newMemDB() *memDB { return &memDB{rows: map[[3]int64]state.LocalRuntimeInput{}} }

func (m *memDB) ListLocalRuntimeInputs() []state.LocalRuntimeInput {
	out := make([]state.LocalRuntimeInput, 0, len(m.rows))
	for _, row := range m.rows {
		out = append(out, row)
	}
	return out
}

func (m *memDB) UpsertLocalRuntimeInput(row state.LocalRuntimeInput) {
	m.rows[[3]int64{row.Kind, row.RefID, row.RefVersion}] = row
}

func (m *memDB) DeleteLocalRuntimeInput(kind, refID, refVersion int64) {
	delete(m.rows, [3]int64{kind, refID, refVersion})
}

func openStore(t *testing.T, db DB, dir string) *Store {
	t.Helper()
	store, err := Open(context.Background(), db, &machinekey.File{Path: filepath.Join(dir, machinekey.FileName)})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store
}

func TestStoreAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	db := newMemDB()
	store := openStore(t, db, dir)

	if err := store.StoreRuntimeInputs(map[apigen.ValueRef]string{vr(7): "s3cret"}, map[apigen.ValueRef]string{vr(9): "conf"}); err != nil {
		t.Fatalf("StoreRuntimeInputs: %v", err)
	}

	// A second Open models a restart: it must reuse the established key file
	// rather than minting a new one, or every stored value would be lost.
	reopened := openStore(t, db, dir)
	secrets, configs, err := reopened.LoadRuntimeInputs()
	if err != nil {
		t.Fatalf("LoadRuntimeInputs: %v", err)
	}
	if secrets[vr(7)] != "s3cret" {
		t.Fatalf("secret 7 = %q, want s3cret", secrets[vr(7)])
	}
	if configs[vr(9)] != "conf" {
		t.Fatalf("config 9 = %q, want conf", configs[vr(9)])
	}
}

// The value must not be readable from the database alone, or persisting it
// locally would be strictly worse than the in-memory-only behaviour it replaces.
func TestStoredValueIsNotPlaintextOnDisk(t *testing.T) {
	db := newMemDB()
	store := openStore(t, db, t.TempDir())

	if err := store.StoreRuntimeInputs(map[apigen.ValueRef]string{vr(7): "s3cret"}, nil); err != nil {
		t.Fatalf("StoreRuntimeInputs: %v", err)
	}
	for _, row := range db.ListLocalRuntimeInputs() {
		if string(row.Ciphertext) == "s3cret" {
			t.Fatal("value was stored in plaintext")
		}
	}
}

// The kind, id, and version are bound as associated data, so a row lifted into
// another slot must not decrypt there.
func TestCiphertextIsBoundToItsKindIDAndVersion(t *testing.T) {
	db := newMemDB()
	store := openStore(t, db, t.TempDir())

	if err := store.StoreRuntimeInputs(map[apigen.ValueRef]string{vr(7): "s3cret"}, nil); err != nil {
		t.Fatalf("StoreRuntimeInputs: %v", err)
	}
	row := db.rows[[3]int64{state.LocalRuntimeInputKindSecret, 7, 1}]

	moved := row
	moved.RefID = 8
	db.UpsertLocalRuntimeInput(moved)
	bumped := row
	bumped.RefVersion = 2
	db.UpsertLocalRuntimeInput(bumped)
	reinterpreted := row
	reinterpreted.Kind = state.LocalRuntimeInputKindConfig
	db.UpsertLocalRuntimeInput(reinterpreted)

	secrets, configs, err := store.LoadRuntimeInputs()
	if err != nil {
		t.Fatalf("LoadRuntimeInputs: %v", err)
	}
	if _, ok := secrets[vr(8)]; ok {
		t.Fatal("a ciphertext moved to another id decrypted")
	}
	if _, ok := secrets[apigen.ValueRef{ID: 7, Version: 2}]; ok {
		t.Fatal("a ciphertext moved to another version decrypted")
	}
	if _, ok := configs[vr(7)]; ok {
		t.Fatal("a secret ciphertext decrypted as a config")
	}
	if secrets[vr(7)] != "s3cret" {
		t.Fatalf("untouched row did not survive: %q", secrets[vr(7)])
	}
}

// Losing the machine key is recoverable here in a way it is not on the primary:
// the primary can refetch nothing, but a secondary can refetch everything. So the
// unreadable rows must be dropped rather than wedging startup.
func TestLoadDropsRowsSealedUnderASupersededKey(t *testing.T) {
	dir := t.TempDir()
	db := newMemDB()
	store := openStore(t, db, dir)
	if err := store.StoreRuntimeInputs(map[apigen.ValueRef]string{vr(7): "s3cret"}, nil); err != nil {
		t.Fatalf("StoreRuntimeInputs: %v", err)
	}

	if err := os.Remove(filepath.Join(dir, machinekey.FileName)); err != nil {
		t.Fatalf("removing machine key: %v", err)
	}
	rekeyed := openStore(t, db, dir)

	secrets, _, err := rekeyed.LoadRuntimeInputs()
	if err != nil {
		t.Fatalf("LoadRuntimeInputs after rekey: %v", err)
	}
	if len(secrets) != 0 {
		t.Fatalf("secrets = %v, want none", secrets)
	}
	if len(db.rows) != 0 {
		t.Fatalf("undecryptable rows were left behind: %v", db.rows)
	}
}

func TestRetainRemovesUnreferencedRows(t *testing.T) {
	db := newMemDB()
	store := openStore(t, db, t.TempDir())
	if err := store.StoreRuntimeInputs(map[apigen.ValueRef]string{vr(1): "a", vr(2): "b"}, map[apigen.ValueRef]string{vr(3): "c"}); err != nil {
		t.Fatalf("StoreRuntimeInputs: %v", err)
	}

	removed, err := store.RetainRuntimeInputs(map[apigen.ValueRef]struct{}{vr(1): {}}, map[apigen.ValueRef]struct{}{})
	if err != nil {
		t.Fatalf("RetainRuntimeInputs: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	secrets, configs, err := store.LoadRuntimeInputs()
	if err != nil {
		t.Fatalf("LoadRuntimeInputs: %v", err)
	}
	if len(secrets) != 1 || secrets[vr(1)] != "a" {
		t.Fatalf("secrets = %v, want only id 1", secrets)
	}
	if len(configs) != 0 {
		t.Fatalf("configs = %v, want none", configs)
	}
}

func vr(id int32) apigen.ValueRef { return apigen.ValueRef{ID: id, Version: 1} }
