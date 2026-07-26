// Package machinekey supplies the machine-scoped key-encryption key (KEK) that
// wraps whatever a node must be able to decrypt unattended after a reboot,
// together with the AEAD helpers that consume it.
//
// It is its own package because two independent callers need the same boundary:
// the primary's secrets store wraps its Secrets Master Key with a machine KEK,
// and a secondary encrypts its local copies of runtime inputs with one. Sharing
// a single Provider means the planned TPM-sealed implementation (see
// docs/engineering/secrets.md) covers both node types in one change instead of
// being retrofitted twice.
//
// How the KEK is protected at rest is the provider's private business — every
// caller only ever sees a 32-byte key — so an implementation is a drop-in and a
// TPM-less node falls back to the file provider transparently.
package machinekey

import (
	"crypto/rand"
	"os"

	"golang.org/x/crypto/chacha20poly1305"
)

// KeyLen is the size of a machine KEK, fixed by the AEAD that consumes it.
const KeyLen = chacha20poly1305.KeySize

// FileName is the conventional name of the file provider's key file inside a
// node's data directory.
const FileName = "machine.key"

// Provider supplies the machine KEK and persists whatever it needs to recover
// that KEK on this machine for unattended boot.
type Provider interface {
	// Establish creates and persists a fresh machine KEK, returning it.
	Establish() ([]byte, error)
	// Load returns the previously established machine KEK, or an error if it
	// cannot be recovered on this machine.
	Load() ([]byte, error)
}

// File is the default Provider: it keeps the KEK in a 0600 file in the data
// dir, outside the database and outside backups, so a leaked DB or backup
// cannot decrypt what the KEK wraps.
type File struct{ Path string }

func (f *File) Establish() ([]byte, error) {
	key := make([]byte, KeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(f.Path, key, 0o600); err != nil {
		return nil, err
	}
	// WriteFile does not chmod an existing file; enforce 0600 explicitly.
	if err := os.Chmod(f.Path, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func (f *File) Load() ([]byte, error) { return os.ReadFile(f.Path) }

// Seal encrypts plaintext under key. aad is bound into the tag, so a ciphertext
// cannot be moved to a different logical slot than the one it was sealed for.
func Seal(key, plaintext, aad []byte) (ciphertext, nonce []byte, err error) {
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

// Open reverses Seal.
func Open(key, ciphertext, nonce, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, nonce, ciphertext, aad)
}
