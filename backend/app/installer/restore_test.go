package installer

import (
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"net/netip"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/util/stringu"
)

func TestApplyRestoredSystemConfigOverrides(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := state.Open(dbPath)
	service, err := systemconfig.InitializeService(store, *systemconfig.Default(systemconfig.DefaultInitial()))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	settings := systemconfig.DefaultSettings(systemconfig.DefaultInitial())
	settings.HttpWeb.Listen = systemconfig.StringLiteral("10.0.0.1:443")
	if updateErr := service.UpdateSettings(*settings, 0, nil); updateErr != nil {
		t.Fatalf("seed web listen: %v", updateErr)
	}
	if closeErr := store.Close(); closeErr != nil {
		t.Fatalf("close seed store: %v", closeErr)
	}

	httpOnly := true
	webListen := ":8443"
	clusterListen := ":9443"
	enrollmentListen := ":9444"
	acmeHostList := "new.example.com"
	err = applyRestoredSystemConfigOverrides(dbPath, installOptions{
		httpOnly:         &httpOnly,
		webListen:        &webListen,
		clusterListen:    &clusterListen,
		enrollmentListen: &enrollmentListen,
		acmeHosts:        &acmeHostList,
	}, noChown)
	if err != nil {
		t.Fatalf("apply overrides: %v", err)
	}

	store = state.Open(dbPath)
	defer store.Close()
	service, err = systemconfig.NewService(store)
	if err != nil {
		t.Fatalf("NewService reopen: %v", err)
	}
	cfg := service.Snapshot()
	if got := service.MustLoadStringSetting(cfg.Settings.HttpWeb.Listen); got != ":8443" {
		t.Fatalf("WebHTTP.Listen = %q, want :8443", got)
	}
	if !service.MustLoadBoolSetting(cfg.Settings.HttpWeb.Enabled) {
		t.Fatal("WebHTTP.Enabled = false, want true")
	}
	if service.MustLoadBoolSetting(cfg.Settings.HttpsWeb.Enabled) {
		t.Fatal("WebHTTPS.Enabled = true, want false")
	}
	if got := service.MustLoadStringSetting(cfg.Settings.Cluster.Listen); got != ":9443" {
		t.Fatalf("ClusterListen = %q, want :9443", got)
	}
	if got := service.MustLoadStringSetting(cfg.Settings.Cluster.EnrollmentListen); got != ":9444" {
		t.Fatalf("EnrollmentListen = %q, want :9444", got)
	}
	if got := stringu.ParseStringList(service.MustLoadStringSetting(cfg.Settings.HttpsWeb.AcmeHosts)); len(got) != 1 || got[0] != "new.example.com" {
		t.Fatalf("AcmeHosts = %#v, want [new.example.com]", got)
	}
}

func TestInvalidateRestoredPrimaryRuntimeState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := state.Open(dbPath)
	nodes.EnsurePrimaryNode(store, "primary", "primary-id", netip.MustParseAddr("192.0.2.1"))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := invalidateRestoredPrimaryRuntimeState(dbPath, noChown); err != nil {
		t.Fatal(err)
	}
}
