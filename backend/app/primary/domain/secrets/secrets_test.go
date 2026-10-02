package secrets

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/lib/machinekey"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func mustOpen(t *testing.T, dir string, store *state.Service) *Manager {
	t.Helper()
	var mgr *Manager
	var err error
	if len(listKeyslots(store.Queries())) == 0 {
		mgr, err = Initialize(dir, store)
	} else {
		mgr, err = Open(dir, store)
	}
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return mgr
}

func recordByRef(t *testing.T, store *state.Service, ref apigen.ValueRef) Record {
	t.Helper()
	for _, r := range ListVersionRecords(store.Queries()) {
		if r.ref() == ref {
			return r
		}
	}
	t.Fatalf("secret version %s not found", ref)
	return Record{}
}

func TestOpenRejectsUninitializedStore(t *testing.T) {
	if _, err := Open(t.TempDir(), openTestStore(t)); err == nil {
		t.Fatal("Open succeeded for an uninitialized secrets store")
	}
}

func TestCreateResolveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t)
	mgr := mustOpen(t, dir, store)

	meta, err := mgr.Create("staging.db.password", []byte("hunter2"), 7, 0, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if meta.SecretID == 0 || meta.Version != 1 || meta.Author != 7 {
		t.Fatalf("meta = %+v", meta)
	}
	got, ok := mgr.Resolve(meta.Ref())
	if !ok || got != "hunter2" {
		t.Fatalf("Resolve = %q, %v; want hunter2, true", got, ok)
	}
	if m, ok := mgr.MetaByRef(meta.Ref()); !ok || m.Name != "staging.db.password" || m.SecretID != meta.SecretID {
		t.Fatalf("MetaByRef = %+v, %v", m, ok)
	}
	info, err := os.Stat(filepath.Join(dir, machinekey.FileName))
	if err != nil {
		t.Fatalf("stat machine.key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("machine.key perm = %o; want 600", perm)
	}
}

func TestReopenWithMachineKeyUnlocks(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t)
	mgr := mustOpen(t, dir, store)
	meta, err := mgr.Create("k", []byte("v"), 0, 0, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	mgr2 := mustOpen(t, dir, store)
	if unlocked, _ := mgr2.Status(); !unlocked {
		t.Fatal("expected reopened store to be unlocked")
	}
	if got, ok := mgr2.Resolve(meta.Ref()); !ok || got != "v" {
		t.Fatalf("Resolve after reopen = %q, %v", got, ok)
	}
}

func TestCiphertextAtRestNotPlaintext(t *testing.T) {
	store := openTestStore(t)
	mgr := mustOpen(t, t.TempDir(), store)
	meta, err := mgr.Create("k", []byte("super-secret-value"), 0, 0, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if string(recordByRef(t, store, meta.Ref()).Ciphertext) == "super-secret-value" {
		t.Fatal("value stored in plaintext")
	}
}

func TestAADBindingPreventsSwap(t *testing.T) {
	store := openTestStore(t)
	mgr := mustOpen(t, t.TempDir(), store)
	metaA, err := mgr.Create("a", []byte("value-a"), 0, 0, 0)
	if err != nil {
		t.Fatalf("Create a: %v", err)
	}
	metaB, err := mgr.Create("b", []byte("value-b"), 0, 0, 0)
	if err != nil {
		t.Fatalf("Create b: %v", err)
	}
	recA := recordByRef(t, store, metaA.Ref())
	if _, err := aeadOpen(mgr.smk, recA.Ciphertext, recA.Nonce, secretAAD(metaB.SecretID)); err == nil {
		t.Fatal("ciphertext of one secret opened under another secret's identity")
	}
	if pt, err := aeadOpen(mgr.smk, recA.Ciphertext, recA.Nonce, secretAAD(metaA.SecretID)); err != nil || string(pt) != "value-a" {
		t.Fatalf("own identity = %q, %v", pt, err)
	}
}

func TestRenameIsMetadataOnly(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t)
	mgr := mustOpen(t, dir, store)
	first, err := mgr.Create("db.password", []byte("one"), 0, 0, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	second, err := mgr.SetWithDeploymentUpdates(first.SecretID, []byte("two"), 0, false, nil, nil)
	if err != nil {
		t.Fatalf("Set second: %v", err)
	}
	beforeCiphertext := string(recordByRef(t, store, first.Ref()).Ciphertext)
	if err := mgr.Rename(first.SecretID, "prod.db.password"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if got := string(recordByRef(t, store, first.Ref()).Ciphertext); got != beforeCiphertext {
		t.Fatal("rename re-encrypted a version; the id-bound AAD makes that unnecessary")
	}
	if got, ok := mgr.Resolve(first.Ref()); !ok || got != "one" {
		t.Fatalf("Resolve first after rename = %q, %v; want one, true", got, ok)
	}
	if got, ok := mgr.Resolve(second.Ref()); !ok || got != "two" {
		t.Fatalf("Resolve second after rename = %q, %v; want two, true", got, ok)
	}
	mgr2 := mustOpen(t, dir, store)
	if got, ok := mgr2.Resolve(first.Ref()); !ok || got != "one" {
		t.Fatalf("Resolve first after reopen = %q, %v; want one, true", got, ok)
	}
	if m, ok := mgr2.MetaByRef(second.Ref()); !ok || m.Name != "prod.db.password" || m.Ref() != second.Ref() {
		t.Fatalf("MetaByRef after reopen = %+v, %v", m, ok)
	}
}

func TestSystemSecretsLiveInSpaceZero(t *testing.T) {
	store := openTestStore(t)
	mgr := mustOpen(t, t.TempDir(), store)

	if err := mgr.SetInternal("opendeploy.cluster.ca.key", []byte("ca-key")); err != nil {
		t.Fatalf("SetInternal: %v", err)
	}
	records := ListVersionRecords(store.Queries())
	if len(records) != 1 || records[0].SpaceID != 0 || records[0].Author != 0 || records[0].Name != "opendeploy.cluster.ca.key" {
		t.Fatalf("system secret rows = %+v, want one space 0 row authored by the system", records)
	}
	ref := apigen.ValueRef{ID: records[0].SecretID, Version: 1}
	if _, ok := mgr.Resolve(ref); ok {
		t.Fatal("Resolve exposed a space 0 secret")
	}
	if _, err := mgr.RevealByRef(ref); err != ErrNotFound {
		t.Fatalf("RevealByRef on a space 0 secret err = %v; want ErrNotFound", err)
	}
	if _, ok := mgr.MetaByRef(ref); ok {
		t.Fatal("MetaByRef exposed a space 0 secret")
	}
	got, err := mgr.RevealInternal("opendeploy.cluster.ca.key")
	if err != nil || string(got) != "ca-key" {
		t.Fatalf("RevealInternal = %q, %v; want ca-key, nil", got, err)
	}
	if err := mgr.SetInternal("opendeploy.cluster.ca.key", []byte("ca-key-2")); err != nil {
		t.Fatalf("SetInternal again: %v", err)
	}
	if got, err := mgr.RevealInternal("opendeploy.cluster.ca.key"); err != nil || string(got) != "ca-key-2" {
		t.Fatalf("RevealInternal after second write = %q, %v", got, err)
	}
	if records := ListVersionRecords(store.Queries()); len(records) != 2 {
		t.Fatalf("second SetInternal did not append a version: %+v", records)
	}
	if _, err := mgr.Create("opendeploy.cluster.ca.key", []byte("user"), 0, 0, 0); err != ErrReservedName {
		t.Fatalf("Create reserved name err = %v; want ErrReservedName", err)
	}
	if _, err := mgr.Create("opendeploy.config.github_token", []byte("user"), 0, 0, 0); err != nil {
		t.Fatalf("Create opendeploy config secret err = %v; want nil", err)
	}
	if _, err := mgr.Create("opendeploy.tls.pem", []byte("tls"), 0, 0, 0); err != nil {
		t.Fatalf("Create initial TLS cert secret err = %v; want nil", err)
	}
}

func TestRevealInternalLoadsFromStoreAfterReopen(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t)
	mgr := mustOpen(t, dir, store)
	if err := mgr.SetInternal("opendeploy.cluster.primary.key", []byte("primary-key")); err != nil {
		t.Fatalf("SetInternal: %v", err)
	}
	mgr2 := mustOpen(t, dir, store)
	got, err := mgr2.RevealInternal("opendeploy.cluster.primary.key")
	if err != nil || string(got) != "primary-key" {
		t.Fatalf("RevealInternal after reopen = %q, %v; want primary-key, nil", got, err)
	}
}

func TestRecoveryUnlockOnFreshMachine(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t)
	mgr := mustOpen(t, dir, store)
	meta, err := mgr.Create("k", []byte("v"), 0, 0, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	code, err := mgr.GenerateRecoveryCode(0)
	if err != nil {
		t.Fatalf("GenerateRecoveryCode: %v", err)
	}
	if _, rc := mgr.Status(); !rc {
		t.Fatal("recovery should be configured")
	}
	freshDir := t.TempDir()
	mgr2 := mustOpen(t, freshDir, store)
	if unlocked, _ := mgr2.Status(); unlocked {
		t.Fatal("fresh machine without machine.key should be locked")
	}
	if _, ok := mgr2.Resolve(meta.Ref()); ok {
		t.Fatal("locked store must not resolve secrets")
	}
	if _, err := mgr2.Create("x", []byte("y"), 0, 0, 0); err == nil {
		t.Fatal("locked store must reject Create")
	}
	if err := mgr2.Rename(meta.SecretID, "renamed"); err != ErrLocked {
		t.Fatalf("locked store rename err = %v; want ErrLocked", err)
	}
	if err := mgr2.Unlock("AAAAA-BBBBB-CCCCC", 0); err == nil {
		t.Fatal("expected wrong recovery code to fail")
	}
	if err := mgr2.Unlock(code, 0); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if got, ok := mgr2.Resolve(meta.Ref()); !ok || got != "v" {
		t.Fatalf("Resolve after recovery = %q, %v", got, ok)
	}
	if _, err := os.Stat(filepath.Join(freshDir, machinekey.FileName)); err != nil {
		t.Fatalf("machine.key not re-established: %v", err)
	}
	mgr3 := mustOpen(t, freshDir, store)
	if unlocked, _ := mgr3.Status(); !unlocked {
		t.Fatal("expected unattended unlock after recovery")
	}
}

func TestCodeFormattingTolerated(t *testing.T) {
	store := openTestStore(t)
	mgr := mustOpen(t, t.TempDir(), store)
	_, _ = mgr.Create("k", []byte("v"), 0, 0, 0)
	code, err := mgr.GenerateRecoveryCode(0)
	if err != nil {
		t.Fatalf("GenerateRecoveryCode: %v", err)
	}
	mgr2 := mustOpen(t, t.TempDir(), store)
	munged := normalizeCode(code)
	spaced := ""
	for i, c := range munged {
		if i > 0 && i%4 == 0 {
			spaced += " "
		}
		spaced += string(c)
	}
	if err := mgr2.Unlock(toLower(spaced), 0); err != nil {
		t.Fatalf("Unlock with reformatted code: %v", err)
	}
}

func toLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func TestKeyslotsAreNodeKeyedEvents(t *testing.T) {
	dir := t.TempDir()
	store := state.Open(filepath.Join(dir, "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	primary := nodes.EnsurePrimaryNode(store, "primary", "primary-id")
	mgr, err := Initialize(dir, store)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	slots := listKeyslots(store.Queries())
	machine, ok := findSlot(slots, slotMachine, primary.ID)
	if !ok || len(slots) != 1 {
		t.Fatalf("after Initialize slots = %+v; want one machine slot for node %d", slots, primary.ID)
	}
	if machine.NodeID != primary.ID || machine.KDFSalt != nil {
		t.Fatalf("machine slot = %+v", machine)
	}
	if _, err := mgr.GenerateRecoveryCode(7); err != nil {
		t.Fatalf("GenerateRecoveryCode: %v", err)
	}
	if _, err := mgr.GenerateRecoveryCode(7); err != nil {
		t.Fatalf("GenerateRecoveryCode again: %v", err)
	}
	if _, ok := findSlot(listKeyslots(store.Queries()), slotRecovery, 0); !ok {
		t.Fatal("recovery slot missing")
	}
	raw := sqlitedb.MustOpen(filepath.Join(dir, "primary.db"))
	defer raw.Close()
	recoverySlot := pq.SecretKeyslotEntityID(pq.SecretKeyslot{Kind: slotRecovery})
	var rows int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM write_event_mutations m JOIN write_events e ON e.seq = m.seq WHERE m.entity_type = ? AND m.entity_id = ? AND e.actor = 7`,
		int64(apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT), recoverySlot).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("recovery slot writes authored by user 7 = %d, %v; want 2 (create then update)", rows, err)
	}
	var types string
	if err := raw.QueryRow(`SELECT group_concat(op, ',') FROM (SELECT op FROM write_event_mutations WHERE entity_type = ? AND entity_id = ? ORDER BY seq)`,
		int64(apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT), recoverySlot).Scan(&types); err != nil || types != "1,2" {
		t.Fatalf("recovery slot write kinds = %q, %v; want 1,2", types, err)
	}
	if err := store.Commit(t.Context(), nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		deletes, err := q.NodeSecretKeyslotDeletes(t.Context(), pq.EventMeta{GlobalSeq: seq, EventTime: 1}, int64(primary.ID))
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(deletes...), nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := findSlot(listKeyslots(store.Queries()), slotMachine, primary.ID); ok {
		t.Fatal("machine slot still live after delete event")
	}
	if _, err := Open(dir, store); err != nil {
		t.Fatalf("Open: %v", err)
	}
}
