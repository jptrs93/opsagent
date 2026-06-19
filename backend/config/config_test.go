package config

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/secrets"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

func TestMasterPasswordHashRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	service := &Service{Storage: sqlite.NewPrimaryStorage(dbPath)}

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

	reopened := &Service{Storage: sqlite.NewPrimaryStorage(dbPath)}
	hash, err = reopened.GetMasterPasswordHash()
	if err != nil {
		t.Fatalf("GetMasterPasswordHash after reopen: %v", err)
	}
	if hash != "hash-1" {
		t.Fatalf("expected persisted hash-1, got %q", hash)
	}
}

func TestEnsureInitialMasterPasswordHashPersisted(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := sqlite.NewPrimaryStorage(dbPath)
	service := &Service{Storage: store}
	old := ainit.StaticConfig.InitialMasterPasswordHash
	t.Cleanup(func() { ainit.StaticConfig.InitialMasterPasswordHash = old })
	ainit.StaticConfig.InitialMasterPasswordHash = "initial-hash"

	if err := service.EnsureInitialMasterPasswordHashPersisted(); err != nil {
		t.Fatalf("EnsureInitialMasterPasswordHashPersisted: %v", err)
	}
	value, configured, err := store.FetchConfigValue(string(MasterPasswordHash))
	if err != nil {
		t.Fatalf("FetchConfigValue: %v", err)
	}
	if !configured || value != "initial-hash" {
		t.Fatalf("persisted value = %q configured=%t, want initial-hash true", value, configured)
	}

	if err := service.SetMasterPasswordHash("changed-hash"); err != nil {
		t.Fatalf("SetMasterPasswordHash: %v", err)
	}
	ainit.StaticConfig.InitialMasterPasswordHash = "new-initial-hash"
	if err := service.EnsureInitialMasterPasswordHashPersisted(); err != nil {
		t.Fatalf("EnsureInitialMasterPasswordHashPersisted after set: %v", err)
	}
	value, configured, err = store.FetchConfigValue(string(MasterPasswordHash))
	if err != nil {
		t.Fatalf("FetchConfigValue after set: %v", err)
	}
	if !configured || value != "changed-hash" {
		t.Fatalf("persisted value after set = %q configured=%t, want changed-hash true", value, configured)
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
	if _, err := secretMgr.Set("opendeploy.config.github_token", "config", []byte("ghp_test"), 0); err != nil {
		t.Fatalf("Set secret: %v", err)
	}
	service := &Service{Storage: store, Secrets: secretMgr}

	if err := service.UpdateValue(GithubToken, "opendeploy.config.github_token"); err != nil {
		t.Fatalf("UpdateValue: %v", err)
	}

	cfg := service.Snapshot()
	if cfg.GithubToken.Key() != "opendeploy.config.github_token" {
		t.Fatalf("GithubToken key = %q", cfg.GithubToken.Key())
	}
	value, err := cfg.GithubToken.Reveal()
	if err != nil || value != "ghp_test" {
		t.Fatalf("GithubToken reveal = %q, %v", value, err)
	}
}

func TestSecretConfigFallsBackToLegacyFixedSecret(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "primary.db")
	store := sqlite.NewPrimaryStorage(dbPath)
	secretMgr, err := secrets.Open(dir, store)
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}
	if _, err := secretMgr.Set("config.github_token", "config", []byte("legacy"), 0); err != nil {
		t.Fatalf("Set legacy secret: %v", err)
	}
	service := &Service{Storage: store, Secrets: secretMgr}

	cfg := service.Snapshot()
	if cfg.GithubToken.Key() != "config.github_token" {
		t.Fatalf("GithubToken key = %q", cfg.GithubToken.Key())
	}
	value, err := cfg.GithubToken.Reveal()
	if err != nil || value != "legacy" {
		t.Fatalf("GithubToken reveal = %q, %v", value, err)
	}
}

func TestBackupEnabledDefaultsFalseAndCanBeEnabled(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	service := &Service{Storage: sqlite.NewPrimaryStorage(dbPath)}

	if service.Snapshot().BackupEnabled {
		t.Fatal("BackupEnabled default = true, want false")
	}
	if err := service.UpdateValue(BackupEnabled, "true"); err != nil {
		t.Fatalf("UpdateValue BackupEnabled: %v", err)
	}
	if !service.Snapshot().BackupEnabled {
		t.Fatal("BackupEnabled after update = false, want true")
	}
}
