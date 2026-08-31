// Package wgkey owns the node-local WireGuard transport keypair. The private
// key is generated on the node at first boot, persisted as a 0600 file in the
// agent data directory, and never transits any channel or enters any database
// — only the derived public key is registered with the cluster. A machine that
// loses the file mints a fresh identity; transport keys are machine-bound,
// cluster state is not.
package wgkey

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const FileName = "wireguard.key"

// Key is the node's transport identity. Private stays on this machine;
// PublicBase64 is what enrollment and the cluster stream report.
type Key struct {
	Private wgtypes.Key
	Public  wgtypes.Key
}

func (k Key) PublicBase64() string { return k.Public.String() }

// LoadOrGenerate returns the node keypair from dir, generating and persisting
// a new one when no usable key file exists. A present-but-corrupt file is an
// error rather than a silent regeneration: replacing the identity of a node
// that still holds a readable-looking file should be an operator decision
// (delete the file), not a parse fallback.
func LoadOrGenerate(dir string) (Key, error) {
	path := filepath.Join(dir, FileName)
	raw, err := os.ReadFile(path)
	if err == nil {
		private, parseErr := wgtypes.ParseKey(strings.TrimSpace(string(raw)))
		if parseErr != nil {
			return Key{}, fmt.Errorf("parsing WireGuard key file %s: %w", path, parseErr)
		}
		return Key{Private: private, Public: private.PublicKey()}, nil
	}
	if !os.IsNotExist(err) {
		return Key{}, fmt.Errorf("reading WireGuard key file %s: %w", path, err)
	}
	private, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return Key{}, fmt.Errorf("generating WireGuard key: %w", err)
	}
	if err := os.WriteFile(path, []byte(private.String()+"\n"), 0o600); err != nil {
		return Key{}, fmt.Errorf("writing WireGuard key file %s: %w", path, err)
	}
	return Key{Private: private, Public: private.PublicKey()}, nil
}

// ValidatePublic reports whether s is a well-formed base64 Curve25519 public
// key and returns its canonical encoding. Every node must hold a transport
// key, so the empty string is invalid.
func ValidatePublic(s string) (string, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return "", fmt.Errorf("missing WireGuard public key")
	}
	key, err := wgtypes.ParseKey(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid WireGuard public key: %w", err)
	}
	return key.String(), nil
}
