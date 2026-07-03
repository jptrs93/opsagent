package installer

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/config"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
	"github.com/jptrs93/opsagent/backend/util/stringu"
)

func TestApplyRestoredPrimaryConfigOverrides(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := sqlite.NewPrimaryStorage(dbPath)
	service, err := config.NewService(store)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	settings := config.DefaultSettings(ainit.StaticConfig)
	settings.HttpWeb.Listen.Value = "10.0.0.1:443"
	if err := service.UpdateSettings(*settings); err != nil {
		t.Fatalf("seed web listen: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	httpOnly := true
	webListen := ":8443"
	clusterListen := ":9443"
	enrollmentListen := ":9444"
	acmeHosts := "new.example.com"
	err = applyRestoredPrimaryConfigOverrides(dbPath, installOptions{
		httpOnly:         &httpOnly,
		webListen:        &webListen,
		clusterListen:    &clusterListen,
		enrollmentListen: &enrollmentListen,
		acmeHosts:        &acmeHosts,
	}, noChown)
	if err != nil {
		t.Fatalf("apply overrides: %v", err)
	}

	store = sqlite.NewPrimaryStorage(dbPath)
	defer store.Close()
	service, err = config.NewService(store)
	if err != nil {
		t.Fatalf("NewService reopen: %v", err)
	}
	cfg := service.Snapshot()
	if got := cfg.Settings.HttpWeb.Listen.Value; got != ":8443" {
		t.Fatalf("WebHTTP.Listen = %q, want :8443", got)
	}
	if !cfg.Settings.HttpWeb.Enabled.Value {
		t.Fatal("WebHTTP.Enabled = false, want true")
	}
	if cfg.Settings.HttpsWeb.Enabled.Value {
		t.Fatal("WebHTTPS.Enabled = true, want false")
	}
	if got := cfg.Settings.Cluster.Listen.Value; got != ":9443" {
		t.Fatalf("ClusterListen = %q, want :9443", got)
	}
	if got := cfg.Settings.Cluster.EnrollmentListen.Value; got != ":9444" {
		t.Fatalf("EnrollmentListen = %q, want :9444", got)
	}
	if got := stringu.ParseStringList(cfg.Settings.HttpsWeb.AcmeHosts.Value); len(got) != 1 || got[0] != "new.example.com" {
		t.Fatalf("AcmeHosts = %#v, want [new.example.com]", got)
	}
}
