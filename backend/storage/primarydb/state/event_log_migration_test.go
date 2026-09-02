package state

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/lib/machinekey"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func TestMigratedSecretDecryptsThroughManager(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "primary.db")

	smk := make([]byte, machinekey.KeyLen)
	if _, err := rand.Read(smk); err != nil {
		t.Fatal(err)
	}
	kek, err := (&machinekey.File{Path: filepath.Join(dir, machinekey.FileName)}).Establish()
	if err != nil {
		t.Fatal(err)
	}
	wrapped, slotNonce, err := machinekey.Seal(kek, smk, []byte("opendeploy-keyslot:machine"))
	if err != nil {
		t.Fatal(err)
	}
	ct, nonce, err := machinekey.Seal(smk, []byte("hunter2"), []byte("opendeploy-secret:user:s3:v1"))
	if err != nil {
		t.Fatal(err)
	}

	db := sqlitedb.MustOpen(dbPath)
	mustSeed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	mustSeed(`CREATE TABLE secrets (
		id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL,
		value_directory_id INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL, deleted_at INTEGER NOT NULL DEFAULT 0)`)
	mustSeed(`CREATE TABLE secret_spaces (
		id INTEGER PRIMARY KEY AUTOINCREMENT, secret_id INTEGER NOT NULL,
		author INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL,
		space_id INTEGER NOT NULL, global_seq INTEGER NOT NULL DEFAULT 0)`)
	mustSeed(`CREATE TABLE secret_versions (
		id INTEGER PRIMARY KEY AUTOINCREMENT, secret_id INTEGER NOT NULL,
		version INTEGER NOT NULL, smk_version INTEGER NOT NULL,
		ciphertext BLOB NOT NULL, nonce BLOB NOT NULL,
		created_at INTEGER NOT NULL, author INTEGER NOT NULL DEFAULT 0,
		global_seq INTEGER NOT NULL DEFAULT 0, UNIQUE (secret_id, version))`)
	mustSeed(`CREATE TABLE secret_keyslots (
		slot TEXT PRIMARY KEY, smk_version INTEGER NOT NULL,
		wrapped_smk BLOB NOT NULL, nonce BLOB NOT NULL, kdf_salt BLOB,
		created_at INTEGER NOT NULL)`)
	mustSeed(`INSERT INTO secret_keyslots (slot, smk_version, wrapped_smk, nonce, created_at)
		VALUES ('machine', 1, ?, ?, 1000)`, wrapped, slotNonce)
	mustSeed(`INSERT INTO secrets (id, name, created_at) VALUES (3, 'db-password', 1000)`)
	mustSeed(`INSERT INTO secret_spaces (secret_id, created_at, space_id, global_seq) VALUES (3, 1000, 1, 10)`)
	mustSeed(`INSERT INTO secret_versions (id, secret_id, version, smk_version, ciphertext, nonce, created_at, global_seq)
		VALUES (7, 3, 1, 1, ?, ?, 1000, 10)`, ct, nonce)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store := Open(dbPath)
	defer store.Close()
	mgr, err := secrets.Open(dir, store)
	if err != nil {
		t.Fatal(err)
	}
	value, err := mgr.RevealByID(7)
	if err != nil {
		t.Fatalf("migrated secret does not decrypt through the Manager: %v", err)
	}
	if string(value) != "hunter2" {
		t.Fatalf("migrated secret plaintext = %q, want hunter2", value)
	}
	meta, ok := mgr.MetaByID(7)
	if !ok || meta.SecretID != 3 || meta.Version != 1 || meta.Name != "db-password" {
		t.Fatalf("migrated secret meta = %+v ok=%v", meta, ok)
	}
}

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
