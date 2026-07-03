package configmigration

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/config"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

func TestMigrateOldConfig(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := sqlite.NewPrimaryStorage(dbPath)
	if err := store.SetLegacySystemConfigValue(string(ClusterListen), ":9555"); err != nil {
		t.Fatalf("SetConfigValue cluster: %v", err)
	}
	if err := store.SetLegacySystemConfigValue(string(GithubToken), "opendeploy.config.github_token"); err != nil {
		t.Fatalf("SetConfigValue github token: %v", err)
	}

	if err := MigrateOldConfig(store); err != nil {
		t.Fatalf("MigrateOldConfig: %v", err)
	}

	service, err := config.NewService(store)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	cfg := service.Snapshot()
	if got := cfg.Settings.Cluster.Listen.Value; got != ":9555" {
		t.Fatalf("ClusterListen = %q, want :9555", got)
	}
	if got := cfg.Settings.Repo.GithubToken.Key; got != "opendeploy.config.github_token" {
		t.Fatalf("GithubTokenSecretRef.Key = %q", got)
	}

	if err := MigrateOldConfig(store); err != nil {
		t.Fatalf("second MigrateOldConfig: %v", err)
	}
	if row, err := store.FetchLatestOpenDeployConfig(); err != nil {
		t.Fatalf("FetchLatestOpenDeployConfig: %v", err)
	} else if row.ID == 0 {
		t.Fatalf("settings row missing after migration id=%d", row.ID)
	}
}
