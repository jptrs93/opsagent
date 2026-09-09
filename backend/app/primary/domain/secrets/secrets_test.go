package secrets

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/lib/machinekey"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
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

func recordByID(t *testing.T, store *state.Service, id int32) Record {
	t.Helper()
	for _, r := range ListVersionRecords(store.Queries()) {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("secret version %d not found", id)
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
	if got, ok := mgr2.Resolve(meta.ID); !ok || got != "v" {
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
	if string(recordByID(t, store, meta.ID).Ciphertext) == "super-secret-value" {
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
	recA := recordByID(t, store, metaA.ID)
	if _, err := aeadOpen(mgr.smk, recA.Ciphertext, recA.Nonce, userSecretAAD(metaB.SecretID, recA.Version)); err == nil {
		t.Fatal("ciphertext of one secret opened under another secret's identity")
	}
	if pt, err := aeadOpen(mgr.smk, recA.Ciphertext, recA.Nonce, userSecretAAD(metaA.SecretID, recA.Version)); err != nil || string(pt) != "value-a" {
		t.Fatalf("own identity = %q, %v", pt, err)
	}
}

func TestAADBindingPreventsVersionSwap(t *testing.T) {
	store := openTestStore(t)
	mgr := mustOpen(t, t.TempDir(), store)
	v1, err := mgr.Create("db.password", []byte("old"), 0, 0, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	v2, err := mgr.SetWithDeploymentUpdates(v1.SecretID, []byte("new"), 0, false, nil, nil)
	if err != nil {
		t.Fatalf("Set v2: %v", err)
	}
	recV1 := recordByID(t, store, v1.ID)
	if _, err := aeadOpen(mgr.smk, recV1.Ciphertext, recV1.Nonce, userSecretAAD(v1.SecretID, v2.Version)); err == nil {
		t.Fatal("old version ciphertext opened as the new version")
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
	beforeCiphertext := string(recordByID(t, store, first.ID).Ciphertext)
	if err := mgr.Rename(first.SecretID, "prod.db.password"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if got := string(recordByID(t, store, first.ID).Ciphertext); got != beforeCiphertext {
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
	store := openTestStore(t)
	mgr := mustOpen(t, t.TempDir(), store)

	if err := mgr.SetInternal("opendeploy.cluster.ca.key", []byte("ca-key")); err != nil {
		t.Fatalf("SetInternal: %v", err)
	}
	if records := ListVersionRecords(store.Queries()); len(records) != 0 {
		t.Fatalf("system secret was written to user records: %+v", records)
	}
	if _, ok := getSystemSecret(store.Queries(), "opendeploy.cluster.ca.key"); !ok {
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
	code, err := mgr.GenerateRecoveryCode()
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
	if _, ok := mgr2.Resolve(meta.ID); ok {
		t.Fatal("locked store must not resolve secrets")
	}
	if _, err := mgr2.Create("x", []byte("y"), 0, 0, 0); err == nil {
		t.Fatal("locked store must reject Create")
	}
	if err := mgr2.Rename(meta.SecretID, "renamed"); err != ErrLocked {
		t.Fatalf("locked store rename err = %v; want ErrLocked", err)
	}
	if err := mgr2.Unlock("AAAAA-BBBBB-CCCCC"); err == nil {
		t.Fatal("expected wrong recovery code to fail")
	}
	if err := mgr2.Unlock(code); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if got, ok := mgr2.Resolve(meta.ID); !ok || got != "v" {
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
	code, err := mgr.GenerateRecoveryCode()
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
