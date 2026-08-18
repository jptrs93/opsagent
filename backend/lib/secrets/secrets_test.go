package secrets

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/lib/machinekey"
	"github.com/jptrs93/opsagent/backend/storage"
)

// memStore is an in-memory secrets.Store for tests. It mimics the real store's
// persistence so a second Open() sees the same rows (as on a restart/recovery).
type memStore struct {
	slots         map[string]Keyslot
	identities    map[int32]memIdentity
	records       map[int32]Record
	systemRecords map[string]SystemRecord
	nextID        int32
}

type memIdentity struct {
	name    string
	spaceID int32
}

func newMemStore() *memStore {
	return &memStore{
		slots:         map[string]Keyslot{},
		identities:    map[int32]memIdentity{},
		records:       map[int32]Record{},
		systemRecords: map[string]SystemRecord{},
	}
}

func (m *memStore) ListSecretKeyslots() []Keyslot {
	out := make([]Keyslot, 0, len(m.slots))
	for _, s := range m.slots {
		out = append(out, s)
	}
	return out
}
func (m *memStore) UpsertSecretKeyslot(k Keyslot) { m.slots[k.Slot] = k }
func (m *memStore) ListSecretVersionRecords() []Record {
	out := make([]Record, 0, len(m.records))
	for _, r := range m.records {
		out = append(out, r)
	}
	return out
}
func (m *memStore) GetSecretIDByName(spaceID int32, name string) (int32, bool) {
	for id, identity := range m.identities {
		if identity.name == name && identity.spaceID == spaceID {
			return id, true
		}
	}
	return 0, false
}
func (m *memStore) CreateSecretWithVersion(name string, spaceID, directoryID, author int32, seal SealFunc) (Record, error) {
	if _, exists := m.GetSecretIDByName(spaceID, name); exists {
		return Record{}, fmt.Errorf("secret %q already exists", name)
	}
	m.nextID++
	secretID := m.nextID
	m.identities[secretID] = memIdentity{name: name, spaceID: spaceID}
	return m.insertVersion(secretID, author, seal)
}
func (m *memStore) AppendSecretVersionWithDeploymentUpdates(secretID, author int32, seal SealFunc, update bool, deployments []storage.DeploymentConfigVersion, afterCommit func(Record)) (Record, []int32, error) {
	if _, ok := m.identities[secretID]; !ok {
		return Record{}, nil, fmt.Errorf("secret %d not found", secretID)
	}
	rec, err := m.insertVersion(secretID, author, seal)
	if err != nil {
		return Record{}, nil, err
	}
	if afterCommit != nil {
		afterCommit(rec)
	}
	return rec, nil, nil
}
func (m *memStore) insertVersion(secretID, author int32, seal SealFunc) (Record, error) {
	var maxVersion int32
	for _, r := range m.records {
		if r.SecretID == secretID && r.Version > maxVersion {
			maxVersion = r.Version
		}
	}
	version := maxVersion + 1
	sealed, err := seal(secretID, version)
	if err != nil {
		return Record{}, err
	}
	m.nextID++
	identity := m.identities[secretID]
	rec := Record{
		ID:         m.nextID,
		SecretID:   secretID,
		Name:       identity.name,
		Version:    version,
		SpaceID:    identity.spaceID,
		SMKVersion: sealed.SMKVersion,
		Ciphertext: sealed.Ciphertext,
		Nonce:      sealed.Nonce,
		Author:     author,
	}
	m.records[rec.ID] = rec
	return rec, nil
}
func (m *memStore) RenameSecret(secretID int32, newName string) error {
	identity, ok := m.identities[secretID]
	if !ok {
		return fmt.Errorf("secret %d not found", secretID)
	}
	identity.name = newName
	m.identities[secretID] = identity
	for id, r := range m.records {
		if r.SecretID == secretID {
			r.Name = newName
			m.records[id] = r
		}
	}
	return nil
}
func (m *memStore) MoveSecretSpace(secretID, newSpaceID, newDirectoryID, author int32) error {
	identity, ok := m.identities[secretID]
	if !ok {
		return fmt.Errorf("secret %d not found", secretID)
	}
	identity.spaceID = newSpaceID
	m.identities[secretID] = identity
	for id, r := range m.records {
		if r.SecretID == secretID {
			r.SpaceID = newSpaceID
			m.records[id] = r
		}
	}
	return nil
}
func (m *memStore) DeleteSecret(secretID int32) error {
	delete(m.identities, secretID)
	for id, r := range m.records {
		if r.SecretID == secretID {
			delete(m.records, id)
		}
	}
	return nil
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
	var mgr *Manager
	var err error
	if len(store.ListSecretKeyslots()) == 0 {
		mgr, err = Initialize(dir, store)
	} else {
		mgr, err = Open(dir, store)
	}
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return mgr
}

func TestOpenRejectsUninitializedStore(t *testing.T) {
	if _, err := Open(t.TempDir(), newMemStore()); err == nil {
		t.Fatal("Open succeeded for an uninitialized secrets store")
	}
}

func TestCreateResolveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := newMemStore()
	mgr := mustOpen(t, dir, store)

	meta, err := mgr.Create("staging.db.password", []byte("hunter2"), 7, 0, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if meta.SecretID == 0 || meta.ID == 0 || meta.Version != 1 || meta.Author != 7 {
		t.Fatalf("meta = %+v", meta)
	}
	got, ok := mgr.Resolve(meta.ID)
	if !ok || got != "hunter2" {
		t.Fatalf("Resolve = %q, %v; want hunter2, true", got, ok)
	}
	if m, ok := mgr.MetaByID(meta.ID); !ok || m.Name != "staging.db.password" || m.SecretID != meta.SecretID {
		t.Fatalf("MetaByID = %+v, %v", m, ok)
	}

	// machine.key must exist and be 0600.
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
	store := newMemStore()
	mgr := mustOpen(t, dir, store)
	meta, err := mgr.Create("k", []byte("v"), 0, 0, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
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
	if _, err := mgr.Create("k", []byte("super-secret-value"), 0, 0, 0); err != nil {
		t.Fatalf("Create: %v", err)
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
	metaA, err := mgr.Create("a", []byte("value-a"), 0, 0, 0)
	if err != nil {
		t.Fatalf("Create a: %v", err)
	}
	metaB, err := mgr.Create("b", []byte("value-b"), 0, 0, 0)
	if err != nil {
		t.Fatalf("Create b: %v", err)
	}
	// Move a's ciphertext onto b's row: decryption must fail (the secret id is
	// in the AAD), so Resolve(b) returns not-ok rather than leaking value-a.
	recA := store.records[metaA.ID]
	recB := store.records[metaB.ID]
	recB.Ciphertext = recA.Ciphertext
	recB.Nonce = recA.Nonce
	store.records[recB.ID] = recB
	mgr2, err := Open(dir, store)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got, ok := mgr2.Resolve(recB.ID); ok {
		t.Fatalf("expected AAD mismatch to fail; got %q", got)
	}
}

func TestAADBindingPreventsVersionSwap(t *testing.T) {
	dir := t.TempDir()
	store := newMemStore()
	mgr := mustOpen(t, dir, store)
	v1, err := mgr.Create("db.password", []byte("old"), 0, 0, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	v2, err := mgr.SetWithDeploymentUpdates(v1.SecretID, []byte("new"), 0, false, nil, nil)
	if err != nil {
		t.Fatalf("Set v2: %v", err)
	}
	// Move the old version's ciphertext onto the new version row: the version
	// number is in the AAD, so a rollback-by-row-swap must fail.
	recV1 := store.records[v1.ID]
	recV2 := store.records[v2.ID]
	recV2.Ciphertext = recV1.Ciphertext
	recV2.Nonce = recV1.Nonce
	store.records[recV2.ID] = recV2
	mgr2, err := Open(dir, store)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got, ok := mgr2.Resolve(v2.ID); ok {
		t.Fatalf("expected version swap to fail; got %q", got)
	}
}

func TestRenameIsMetadataOnly(t *testing.T) {
	dir := t.TempDir()
	store := newMemStore()
	mgr := mustOpen(t, dir, store)
	first, err := mgr.Create("db.password", []byte("one"), 0, 0, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	second, err := mgr.SetWithDeploymentUpdates(first.SecretID, []byte("two"), 0, false, nil, nil)
	if err != nil {
		t.Fatalf("Set second: %v", err)
	}

	beforeCiphertext := string(store.records[first.ID].Ciphertext)
	if err := mgr.Rename(first.SecretID, "prod.db.password"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if got := string(store.records[first.ID].Ciphertext); got != beforeCiphertext {
		t.Fatal("rename re-encrypted a version; the id-bound AAD makes that unnecessary")
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
	if m, ok := mgr2.MetaByID(second.ID); !ok || m.Name != "prod.db.password" {
		t.Fatalf("MetaByID after reopen = %+v, %v", m, ok)
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
	if _, ok := mgr.Resolve(1); ok {
		t.Fatal("Resolve exposed system secret")
	}
	got, err := mgr.RevealInternal("opendeploy.cluster.ca.key")
	if err != nil || string(got) != "ca-key" {
		t.Fatalf("RevealInternal = %q, %v; want ca-key, nil", got, err)
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
	meta, err := mgr.Create("k", []byte("v"), 0, 0, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
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
	if _, err := mgr2.Create("x", []byte("y"), 0, 0, 0); err == nil {
		t.Fatal("locked store must reject Create")
	}
	if err := mgr2.Rename(meta.SecretID, "renamed"); err != ErrLocked {
		t.Fatalf("locked store rename err = %v; want ErrLocked", err)
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
	if _, err := os.Stat(filepath.Join(freshDir, machinekey.FileName)); err != nil {
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
	_, _ = mgr.Create("k", []byte("v"), 0, 0, 0)
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
