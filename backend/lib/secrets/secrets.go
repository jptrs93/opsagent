// Package secrets implements opendeploy's primary-only secrets store.
//
// Design: envelope encryption with a small key hierarchy.
//
//   - A single 32-byte Secrets Master Key (SMK) encrypts every secret value
//     (XChaCha20-Poly1305, with the owning secret's stable id and version
//     number bound as associated data so a ciphertext cannot be moved to a
//     different secret or version — and renames/moves never re-encrypt).
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
// The secret_keyslots, secrets, secret_versions, and system_secrets tables are
// PRIMARY-ONLY: the cluster feeder never replicates them (it only ships
// deployment configs/status), so secrets never reach a secondary's database.
package secrets

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/jptrs93/opsagent/backend/lib/machinekey"
	"github.com/jptrs93/opsagent/backend/storage"
)

const (
	slotMachine  = "machine"
	slotRecovery = "recovery"

	TLSCertPEMSecretName = "opendeploy.tls.pem"

	keyLen               = machinekey.KeyLen // 32
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

// ErrNotFound is returned when no secret exists for the given id.
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

// Record is an encrypted secret version row as persisted in secret_versions,
// joined with its owning identity's name and space for metadata.
type Record struct {
	ID         int32 // version row id
	SecretID   int32 // stable identity id
	Name       string
	Version    int32
	SpaceID    int32
	SMKVersion int32
	Ciphertext []byte
	Nonce      []byte
	CreatedAt  int64 // epoch ms
	Author     int32
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

// Meta describes a secret version WITHOUT its value.
type Meta struct {
	ID        int32 // version row id
	SecretID  int32
	Name      string
	Version   int32
	SpaceID   int32
	CreatedAt time.Time
	Author    int32
}

// SealedValue is the output of sealing one plaintext under the SMK.
type SealedValue struct {
	SMKVersion int32
	Ciphertext []byte
	Nonce      []byte
}

// SealFunc seals a plaintext for the given identity id and version number.
// The store calls it inside the write transaction, once both are known —
// the AAD binds them, so the ciphertext cannot exist earlier.
type SealFunc func(secretID, version int32) (SealedValue, error)

const defaultUserSpaceID int32 = 1

// Store is the persistence the Manager needs. The sqlite StorageAdapter
// implements it; namespace law (sibling uniqueness across secrets, configs,
// and directories) lives there. Writes follow opendeploy's panic-on-failure
// convention; name/namespace violations return the storage layer's errors.
type Store interface {
	ListSecretKeyslots() []Keyslot
	UpsertSecretKeyslot(Keyslot)
	ListSecretVersionRecords() []Record
	GetSecretIDByName(spaceID int32, name string) (int32, bool)
	CreateSecretWithVersion(name string, spaceID, directoryID, author int32, seal SealFunc) (Record, error)
	AppendSecretVersionWithDeploymentUpdates(secretID, author int32, seal SealFunc, updateDeployments bool, expected []storage.DeploymentConfigVersion, afterCommit func(Record)) (Record, []int32, error)
	UpdateSecretVersionCiphertext(versionID, smkVersion int32, ciphertext, nonce []byte)
	RenameSecret(secretID int32, newName string) error
	MoveSecretSpace(secretID, newSpaceID, newDirectoryID int32) error
	DeleteSecret(secretID int32) error
	GetSystemSecret(name string) (SystemRecord, bool)
	UpsertSystemSecret(SystemRecord)
}

// Manager owns the in-memory SMK and a cache of encrypted version records. It
// is safe for concurrent use.
//
// The machine keyslot always holds AEAD(SMK, KEK) regardless of which
// machinekey.Provider supplied the KEK, so Phase 3 (TPM sealing) plugs in
// without touching the DB format. See docs/engineering/secrets.md.
type Manager struct {
	store      Store
	machineKey machinekey.Provider

	mu          sync.RWMutex
	smk         []byte // nil => locked
	version     int32
	cache       map[int32]Record // version row id -> immutable version row (ciphertext)
	systemCache map[string]SystemRecord
}

// Initialize creates the secrets master key and local machine key for a new
// primary. Open deliberately does not perform this state transition.
func Initialize(dataDir string, store Store) (*Manager, error) {
	m := newManager(dataDir, store)
	if slots := store.ListSecretKeyslots(); len(slots) != 0 {
		return nil, fmt.Errorf("secrets store is already initialized")
	}
	if err := m.initFirstRun(); err != nil {
		return nil, fmt.Errorf("initializing secrets store: %w", err)
	}
	slog.Info("secrets store initialized")
	slog.Warn("secrets recovery code not configured — generate one so secrets can be recovered if this machine is lost")
	return m, nil
}

// Open loads an initialized secrets store. A missing/invalid machine.key leaves
// the store locked so it can be recovered with a configured recovery code.
func Open(dataDir string, store Store) (*Manager, error) {
	m := newManager(dataDir, store)
	for _, r := range store.ListSecretVersionRecords() {
		m.cache[r.ID] = r
	}

	slots := store.ListSecretKeyslots()
	if _, ok := findSlot(slots, slotMachine); !ok {
		if len(slots) == 0 {
			return nil, fmt.Errorf("secrets store is not initialized")
		}
		slog.Warn("secrets store has no machine keyslot; locked until recovery unlock")
		return m, nil
	}

	if err := m.unlockWithMachineKey(slots); err != nil {
		slog.Warn("secrets store locked: could not unlock with machine key; use the recovery code to unlock", "err", err)
		return m, nil
	}
	slog.Info("secrets store unlocked")
	if _, ok := findSlot(slots, slotRecovery); !ok {
		slog.Warn("secrets recovery code not configured — generate one so secrets can be recovered if this machine is lost")
	}
	return m, nil
}

func newManager(dataDir string, store Store) *Manager {
	return &Manager{
		store:       store,
		machineKey:  &machinekey.File{Path: filepath.Join(dataDir, machinekey.FileName)},
		cache:       make(map[int32]Record),
		systemCache: make(map[string]SystemRecord),
	}
}

// openRecordLocked decrypts one cached version row. Caller must hold m.mu
// (read) with m.smk set.
func (m *Manager) openRecordLocked(rec Record) ([]byte, error) {
	return aeadOpen(m.smk, rec.Ciphertext, rec.Nonce, userSecretAAD(rec.SecretID, rec.Version))
}

// Resolve returns the plaintext value for a secret version row id. It
// implements the runner's secret resolver. Returns ("", false) when locked or
// unknown.
func (m *Manager) Resolve(id int32) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.smk == nil {
		return "", false
	}
	rec, ok := m.cache[id]
	if !ok {
		return "", false
	}
	pt, err := m.openRecordLocked(rec)
	if err != nil {
		slog.Error("decrypting secret failed", "id", id, "name", rec.Name, "err", err)
		return "", false
	}
	return string(pt), true
}

// RevealByID returns the decrypted value of a single secret version row on
// explicit request. ErrLocked when the store is locked, ErrNotFound when no
// such row exists.
func (m *Manager) RevealByID(id int32) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.smk == nil {
		return nil, ErrLocked
	}
	rec, ok := m.cache[id]
	if !ok {
		return nil, ErrNotFound
	}
	pt, err := m.openRecordLocked(rec)
	if err != nil {
		return nil, fmt.Errorf("decrypting secret id %d: %w", id, err)
	}
	return pt, nil
}

// ResolveMany decrypts the requested user secrets as one batch. It is used by
// deployment preparation so workers can fetch all referenced secrets in a
// single cluster request and keep plaintext only in memory for runner startup.
func (m *Manager) ResolveMany(ids []int32) (map[int32]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.smk == nil {
		return nil, ErrLocked
	}
	out := make(map[int32]string, len(ids))
	for _, id := range ids {
		if id == 0 {
			return nil, fmt.Errorf("secret id is required")
		}
		if _, ok := out[id]; ok {
			continue
		}
		rec, ok := m.cache[id]
		if !ok {
			return nil, fmt.Errorf("%w: id %d", ErrNotFound, id)
		}
		pt, err := m.openRecordLocked(rec)
		if err != nil {
			return nil, fmt.Errorf("decrypting secret id %d: %w", id, err)
		}
		out[id] = string(pt)
	}
	return out, nil
}

// MetaByID describes a secret version row (never its value). Works while
// locked: metadata needs no decryption.
func (m *Manager) MetaByID(id int32) (Meta, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.cache[id]
	if !ok {
		return Meta{}, false
	}
	return rec.meta(), true
}

// LatestMetaByName returns the newest version's metadata for the named secret
// in the root of the default space.
func (m *Manager) LatestMetaByName(name string) (Meta, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var best Record
	found := false
	for _, rec := range m.cache {
		if rec.Name != name || rec.SpaceID != defaultUserSpaceID {
			continue
		}
		if !found || rec.Version > best.Version {
			best = rec
			found = true
		}
	}
	if !found {
		return Meta{}, false
	}
	return best.meta(), true
}

// Create creates a new secret with its first version in directoryID (0 = the
// space root) of spaceID (0 = the default space). value is encrypted under the
// SMK before it touches disk. Returns the version's metadata (never its value).
func (m *Manager) Create(name string, value []byte, author, spaceID, directoryID int32) (Meta, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Meta{}, errors.New("secret name is required")
	}
	if isReservedInternalName(name) {
		return Meta{}, ErrReservedName
	}
	if spaceID <= 0 {
		spaceID = defaultUserSpaceID
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.smk == nil {
		return Meta{}, ErrLocked
	}
	rec, err := m.store.CreateSecretWithVersion(name, spaceID, directoryID, author, m.sealFuncLocked(value))
	if err != nil {
		return Meta{}, err
	}
	m.cache[rec.ID] = rec
	return rec.meta(), nil
}

// SetByName creates the secret in the root of the default space, or appends a
// version if the name already exists there. Used by install/restore flows that
// provision well-known secrets; interactive callers go through Create/Set with
// explicit ids.
func (m *Manager) SetByName(name string, value []byte, author int32) (Meta, error) {
	name = strings.TrimSpace(name)
	if id, ok := m.store.GetSecretIDByName(defaultUserSpaceID, name); ok {
		return m.SetWithDeploymentUpdates(id, value, author, false, nil, nil)
	}
	return m.Create(name, value, author, 0, 0)
}

// SetWithDeploymentUpdates appends an immutable secret version and optionally
// rolls the caller-asserted deployment references to the new row atomically.
func (m *Manager) SetWithDeploymentUpdates(secretID int32, value []byte, author int32, updateDeployments bool, deployments []storage.DeploymentConfigVersion, onCommit func(Meta)) (Meta, error) {
	if secretID == 0 {
		return Meta{}, ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.smk == nil {
		return Meta{}, ErrLocked
	}
	rec, _, err := m.store.AppendSecretVersionWithDeploymentUpdates(secretID, author, m.sealFuncLocked(value), updateDeployments, deployments, func(committed Record) {
		m.cache[committed.ID] = committed
		if onCommit != nil {
			onCommit(committed.meta())
		}
	})
	if err != nil {
		return Meta{}, err
	}
	return rec.meta(), nil
}

// sealFuncLocked builds the store's seal callback over the current SMK.
// Caller must hold m.mu with m.smk set; the store invokes the callback inside
// its write transaction while that lock is still held.
func (m *Manager) sealFuncLocked(value []byte) SealFunc {
	return func(secretID, version int32) (SealedValue, error) {
		ct, nonce, err := aeadSeal(m.smk, value, userSecretAAD(secretID, version))
		if err != nil {
			return SealedValue{}, err
		}
		return SealedValue{SMKVersion: m.version, Ciphertext: ct, Nonce: nonce}, nil
	}
}

// SetInternal creates or updates an OpenDeploy-managed internal secret. Internal
// secrets are encrypted by the same SMK but are hidden from user-facing CRUD
// and keep the name-bound AAD (they sit outside the file system and are not
// versioned).
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
	ct, nonce, err := aeadSeal(m.smk, value, systemSecretAAD(name))
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

// Rename renames the stable secret identity. The AAD binds the identity id,
// not the name, so no re-encryption happens.
func (m *Manager) Rename(secretID int32, newName string) error {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return errors.New("secret name is required")
	}
	if isReservedInternalName(newName) {
		return ErrReservedName
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.smk == nil {
		return ErrLocked
	}
	if err := m.store.RenameSecret(secretID, newName); err != nil {
		return err
	}
	for id, rec := range m.cache {
		if rec.SecretID == secretID {
			rec.Name = newName
			m.cache[id] = rec
		}
	}
	slog.Info("renamed secret", "id", secretID, "newName", newName)
	return nil
}

// MoveSpace moves the stable secret identity to another space, landing it in
// directoryID there (0 = the destination space's root). The AAD binds the
// identity id, not the space, so no re-encryption happens — but cached version
// records denormalize the space, and authz decisions read it, so the cache is
// fixed up here. Safe to call while locked (no decryption needed).
func (m *Manager) MoveSpace(secretID, newSpaceID, directoryID int32) error {
	if newSpaceID <= 0 {
		newSpaceID = defaultUserSpaceID
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.MoveSecretSpace(secretID, newSpaceID, directoryID); err != nil {
		return err
	}
	for id, rec := range m.cache {
		if rec.SecretID == secretID {
			rec.SpaceID = newSpaceID
			m.cache[id] = rec
		}
	}
	slog.Info("moved secret to space", "id", secretID, "spaceID", newSpaceID)
	return nil
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
	pt, err := aeadOpen(m.smk, rec.Ciphertext, rec.Nonce, systemSecretAAD(name))
	if err != nil {
		return nil, fmt.Errorf("decrypting internal secret %q: %w", name, err)
	}
	return pt, nil
}

// Delete removes a user secret with all its versions. Safe to call while
// locked (no decryption needed).
func (m *Manager) Delete(secretID int32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.DeleteSecret(secretID); err != nil {
		return err
	}
	for id, rec := range m.cache {
		if rec.SecretID == secretID {
			delete(m.cache, id)
		}
	}
	return nil
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
	if _, randomErr := rand.Read(salt); randomErr != nil {
		return "", randomErr
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
	machineKey, err := m.machineKey.Establish()
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
	machineKey, err := m.machineKey.Load()
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
	return machinekey.Seal(key, plaintext, aad)
}

func aeadOpen(key, ciphertext, nonce, aad []byte) ([]byte, error) {
	return machinekey.Open(key, ciphertext, nonce, aad)
}

func slotAAD(slot string) []byte { return []byte("opendeploy-keyslot:" + slot) }

// userSecretAAD binds a user secret's ciphertext to its stable identity id and
// version number. Both are immutable and known before the row is inserted, so
// renames and directory moves never re-encrypt, while a ciphertext still
// cannot be moved to another secret or another version of the same secret.
func userSecretAAD(secretID, version int32) []byte {
	return []byte(fmt.Sprintf("opendeploy-secret:user:s%d:v%d", secretID, version))
}

func systemSecretAAD(name string) []byte {
	return []byte("opendeploy-secret:system:" + name)
}

func isReservedInternalName(name string) bool {
	name = strings.TrimSpace(name)
	if name == TLSCertPEMSecretName {
		return false
	}
	return strings.HasPrefix(name, "opendeploy.") && !strings.HasPrefix(name, "opendeploy.config.")
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
		ID:        r.ID,
		SecretID:  r.SecretID,
		Name:      r.Name,
		Version:   r.Version,
		SpaceID:   r.SpaceID,
		CreatedAt: time.UnixMilli(r.CreatedAt),
		Author:    r.Author,
	}
}
