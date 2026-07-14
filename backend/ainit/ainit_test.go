package ainit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureStaticDirsSetsPaths(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "opendeploy")
	var cfg StaticConfiguration
	ensureStaticDirs(CommandNetproxy, &cfg, dataDir)

	wants := map[string]string{
		"data":           dataDir,
		"assets":         dataDir + "-assets",
		"large assets":   filepath.Join(dataDir, "large-assets"),
		"git metadata":   filepath.Join(dataDir, "git-cache", "metadata"),
		"git worktrees":  filepath.Join(dataDir, "git-cache", "worktrees"),
		"netproxy state": filepath.Join(dataDir, "netproxy", "netstate.pb"),
		"readiness":      filepath.Join(dataDir, "readiness"),
		"resolvconf":     filepath.Join(dataDir, "resolvconf"),
		"tls":            filepath.Join(dataDir, "tls"),
		"acme":           filepath.Join(dataDir, ".certs"),
	}
	gots := map[string]string{
		"data":           cfg.DataDir,
		"assets":         cfg.AssetCacheDir,
		"large assets":   cfg.LargeAssetsDir,
		"git metadata":   cfg.GitMetadataDir,
		"git worktrees":  cfg.GitWorktreesDir,
		"netproxy state": cfg.NetproxyStatePath,
		"readiness":      cfg.ReadinessDir,
		"resolvconf":     cfg.ResolvConfDir,
		"tls":            cfg.TLSDir,
		"acme":           cfg.ACMECacheDir,
	}
	for name, want := range wants {
		if got := gots[name]; got != want {
			t.Errorf("%s path = %q, want %q", name, got, want)
		}
	}
}

func TestEnsureStaticDirsCreatesCompleteAgentLayout(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "opendeploy")
	var cfg StaticConfiguration
	ensureStaticDirs(CommandSecondary, &cfg, dataDir)

	for _, path := range []string{
		cfg.DataDir,
		cfg.AssetCacheDir,
		cfg.GitMetadataDir,
		cfg.GitWorktreesDir,
		cfg.NetproxyDir,
		cfg.ReadinessDir,
		cfg.ResolvConfDir,
		cfg.LargeAssetsDir,
		cfg.ACMECacheDir,
		cfg.TLSDir,
	} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Errorf("static directory %q was not created: %v", path, err)
		}
	}
}

func TestEnsureStaticDirsSkipsDataplane(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "opendeploy")
	var cfg StaticConfiguration
	ensureStaticDirs(CommandNetproxy, &cfg, dataDir)
	if _, err := os.Stat(cfg.DataDir); !os.IsNotExist(err) {
		t.Fatalf("dataplane created runtime directories: %v", err)
	}
}
