// Package secrets implements opendeploy's primary-only secrets store.
//
// Design: envelope encryption with a small key hierarchy.
//
//   - A single 32-byte Secrets Master Key (SMK) encrypts every secret value
//     (XChaCha20-Poly1305, with the secret's name bound as associated data so a
//     ciphertext cannot be moved to a different name).
//   - The SMK is never stored in the clear. It is stored wrapped, once per
//     "keyslot": the MACHINE slot (the SMK sealed under a random key kept in
//     {dataDir}/machine.key, 0600, for unattended boot) and the optional
//     RECOVERY slot (the SMK sealed under an Argon2id-derived key from a
//     break-glass recovery code).
//
// The machine key lives outside the database and outside backups, so a leaked
// DB/backup is useless without either the on-box machine.key or the recovery
// code. Recovery on a fresh machine: restore the DB backup, then Unlock with
// the recovery code — this re-derives the SMK and writes a new machine.key.
//
// The secret_keyslots, secrets, and system_secrets tables are PRIMARY-ONLY: the
// cluster feeder never replicates them (it only ships deployment configs/status),
// so secrets never reach a secondary's database.
package secrets

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	slotMachine  = "machine"
	slotRecovery = "recovery"

	machineKeyFile = "machine.key"

	keyLen               = chacha20poly1305.KeySize // 32
	saltLen              = 16
	recoveryEntropyBytes = 32 // 256 bits -> 52 base32 chars

	// Argon2id parameters for deriving a KEK from the recovery code. Tuned for
	// an interactive break-glass operation, not a hot path.
	argon2Time    = 3
	argon2Memory  = 64 * 1024 // 64 MiB
	argon2Threads = 4
)

// ErrLocked is returned by write/read operations when the SMK is not loaded
// (the store is locked and awaiting a recovery unlock).
var ErrLocked = errors.New("secrets store is locked")

// ErrNoRecoveryCode is returned by Unlock when no recovery slot exists.
var ErrNoRecoveryCode = errors.New("no recovery code configured")

// ErrInvalidRecoveryCode is returned by Unlock when the supplied code does not
// unwrap the recovery slot.
var ErrInvalidRecoveryCode = errors.New("invalid recovery code")

// ErrNotFound is returned by Reveal when no secret exists for the given name.
var ErrNotFound = errors.New("secret not found")

// ErrReservedName is returned when user-facing APIs try to mutate OpenDeploy's
// reserved internal secret namespace.
var ErrReservedName = errors.New("secret name is reserved for internal use")

// Keyslot is a wrapped copy of the SMK as persisted in secret_keyslots.
type Keyslot struct {
	Slot       string
	SMKVersion int32
	WrappedSMK []byte
	Nonce      []byte
	KDFSalt    []byte // recovery slot only; nil for the machine slot
	CreatedAt  int64  // epoch ms
}

// Record is an encrypted secret as persisted in the secrets table.
type Record struct {
	Name       string
	Group      string
	SMKVersion int32
	Ciphertext []byte
	Nonce      []byte
	CreatedAt  int64 // epoch ms
	UpdatedAt  int64 // epoch ms
	UpdatedBy  int32
}

// SystemRecord is an encrypted OpenDeploy-managed secret as persisted in the
// system_secrets table.
type SystemRecord struct {
	Name       string
	SMKVersion int32
	Ciphertext []byte
	Nonce      []byte
	CreatedAt  int64 // epoch ms
	UpdatedAt  int64 // epoch ms
}

// Meta describes a secret WITHOUT its value, for listing.
type Meta struct {
	Name      string
	Group     string
	CreatedAt time.Time
	UpdatedAt time.Time
	UpdatedBy int32
}

const defaultUserSecretGroup = "default"

// Store is the persistence the Manager needs. The sqlite StorageAdapter
// implements it. Writes follow opendeploy's panic-on-failure convention.
type Store interface {
	ListSecretKeyslots() []Keyslot
	UpsertSecretKeyslot(Keyslot)
	ListSecrets() []Record
	UpsertSecret(Record)
	DeleteSecret(name string)
	GetSystemSecret(name string) (SystemRecord, bool)
	UpsertSystemSecret(SystemRecord)
}

// machineKeyProvider supplies the key-encryption key (KEK) that wraps the SMK
// in the machine keyslot, and persists whatever it needs to recover that KEK on
// this machine for unattended boot. It is the boundary at which Phase 3 (TPM
// sealing) plugs in: the default fileMachineKey keeps the KEK in a 0600 file; a
// future tpm2MachineKey seals it to the TPM and stores only the sealed blob.
//
// The DB keyslot format is identical for every provider — the machine slot
// always holds AEAD(SMK, KEK); only how the KEK itself is protected at rest
// differs — so a provider is a drop-in and a TPM-less node (container, VM with
// no vTPM) transparently falls back to the file provider. See
// docs/engineering/secrets.md.
type machineKeyProvider interface {
	// establish creates and persists a fresh machine KEK, returning it.
	establish() ([]byte, error)
	// load returns the previously-established machine KEK, or an error (which
	// leaves the store locked) if it cannot be recovered on this machine.
	load() ([]byte, error)
}

// Manager owns the in-memory SMK and a cache of encrypted records. It is safe
// for concurrent use.
type Manager struct {
	store      Store
	machineKey machineKeyProvider

	mu          sync.RWMutex
	smk         []byte // nil => locked
	version     int32
	cache       map[string]Record // name -> record (ciphertext)
	systemCache map[string]SystemRecord
}

// Open loads the secrets store. On first run (no keyslots) it generates the SMK
// and machine key and writes machine.key. If keyslots exist it unlocks using
// machine.key; a missing/invalid machine.key leaves the store locked (a valid
// state — the server still runs; deployments referencing secrets fail closed
// until Unlock). Open only returns an error for a genuine first-init failure.
func Open(dataDir string, store Store) (*Manager, error) {
	m := &Manager{
		store:       store,
		machineKey:  &fileMachineKey{path: filepath.Join(dataDir, machineKeyFile)},
		cache:       make(map[string]Record),
		systemCache: make(map[string]SystemRecord),
	}
	for _, r := range store.ListSecrets() {
		m.cache[r.Name] = r
	}

	slots := store.ListSecretKeyslots()
	if _, ok := findSlot(slots, slotMachine); !ok {
		if len(slots) == 0 {
			if err := m.initFirstRun(); err != nil {
				return nil, fmt.Errorf("initializing secrets store: %w", err)
			}
			slog.Info("secrets store initialized")
			slog.Warn("secrets recovery code not configured — generate one so secrets can be recovered if this machine is lost")
			return m, nil
		}
		slog.Error("secrets store has no machine keyslot; locked until recovery unlock")
		return m, nil
	}

	if err := m.unlockWithMachineKey(slots); err != nil {
		slog.Error("secrets store locked: could not unlock with machine key; use the recovery code to unlock", "err", err)
		return m, nil
	}
	slog.Info("secrets store unlocked")
	if _, ok := findSlot(slots, slotRecovery); !ok {
		slog.Warn("secrets recovery code not configured — generate one so secrets can be recovered if this machine is lost")
	}
	return m, nil
}

// Resolve returns the plaintext value for a secret name. It implements the
// runner's secret resolver. Returns ("", false) when locked or unknown.
func (m *Manager) Resolve(name string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.smk == nil {
		return "", false
	}
	rec, ok := m.cache[name]
	if !ok {
		return "", false
	}
	pt, err := aeadOpen(m.smk, rec.Ciphertext, rec.Nonce, secretAAD("user", name))
	if err != nil {
		slog.Error("decrypting secret failed", "name", name, "err", err)
		return "", false
	}
	return string(pt), true
}

// ResolveMany decrypts the requested user secrets as one batch. It is used by
// deployment preparation so workers can fetch all referenced secrets in a
// single cluster request and keep plaintext only in memory for runner startup.
func (m *Manager) ResolveMany(names []string) (map[string]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.smk == nil {
		return nil, ErrLocked
	}
	out := make(map[string]string, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("secret name is required")
		}
		if _, ok := out[name]; ok {
			continue
		}
		rec, ok := m.cache[name]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		pt, err := aeadOpen(m.smk, rec.Ciphertext, rec.Nonce, secretAAD("user", name))
		if err != nil {
			return nil, fmt.Errorf("decrypting secret %q: %w", name, err)
		}
		out[name] = string(pt)
	}
	return out, nil
}

func (m *Manager) HasSecret(name string) (bool, time.Time) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.cache[name]
	return ok, time.Unix(r.UpdatedAt/1000, 0)
}

// Reveal returns the decrypted value of a single secret on explicit operator
// request. Unlike Resolve (the runner's silent spawn-time path), it
// distinguishes the failure modes for the API: ErrLocked when the store is
// locked, ErrNotFound when no such secret exists.
func (m *Manager) Reveal(name string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.smk == nil {
		return nil, ErrLocked
	}
	rec, ok := m.cache[name]
	if !ok {
		return nil, ErrNotFound
	}
	pt, err := aeadOpen(m.smk, rec.Ciphertext, rec.Nonce, secretAAD("user", name))
	if err != nil {
		return nil, fmt.Errorf("decrypting secret %q: %w", name, err)
	}
	return pt, nil
}

// Set creates or updates a secret. value is encrypted under the SMK before it
// touches disk. Returns the secret's metadata (never its value).
func (m *Manager) Set(name, group string, value []byte, updatedBy int32) (Meta, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Meta{}, errors.New("secret name is required")
	}
	if isReservedInternalName(name) {
		return Meta{}, ErrReservedName
	}
	return m.set(name, group, value, updatedBy)
}

// SetInternal creates or updates an OpenDeploy-managed internal secret. Internal
// secrets are encrypted by the same SMK but are hidden from user-facing CRUD.
func (m *Manager) SetInternal(name string, value []byte) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("secret name is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.smk == nil {
		return ErrLocked
	}
	ct, nonce, err := aeadSeal(m.smk, value, secretAAD("system", name))
	if err != nil {
		return err
	}
	now := nowMs()
	createdAt := now
	if existing, ok := m.systemCache[name]; ok {
		createdAt = existing.CreatedAt
	} else if existing, ok := m.store.GetSystemSecret(name); ok {
		createdAt = existing.CreatedAt
	}
	rec := SystemRecord{
		Name:       name,
		SMKVersion: m.version,
		Ciphertext: ct,
		Nonce:      nonce,
		CreatedAt:  createdAt,
		UpdatedAt:  now,
	}
	m.store.UpsertSystemSecret(rec)
	m.systemCache[name] = rec
	return nil
}

func (m *Manager) set(name, group string, value []byte, updatedBy int32) (Meta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.smk == nil {
		return Meta{}, ErrLocked
	}
	ct, nonce, err := aeadSeal(m.smk, value, secretAAD("user", name))
	if err != nil {
		return Meta{}, err
	}
	now := nowMs()
	createdAt := now
	if existing, ok := m.cache[name]; ok {
		createdAt = existing.CreatedAt
	}
	rec := Record{
		Name:       name,
		Group:      defaultUserSecretGroup,
		SMKVersion: m.version,
		Ciphertext: ct,
		Nonce:      nonce,
		CreatedAt:  createdAt,
		UpdatedAt:  now,
		UpdatedBy:  updatedBy,
	}
	m.store.UpsertSecret(rec)
	m.cache[name] = rec
	return rec.meta(), nil
}

// RevealInternal decrypts an OpenDeploy-managed internal secret. It bypasses the
// user-facing internal-secret guard but still requires the store to be unlocked.
func (m *Manager) RevealInternal(name string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.smk == nil {
		return nil, ErrLocked
	}
	rec, ok := m.systemCache[name]
	if !ok {
		if rec, ok = m.store.GetSystemSecret(name); !ok {
			return nil, ErrNotFound
		}
	}
	pt, err := aeadOpen(m.smk, rec.Ciphertext, rec.Nonce, secretAAD("system", name))
	if err != nil {
		return nil, fmt.Errorf("decrypting internal secret %q: %w", name, err)
	}
	return pt, nil
}

// Delete removes a user secret. Safe to call while locked (no decryption needed).
func (m *Manager) Delete(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if isReservedInternalName(name) {
		return ErrReservedName
	}
	m.store.DeleteSecret(name)
	delete(m.cache, name)
	return nil
}

// List returns metadata for all secrets, sorted by name. Never returns values.
func (m *Manager) List() []Meta {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Meta, 0, len(m.cache))
	for _, rec := range m.cache {
		out = append(out, rec.meta())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Status reports whether the store is unlocked and whether a recovery code has
// been configured.
func (m *Manager) Status() (unlocked, recoveryConfigured bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	unlocked = m.smk != nil
	_, recoveryConfigured = findSlot(m.store.ListSecretKeyslots(), slotRecovery)
	return
}

// GenerateRecoveryCode creates (or rotates) the recovery keyslot and returns
// the new break-glass code. The code is returned exactly once and is never
// stored — only its Argon2id-wrapped SMK is persisted.
func (m *Manager) GenerateRecoveryCode() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.smk == nil {
		return "", ErrLocked
	}
	code, err := generateRecoveryCode()
	if err != nil {
		return "", err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	kek := argon2.IDKey([]byte(normalizeCode(code)), salt, argon2Time, argon2Memory, argon2Threads, keyLen)
	wrapped, nonce, err := aeadSeal(kek, m.smk, slotAAD(slotRecovery))
	if err != nil {
		return "", err
	}
	m.store.UpsertSecretKeyslot(Keyslot{
		Slot:       slotRecovery,
		SMKVersion: m.version,
		WrappedSMK: wrapped,
		Nonce:      nonce,
		KDFSalt:    salt,
		CreatedAt:  nowMs(),
	})
	return code, nil
}

// Unlock recovers a locked store using the recovery code, then re-establishes a
// fresh local machine key (and machine keyslot) so subsequent boots are
// unattended again.
func (m *Manager) Unlock(code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := findSlot(m.store.ListSecretKeyslots(), slotRecovery)
	if !ok {
		return ErrNoRecoveryCode
	}
	kek := argon2.IDKey([]byte(normalizeCode(code)), rec.KDFSalt, argon2Time, argon2Memory, argon2Threads, keyLen)
	smk, err := aeadOpen(kek, rec.WrappedSMK, rec.Nonce, slotAAD(slotRecovery))
	if err != nil {
		return ErrInvalidRecoveryCode
	}
	if err := m.rewriteMachineSlot(smk, rec.SMKVersion); err != nil {
		return err
	}
	m.smk = smk
	m.version = rec.SMKVersion
	slog.Info("secrets store unlocked via recovery code; machine key re-established")
	return nil
}

// --- init / unlock internals ---

func (m *Manager) initFirstRun() error {
	smk := make([]byte, keyLen)
	if _, err := rand.Read(smk); err != nil {
		return err
	}
	if err := m.rewriteMachineSlot(smk, 1); err != nil {
		return err
	}
	m.smk = smk
	m.version = 1
	return nil
}

// rewriteMachineSlot establishes a fresh machine KEK via the configured
// provider and stores the SMK wrapped under it as the machine keyslot.
func (m *Manager) rewriteMachineSlot(smk []byte, version int32) error {
	machineKey, err := m.machineKey.establish()
	if err != nil {
		return err
	}
	wrapped, nonce, err := aeadSeal(machineKey, smk, slotAAD(slotMachine))
	if err != nil {
		return err
	}
	m.store.UpsertSecretKeyslot(Keyslot{
		Slot:       slotMachine,
		SMKVersion: version,
		WrappedSMK: wrapped,
		Nonce:      nonce,
		CreatedAt:  nowMs(),
	})
	return nil
}

func (m *Manager) unlockWithMachineKey(slots []Keyslot) error {
	machineKey, err := m.machineKey.load()
	if err != nil {
		return err
	}
	slot, ok := findSlot(slots, slotMachine)
	if !ok {
		return errors.New("no machine keyslot")
	}
	smk, err := aeadOpen(machineKey, slot.WrappedSMK, slot.Nonce, slotAAD(slotMachine))
	if err != nil {
		return fmt.Errorf("unwrapping SMK: %w", err)
	}
	m.smk = smk
	m.version = slot.SMKVersion
	return nil
}

// --- crypto helpers ---

func aeadSeal(key, plaintext, aad []byte) (ciphertext, nonce []byte, err error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return aead.Seal(nil, nonce, plaintext, aad), nonce, nil
}

func aeadOpen(key, ciphertext, nonce, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, nonce, ciphertext, aad)
}

func slotAAD(slot string) []byte { return []byte("opendeploy-keyslot:" + slot) }

func secretAAD(class, name string) []byte {
	return []byte("opendeploy-secret:" + class + ":" + name)
}

func isReservedInternalName(name string) bool {
	name = strings.TrimSpace(name)
	return strings.HasPrefix(name, "opendeploy.") && !strings.HasPrefix(name, "opendeploy.config.")
}

// fileMachineKey is the default machineKeyProvider: it stores the machine KEK
// as a 0600 file in the data dir (outside the DB and outside backups, so a
// leaked DB/backup cannot decrypt the SMK). Phase 3 adds a tpm2MachineKey
// alongside this; see docs/engineering/secrets.md.
type fileMachineKey struct{ path string }

func (f *fileMachineKey) establish() ([]byte, error) {
	key := make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(f.path, key, 0o600); err != nil {
		return nil, err
	}
	// WriteFile does not chmod an existing file; enforce 0600 explicitly.
	if err := os.Chmod(f.path, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func (f *fileMachineKey) load() ([]byte, error) {
	return os.ReadFile(f.path)
}

func generateRecoveryCode() (string, error) {
	raw := make([]byte, recoveryEntropyBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	return groupCode(enc), nil
}

// groupCode inserts a hyphen every 5 characters for readability.
func groupCode(s string) string {
	var b strings.Builder
	for i, c := range s {
		if i > 0 && i%5 == 0 {
			b.WriteByte('-')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// normalizeCode reverses groupCode and tolerates user formatting.
func normalizeCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}

func findSlot(slots []Keyslot, name string) (Keyslot, bool) {
	for _, s := range slots {
		if s.Slot == name {
			return s, true
		}
	}
	return Keyslot{}, false
}

func nowMs() int64 { return time.Now().UnixMilli() }

func (r Record) meta() Meta {
	return Meta{
		Name:      r.Name,
		Group:     defaultUserSecretGroup,
		CreatedAt: time.UnixMilli(r.CreatedAt),
		UpdatedAt: time.UnixMilli(r.UpdatedAt),
		UpdatedBy: r.UpdatedBy,
	}
}
