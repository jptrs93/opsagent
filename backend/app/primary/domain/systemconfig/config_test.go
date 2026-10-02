package systemconfig

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func TestMasterPasswordHashRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	service, err := InitializeService(state.Open(dbPath), *Default(DefaultInitial()))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if setErr := service.SetMasterPasswordHash("hash-1", 0); setErr != nil {
		t.Fatalf("SetMasterPasswordHash: %v", setErr)
	}
	hash, err := service.GetMasterPasswordHash()
	if err != nil {
		t.Fatalf("GetMasterPasswordHash after set: %v", err)
	}
	if hash != "hash-1" {
		t.Fatalf("expected hash-1, got %q", hash)
	}

	reopened, err := NewService(state.Open(dbPath))
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

func TestConfigSubscriptionDeliversPersistedRevisions(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	initial := DefaultInitial()
	initial.MasterPasswordHash = "initial-hash"
	service, err := InitializeService(state.Open(dbPath), *Default(initial))
	if err != nil {
		t.Fatalf("InitializeService: %v", err)
	}
	persisted := func() (pq.SystemConfigRevision, apigen.SystemConfig) {
		t.Helper()
		row, err := LatestRevision(service.Storage.Queries())
		if err != nil {
			t.Fatalf("LatestRevision: %v", err)
		}
		return row, normalizeConfig(*erru.Must(apigen.DecodeSystemConfig(row.ConfigBlob)))
	}

	sub := service.SnapshotAndSubscribe(nil)
	defer sub.Unsubscribe()
	if !sub.InitialValueValid {
		t.Fatal("initial config is missing")
	}
	initialRow, stored := persisted()
	if service.VersionID() != initialRow.Seq || initialRow.UpdatedAt == 0 {
		t.Fatalf("version id = %d, want revision %d with a timestamp", service.VersionID(), initialRow.Seq)
	}
	if sub.InitialValue.MasterPasswordHash != "initial-hash" || !reflect.DeepEqual(sub.InitialValue, stored) {
		t.Fatalf("initial config = %+v, want the persisted revision %+v", sub.InitialValue, stored)
	}

	if err := service.SetMasterPasswordHash("changed-hash", 0); err != nil {
		t.Fatalf("SetMasterPasswordHash: %v", err)
	}
	select {
	case got := <-sub.Ch:
		row, stored := persisted()
		if row.Seq == initialRow.Seq || service.VersionID() != row.Seq {
			t.Fatalf("version id = %d, want the new revision %d after %d", service.VersionID(), row.Seq, initialRow.Seq)
		}
		if got.MasterPasswordHash != "changed-hash" || !reflect.DeepEqual(got, stored) {
			t.Fatalf("update = %+v, want the persisted revision %+v", got, stored)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for config update")
	}
}

func TestNewServiceRejectsUninitializedConfig(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	if _, err := NewService(store); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("NewService error = %v, want not initialized", err)
	}
}

func TestEnsureInitialSettingsPersistedIncludesMasterPasswordHash(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := state.Open(dbPath)
	initial := DefaultInitial()
	initial.MasterPasswordHash = "initial-hash"
	service, err := InitializeService(store, *Default(initial))
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

	if setErr := service.SetMasterPasswordHash("changed-hash", 0); setErr != nil {
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
	store := state.Open(dbPath)
	secretMgr, err := secrets.Initialize(dir, store)
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}
	secretMeta, err := secretMgr.Create("opendeploy.config.github_token", []byte("ghp_test"), 0, 0, 0)
	if err != nil {
		t.Fatalf("Set secret: %v", err)
	}
	service, err := InitializeService(store, *Default(DefaultInitial()))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	settings := DefaultSettings(DefaultInitial())
	settings.Repo.GithubToken = apigen.SecretRef{Ref: secretMeta.Ref()}
	if err := service.UpdateSettings(*settings, 0, nil); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	cfg := service.Snapshot()
	if cfg.Settings.Repo.GithubToken.Ref != secretMeta.Ref() {
		t.Fatalf("GithubTokenSecretRef = %v, want %v", cfg.Settings.Repo.GithubToken.Ref, secretMeta.Ref())
	}
}

func TestBackupEnabledDefaultsFalseAndCanBeEnabled(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := state.Open(dbPath)
	service, err := InitializeService(store, *Default(DefaultInitial()))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	if service.Snapshot().Settings.Backup.Enabled.Value {
		t.Fatal("BackupEnabled default = true, want false")
	}
	if service.Snapshot().Settings.LargeAssets.UseSeparateS3.Value {
		t.Fatal("LargeAssets.UseSeparateS3 default = true, want false")
	}
	settings := DefaultSettings(DefaultInitial())
	settings.Backup.Enabled = apigen.BoolSetting{Value: true}
	if err := service.UpdateSettings(*settings, 0, nil); err != nil {
		t.Fatalf("UpdateSettings BackupEnabled: %v", err)
	}
	if !service.Snapshot().Settings.Backup.Enabled.Value {
		t.Fatal("BackupEnabled after update = false, want true")
	}
	select {
	case <-service.AssetTargetWake():
	default:
		t.Fatal("BackupEnabled update did not wake the asset reconciler")
	}
	if err := service.UpdateSettings(service.Snapshot().Settings, 0, nil); err != nil {
		t.Fatalf("UpdateSettings with the same target: %v", err)
	}
	select {
	case <-service.AssetTargetWake():
		t.Fatal("a save that keeps the storage target woke the asset reconciler")
	default:
	}
	if err := service.SetMasterPasswordHash("rotated", 0); err != nil {
		t.Fatalf("SetMasterPasswordHash: %v", err)
	}
}

func TestStoredSettingsPreserveConfigRefWithoutResolution(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := state.Open(dbPath)
	service, err := InitializeService(store, *Default(DefaultInitial()))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	userCfgMeta, err := values.CreateConfig(store, "shared.cluster.listen", nodes.DefaultSpaceID, 0, 0, ":9555")
	if err != nil {
		t.Fatalf("CreateConfigWithVersion: %v", err)
	}
	userCfg := statetest.ValueVersions(store, userCfgMeta)[0]

	settings := DefaultSettings(DefaultInitial())
	settings.Cluster.Listen = apigen.StringSetting{ConfigRef: apigen.ConfigRef{Ref: userCfg.Ref}}
	if err := service.UpdateSettings(*settings, 0, nil); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	cfg := service.Snapshot()
	if cfg.Settings.Cluster.Listen.Value != "" {
		t.Fatalf("ClusterListen value = %q, want empty stored value", cfg.Settings.Cluster.Listen.Value)
	}
	if cfg.Settings.Cluster.Listen.ConfigRef.Ref != userCfg.Ref {
		t.Fatalf("Cluster.Listen.ConfigRef.Ref = %v, want %v", cfg.Settings.Cluster.Listen.ConfigRef.Ref, userCfg.Ref)
	}
}

func TestWebUIDefaultsPreserveExistingHTTPSInstall(t *testing.T) {
	service, err := InitializeService(state.Open(filepath.Join(t.TempDir(), "primary.db")), *Default(DefaultInitial()))
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

func TestKeepLocalCopyTogglesWakeTheReconcilerOnlyWhileBackupEnabled(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := state.Open(dbPath)
	service, err := InitializeService(store, *Default(DefaultInitial()))
	if err != nil {
		t.Fatalf("InitializeService: %v", err)
	}
	if service.Snapshot().Settings.LargeAssets.KeepLocalCopy.Value {
		t.Fatal("LargeAssets.KeepLocalCopy default = true, want false")
	}
	woke := func() bool {
		select {
		case <-service.AssetTargetWake():
			return true
		default:
			return false
		}
	}

	settings := DefaultSettings(DefaultInitial())
	settings.LargeAssets.KeepLocalCopy = apigen.BoolSetting{Value: true}
	if err := service.UpdateSettings(*settings, 0, nil); err != nil {
		t.Fatalf("UpdateSettings keep local while backup disabled: %v", err)
	}
	if woke() {
		t.Fatal("keep local toggle while backup is disabled woke the reconciler")
	}

	settings.Backup.Enabled = apigen.BoolSetting{Value: true}
	if err := service.UpdateSettings(*settings, 0, nil); err != nil {
		t.Fatalf("UpdateSettings enable backup: %v", err)
	}
	if !woke() {
		t.Fatal("enabling backup did not wake the reconciler")
	}

	settings.LargeAssets.KeepLocalCopy = apigen.BoolSetting{Value: false}
	if err := service.UpdateSettings(*settings, 0, nil); err != nil {
		t.Fatalf("UpdateSettings clear keep local while backup enabled: %v", err)
	}
	if !woke() {
		t.Fatal("keep local toggle while backup is enabled did not wake the reconciler")
	}
}
