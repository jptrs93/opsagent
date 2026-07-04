package config

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/secrets"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

func TestMasterPasswordHashRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	service, err := NewService(sqlite.NewPrimaryStorage(dbPath))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if err := service.SetMasterPasswordHash("hash-1"); err != nil {
		t.Fatalf("SetMasterPasswordHash: %v", err)
	}
	hash, err := service.GetMasterPasswordHash()
	if err != nil {
		t.Fatalf("GetMasterPasswordHash after set: %v", err)
	}
	if hash != "hash-1" {
		t.Fatalf("expected hash-1, got %q", hash)
	}

	reopened, err := NewService(sqlite.NewPrimaryStorage(dbPath))
	if err != nil {
		t.Fatalf("NewService reopen: %v", err)
	}
	hash, err = reopened.GetMasterPasswordHash()
	if err != nil {
		t.Fatalf("GetMasterPasswordHash after reopen: %v", err)
	}
	if hash != "hash-1" {
		t.Fatalf("expected persisted hash-1, got %q", hash)
	}
}

func TestEnsureInitialSettingsPersistedIncludesMasterPasswordHash(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := sqlite.NewPrimaryStorage(dbPath)
	old := ainit.StaticConfig.InitialMasterPasswordHash
	t.Cleanup(func() { ainit.StaticConfig.InitialMasterPasswordHash = old })
	ainit.StaticConfig.InitialMasterPasswordHash = "initial-hash"
	service, err := NewService(store)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	value, err := service.GetMasterPasswordHash()
	if err != nil {
		t.Fatalf("GetMasterPasswordHash: %v", err)
	}
	if value != "initial-hash" {
		t.Fatalf("persisted value = %q, want initial-hash", value)
	}

	if err := service.SetMasterPasswordHash("changed-hash"); err != nil {
		t.Fatalf("SetMasterPasswordHash: %v", err)
	}
	ainit.StaticConfig.InitialMasterPasswordHash = "new-initial-hash"
	value, err = service.GetMasterPasswordHash()
	if err != nil {
		t.Fatalf("GetMasterPasswordHash after set: %v", err)
	}
	if value != "changed-hash" {
		t.Fatalf("persisted value after set = %q, want changed-hash", value)
	}
}

func TestSecretConfigReferencesExistingSecret(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "primary.db")
	store := sqlite.NewPrimaryStorage(dbPath)
	secretMgr, err := secrets.Open(dir, store)
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}
	secretMeta, err := secretMgr.Set("opendeploy.config.github_token", []byte("ghp_test"), 0)
	if err != nil {
		t.Fatalf("Set secret: %v", err)
	}
	service, err := NewService(store)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	settings := DefaultSettings(ainit.StaticConfig)
	settings.Repo.GithubToken = apigen.SecretRef{ID: secretMeta.ID}
	if err := service.UpdateSettings(*settings); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	cfg := service.Snapshot()
	if cfg.Settings.Repo.GithubToken.ID != secretMeta.ID {
		t.Fatalf("GithubTokenSecretRef ID = %d, want %d", cfg.Settings.Repo.GithubToken.ID, secretMeta.ID)
	}
}

func TestBackupEnabledDefaultsFalseAndCanBeEnabled(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	service, err := NewService(sqlite.NewPrimaryStorage(dbPath))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if service.Snapshot().Settings.Backup.Enabled.Value {
		t.Fatal("BackupEnabled default = true, want false")
	}
	settings := DefaultSettings(ainit.StaticConfig)
	settings.Backup.Enabled = apigen.BoolSetting{Value: true}
	if err := service.UpdateSettings(*settings); err != nil {
		t.Fatalf("UpdateSettings BackupEnabled: %v", err)
	}
	if !service.Snapshot().Settings.Backup.Enabled.Value {
		t.Fatal("BackupEnabled after update = false, want true")
	}
}

func TestStoredSettingsPreserveConfigRefWithoutResolution(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := sqlite.NewPrimaryStorage(dbPath)
	service, err := NewService(store)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	userCfg := store.SetUserConfig("shared.cluster.listen", ":9555", 0, 1)

	settings := DefaultSettings(ainit.StaticConfig)
	settings.Cluster.Listen = apigen.StringSetting{ConfigRef: apigen.ConfigRef{ID: userCfg.ID}}
	if err := service.UpdateSettings(*settings); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	cfg := service.Snapshot()
	if cfg.Settings.Cluster.Listen.Value != "" {
		t.Fatalf("ClusterListen value = %q, want empty stored value", cfg.Settings.Cluster.Listen.Value)
	}
	if cfg.Settings.Cluster.Listen.ConfigRef.ID != userCfg.ID {
		t.Fatalf("Cluster.Listen.ConfigRef.ID = %d, want %d", cfg.Settings.Cluster.Listen.ConfigRef.ID, userCfg.ID)
	}
}

func TestWebUIDefaultsPreserveExistingHTTPSInstall(t *testing.T) {
	old := ainit.StaticConfig
	t.Cleanup(func() { ainit.StaticConfig = old })
	ainit.StaticConfig.InitialWebHTTPEnabled = false
	ainit.StaticConfig.InitialWebHTTPListen = ":8080"
	ainit.StaticConfig.InitialWebHTTPSEnabled = true
	ainit.StaticConfig.InitialWebHTTPSListen = ":443"

	service, err := NewService(sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db")))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	cfg := service.Snapshot()

	if !cfg.Settings.HttpsWeb.Enabled.Value {
		t.Fatal("WebHTTPSEnabled default = false, want true")
	}
	if cfg.Settings.HttpsWeb.Listen.Value != ":443" {
		t.Fatalf("WebHTTPSListen default = %q, want :443", cfg.Settings.HttpsWeb.Listen.Value)
	}
	if cfg.Settings.HttpWeb.Enabled.Value {
		t.Fatal("WebHTTPEnabled default = true, want false")
	}
	if cfg.Settings.HttpWeb.Listen.Value != ":8080" {
		t.Fatalf("WebHTTPListen default = %q, want :8080", cfg.Settings.HttpWeb.Listen.Value)
	}
}
