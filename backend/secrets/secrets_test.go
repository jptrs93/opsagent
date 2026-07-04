package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

// memStore is an in-memory secrets.Store for tests. It mimics the real store's
// persistence so a second Open() sees the same rows (as on a restart/recovery).
type memStore struct {
	slots         map[string]Keyslot
	records       map[int32]Record
	systemRecords map[string]SystemRecord
}

func newMemStore() *memStore {
	return &memStore{slots: map[string]Keyslot{}, records: map[int32]Record{}, systemRecords: map[string]SystemRecord{}}
}

func (m *memStore) ListSecretKeyslots() []Keyslot {
	out := make([]Keyslot, 0, len(m.slots))
	for _, s := range m.slots {
		out = append(out, s)
	}
	return out
}
func (m *memStore) UpsertSecretKeyslot(k Keyslot) { m.slots[k.Slot] = k }
func (m *memStore) ListSecrets() []Record {
	out := make([]Record, 0, len(m.records))
	for _, r := range m.records {
		out = append(out, r)
	}
	return out
}
func (m *memStore) NextSecretVersion(name string) int32 {
	var max int32
	for _, r := range m.records {
		if r.Name == name && r.Version > max {
			max = r.Version
		}
	}
	return max + 1
}
func (m *memStore) InsertSecret(r Record) Record {
	r.ID = int32(len(m.records) + 1)
	m.records[r.ID] = r
	return r
}
func (m *memStore) RenameSecretRecords(name, newName string, records []Record) {
	for _, r := range records {
		m.records[r.ID] = r
	}
}
func (m *memStore) DeleteSecret(name string) {
	for id, r := range m.records {
		if r.Name == name {
			delete(m.records, id)
		}
	}
}
func (m *memStore) GetSystemSecret(name string) (SystemRecord, bool) {
	r, ok := m.systemRecords[name]
	return r, ok
}
func (m *memStore) UpsertSystemSecret(r SystemRecord) { m.systemRecords[r.Name] = r }

func (m *memStore) recordByName(name string) Record {
	for _, r := range m.records {
		if r.Name == name {
			return r
		}
	}
	return Record{}
}

func mustOpen(t *testing.T, dir string, store Store) *Manager {
	t.Helper()
	mgr, err := Open(dir, store)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return mgr
}

func TestSetResolveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := newMemStore()
	mgr := mustOpen(t, dir, store)

	meta, err := mgr.Set("staging.db.password", "staging", []byte("hunter2"), 7)
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok := mgr.Resolve(meta.ID)
	if !ok || got != "hunter2" {
		t.Fatalf("Resolve = %q, %v; want hunter2, true", got, ok)
	}

	// machine.key must exist and be 0600.
	info, err := os.Stat(filepath.Join(dir, machineKeyFile))
	if err != nil {
		t.Fatalf("stat machine.key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("machine.key perm = %o; want 600", perm)
	}

	// List returns metadata, never the value.
	metas := mgr.List()
	if len(metas) != 1 || metas[0].ID == 0 || metas[0].Name != "staging.db.password" || metas[0].Version != 1 {
		t.Fatalf("List = %+v", metas)
	}
}

func TestReopenWithMachineKeyUnlocks(t *testing.T) {
	dir := t.TempDir()
	store := newMemStore()
	mgr := mustOpen(t, dir, store)
	meta, err := mgr.Set("k", "", []byte("v"), 0)
	if err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Reopen (simulates a normal restart): machine.key + DB present -> unlocked.
	mgr2 := mustOpen(t, dir, store)
	if unlocked, _ := mgr2.Status(); !unlocked {
		t.Fatal("expected reopened store to be unlocked")
	}
	if got, ok := mgr2.Resolve(meta.ID); !ok || got != "v" {
		t.Fatalf("Resolve after reopen = %q, %v", got, ok)
	}
}

func TestCiphertextAtRestNotPlaintext(t *testing.T) {
	dir := t.TempDir()
	store := newMemStore()
	mgr := mustOpen(t, dir, store)
	if _, err := mgr.Set("k", "", []byte("super-secret-value"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	rec := store.recordByName("k")
	if string(rec.Ciphertext) == "super-secret-value" {
		t.Fatal("value stored in plaintext")
	}
}

func TestAADBindingPreventsSwap(t *testing.T) {
	dir := t.TempDir()
	store := newMemStore()
	mgr := mustOpen(t, dir, store)
	if _, err := mgr.Set("a", "", []byte("value-a"), 0); err != nil {
		t.Fatalf("Set a: %v", err)
	}
	if _, err := mgr.Set("b", "", []byte("value-b"), 0); err != nil {
		t.Fatalf("Set b: %v", err)
	}
	// Move a's ciphertext onto b's name: decryption must fail (name is AAD), so
	// Resolve("b") returns not-ok rather than leaking value-a under b.
	recA := store.recordByName("a")
	recB := store.recordByName("b")
	recB.Ciphertext = recA.Ciphertext
	recB.Nonce = recA.Nonce
	store.records[recB.ID] = recB
	mgr2 := mustOpen(t, dir, store)
	if got, ok := mgr2.Resolve(recB.ID); ok {
		t.Fatalf("expected AAD mismatch to fail; got %q", got)
	}
}

func TestRenameReencryptsVersions(t *testing.T) {
	dir := t.TempDir()
	store := newMemStore()
	mgr := mustOpen(t, dir, store)
	first, err := mgr.Set("db.password", "", []byte("one"), 0)
	if err != nil {
		t.Fatalf("Set first: %v", err)
	}
	second, err := mgr.Set("db.password", "", []byte("two"), 0)
	if err != nil {
		t.Fatalf("Set second: %v", err)
	}

	if _, err := mgr.Rename("db.password", "prod.db.password"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if got, ok := mgr.Resolve(first.ID); !ok || got != "one" {
		t.Fatalf("Resolve first after rename = %q, %v; want one, true", got, ok)
	}
	if got, ok := mgr.Resolve(second.ID); !ok || got != "two" {
		t.Fatalf("Resolve second after rename = %q, %v; want two, true", got, ok)
	}

	mgr2 := mustOpen(t, dir, store)
	if got, ok := mgr2.Resolve(first.ID); !ok || got != "one" {
		t.Fatalf("Resolve first after reopen = %q, %v; want one, true", got, ok)
	}
	if got, ok := mgr2.Resolve(second.ID); !ok || got != "two" {
		t.Fatalf("Resolve second after reopen = %q, %v; want two, true", got, ok)
	}
}

func TestSystemSecretsAreSeparateFromUserSecrets(t *testing.T) {
	dir := t.TempDir()
	store := newMemStore()
	mgr := mustOpen(t, dir, store)

	if err := mgr.SetInternal("opendeploy.cluster.ca.key", []byte("ca-key")); err != nil {
		t.Fatalf("SetInternal: %v", err)
	}
	if len(store.records) != 0 {
		t.Fatalf("system secret was written to user records: %+v", store.records)
	}
	if _, ok := store.systemRecords["opendeploy.cluster.ca.key"]; !ok {
		t.Fatal("system secret was not written to system records")
	}
	if got := mgr.List(); len(got) != 0 {
		t.Fatalf("List exposed system secret: %+v", got)
	}
	if _, ok := mgr.Resolve(1); ok {
		t.Fatal("Resolve exposed system secret")
	}
	if _, err := mgr.Reveal("opendeploy.cluster.ca.key"); err != ErrNotFound {
		t.Fatalf("Reveal system secret err = %v; want ErrNotFound", err)
	}
	got, err := mgr.RevealInternal("opendeploy.cluster.ca.key")
	if err != nil || string(got) != "ca-key" {
		t.Fatalf("RevealInternal = %q, %v; want ca-key, nil", got, err)
	}
	if _, err := mgr.Set("opendeploy.cluster.ca.key", "", []byte("user"), 0); err != ErrReservedName {
		t.Fatalf("Set reserved name err = %v; want ErrReservedName", err)
	}
	if _, err := mgr.Set("opendeploy.config.github_token", "config", []byte("user"), 0); err != nil {
		t.Fatalf("Set opendeploy config secret err = %v; want nil", err)
	}
}

func TestRevealInternalLoadsFromStoreAfterReopen(t *testing.T) {
	dir := t.TempDir()
	store := newMemStore()
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
	store := newMemStore()
	mgr := mustOpen(t, dir, store)
	meta, err := mgr.Set("k", "", []byte("v"), 0)
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	code, err := mgr.GenerateRecoveryCode()
	if err != nil {
		t.Fatalf("GenerateRecoveryCode: %v", err)
	}
	if _, rc := mgr.Status(); !rc {
		t.Fatal("recovery should be configured")
	}

	// Simulate restoring the DB backup onto a fresh machine: same store rows,
	// but no machine.key on disk.
	freshDir := t.TempDir()
	mgr2 := mustOpen(t, freshDir, store)
	if unlocked, _ := mgr2.Status(); unlocked {
		t.Fatal("fresh machine without machine.key should be locked")
	}
	if _, ok := mgr2.Resolve(meta.ID); ok {
		t.Fatal("locked store must not resolve secrets")
	}
	if _, err := mgr2.Set("x", "", []byte("y"), 0); err == nil {
		t.Fatal("locked store must reject Set")
	}

	// Wrong code fails.
	if err := mgr2.Unlock("AAAAA-BBBBB-CCCCC"); err == nil {
		t.Fatal("expected wrong recovery code to fail")
	}
	// Correct code unlocks and re-establishes machine.key.
	if err := mgr2.Unlock(code); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if got, ok := mgr2.Resolve(meta.ID); !ok || got != "v" {
		t.Fatalf("Resolve after recovery = %q, %v", got, ok)
	}
	if _, err := os.Stat(filepath.Join(freshDir, machineKeyFile)); err != nil {
		t.Fatalf("machine.key not re-established: %v", err)
	}

	// And a subsequent normal reopen on the fresh machine is unattended.
	mgr3 := mustOpen(t, freshDir, store)
	if unlocked, _ := mgr3.Status(); !unlocked {
		t.Fatal("expected unattended unlock after recovery")
	}
}

func TestCodeFormattingTolerated(t *testing.T) {
	dir := t.TempDir()
	store := newMemStore()
	mgr := mustOpen(t, dir, store)
	_, _ = mgr.Set("k", "", []byte("v"), 0)
	code, err := mgr.GenerateRecoveryCode()
	if err != nil {
		t.Fatalf("GenerateRecoveryCode: %v", err)
	}
	fresh := t.TempDir()
	mgr2 := mustOpen(t, fresh, store)
	// Lowercase, spaces instead of hyphens — normalizeCode should accept it.
	munged := normalizeCode(code)
	spaced := ""
	for i, c := range munged {
		if i > 0 && i%4 == 0 {
			spaced += " "
		}
		spaced += string(c)
	}
	if err := mgr2.Unlock(toLower(spaced)); err != nil {
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
