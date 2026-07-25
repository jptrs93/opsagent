package config

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

func TestMasterPasswordHashRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	service, err := InitializeService(sqlite.NewPrimaryStorage(dbPath), *DefaultConfig(DefaultInitialConfig()))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if setErr := service.SetMasterPasswordHash("hash-1"); setErr != nil {
		t.Fatalf("SetMasterPasswordHash: %v", setErr)
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

func TestVersionedConfigSnapshotsRedactMasterPasswordHash(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	initial := DefaultInitialConfig()
	initial.MasterPasswordHash = "initial-hash"
	service, err := InitializeService(sqlite.NewPrimaryStorage(dbPath), *DefaultConfig(initial))
	if err != nil {
		t.Fatalf("InitializeService: %v", err)
	}

	sub := service.VersionedSnapshotAndSubscribe()
	defer sub.UnsubscribeFunc()
	if !sub.InitialValueValid {
		t.Fatal("initial versioned config snapshot is missing")
	}
	if sub.InitialValue.Version != service.VersionID() {
		t.Fatalf("initial version = %d, want %d", sub.InitialValue.Version, service.VersionID())
	}
	if sub.InitialValue.UpdatedAt.IsZero() {
		t.Fatal("initial updated_at is zero")
	}
	initialRow, err := service.Storage.FetchLatestOpenDeployConfig()
	if err != nil {
		t.Fatalf("FetchLatestOpenDeployConfig: %v", err)
	}
	if !sub.InitialValue.UpdatedAt.Equal(time.UnixMilli(initialRow.UpdatedAt)) {
		t.Fatalf("initial updated_at = %v, want %v", sub.InitialValue.UpdatedAt, time.UnixMilli(initialRow.UpdatedAt))
	}
	if sub.InitialValue.Config.MasterPasswordHash != "" {
		t.Fatalf("initial master password hash = %q, want empty", sub.InitialValue.Config.MasterPasswordHash)
	}

	if err := service.SetMasterPasswordHash("changed-hash"); err != nil {
		t.Fatalf("SetMasterPasswordHash: %v", err)
	}
	select {
	case got := <-sub.Ch:
		if got.Version != service.VersionID() {
			t.Fatalf("update version = %d, want %d", got.Version, service.VersionID())
		}
		if got.UpdatedAt.IsZero() {
			t.Fatal("update updated_at is zero")
		}
		if got.Config.MasterPasswordHash != "" {
			t.Fatalf("update master password hash = %q, want empty", got.Config.MasterPasswordHash)
		}
		row, err := service.Storage.FetchLatestOpenDeployConfig()
		if err != nil {
			t.Fatalf("FetchLatestOpenDeployConfig: %v", err)
		}
		if !got.UpdatedAt.Equal(time.UnixMilli(row.UpdatedAt)) {
			t.Fatalf("update updated_at = %v, want %v", got.UpdatedAt, time.UnixMilli(row.UpdatedAt))
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for versioned config update")
	}
}

func TestNewServiceRejectsUninitializedConfig(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	if _, err := NewService(store); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("NewService error = %v, want not initialized", err)
	}
}

func TestEnsureInitialSettingsPersistedIncludesMasterPasswordHash(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := sqlite.NewPrimaryStorage(dbPath)
	initial := DefaultInitialConfig()
	initial.MasterPasswordHash = "initial-hash"
	service, err := InitializeService(store, *DefaultConfig(initial))
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

	if setErr := service.SetMasterPasswordHash("changed-hash"); setErr != nil {
		t.Fatalf("SetMasterPasswordHash: %v", setErr)
	}
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
	secretMgr, err := secrets.Initialize(dir, store)
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}
	secretMeta, err := secretMgr.Set("opendeploy.config.github_token", []byte("ghp_test"), 0)
	if err != nil {
		t.Fatalf("Set secret: %v", err)
	}
	service, err := InitializeService(store, *DefaultConfig(DefaultInitialConfig()))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	settings := DefaultSettings(DefaultInitialConfig())
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
	store := sqlite.NewPrimaryStorage(dbPath)
	service, err := InitializeService(store, *DefaultConfig(DefaultInitialConfig()))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if service.Snapshot().Settings.Backup.Enabled.Value {
		t.Fatal("BackupEnabled default = true, want false")
	}
	if service.Snapshot().Settings.LargeAssets.UseSeparateS3.Value {
		t.Fatal("LargeAssets.UseSeparateS3 default = true, want false")
	}
	settings := DefaultSettings(DefaultInitialConfig())
	settings.Backup.Enabled = apigen.BoolSetting{Value: true}
	if err := service.UpdateSettings(*settings); err != nil {
		t.Fatalf("UpdateSettings BackupEnabled: %v", err)
	}
	if !service.Snapshot().Settings.Backup.Enabled.Value {
		t.Fatal("BackupEnabled after update = false, want true")
	}
	select {
	case <-service.AssetMigrationWake():
	default:
		t.Fatal("BackupEnabled update did not wake the asset migration worker")
	}
	migration, ok := store.GetUnfinishedAssetMigration()
	if !ok {
		t.Fatal("BackupEnabled update did not create an asset migration")
	}
	if migration.OldConfigVersionID == migration.NewConfigVersionID {
		t.Fatal("asset migration old and new config IDs are equal")
	}
	latestBeforeBlockedSave, err := store.FetchLatestOpenDeployConfig()
	if err != nil {
		t.Fatalf("FetchLatestOpenDeployConfig before blocked save: %v", err)
	}
	if err := service.UpdateSettings(service.Snapshot().Settings); !errors.Is(err, sqlite.ErrAssetMigrationInProgress) {
		t.Fatalf("UpdateSettings during migration error = %v, want ErrAssetMigrationInProgress", err)
	}
	latestAfterBlockedSave, err := store.FetchLatestOpenDeployConfig()
	if err != nil {
		t.Fatalf("FetchLatestOpenDeployConfig after blocked save: %v", err)
	}
	if latestAfterBlockedSave.ID != latestBeforeBlockedSave.ID {
		t.Fatalf("blocked settings save created config %d, previous latest was %d", latestAfterBlockedSave.ID, latestBeforeBlockedSave.ID)
	}
	if err := service.SetMasterPasswordHash("allowed-during-migration"); err != nil {
		t.Fatalf("SetMasterPasswordHash during migration: %v", err)
	}
}

func TestStoredSettingsPreserveConfigRefWithoutResolution(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := sqlite.NewPrimaryStorage(dbPath)
	service, err := InitializeService(store, *DefaultConfig(DefaultInitialConfig()))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	userCfg := store.SetUserConfig("shared.cluster.listen", ":9555", 0, 1)

	settings := DefaultSettings(DefaultInitialConfig())
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
	service, err := InitializeService(sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db")), *DefaultConfig(DefaultInitialConfig()))
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
