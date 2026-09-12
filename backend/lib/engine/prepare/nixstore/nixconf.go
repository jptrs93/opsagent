package nixstore

import (
	"os"
	"path/filepath"
)

// NixConf is the configuration every build and maintenance container runs
// with. OpenDeploy owns it; flakes cannot change it.
const NixConf = `experimental-features = nix-command flakes
sandbox = false
build-users-group = nixbld
substituters = https://cache.nixos.org/
trusted-public-keys = cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=
max-jobs = auto
`

func (m *Manager) writeNixConf() error {
	path := m.NixConfPath()
	if current, err := os.ReadFile(path); err == nil && string(current) == NixConf {
		return nil
	}
	tmp := filepath.Join(filepath.Dir(path), ".nix.conf.tmp")
	if err := os.WriteFile(tmp, []byte(NixConf), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
