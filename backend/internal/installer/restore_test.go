package installer

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

func TestApplyRestoredPrimaryConfigOverrides(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := sqlite.NewPrimaryStorage(dbPath)
	if err := store.SetConfigValue(primaryConfigWebListen, "10.0.0.1:443"); err != nil {
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
	err := applyRestoredPrimaryConfigOverrides(dbPath, installOptions{
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
	assertConfigValue(t, store, primaryConfigWebListen, ":8443")
	assertConfigValue(t, store, primaryConfigWebHTTPOnly, "true")
	assertConfigValue(t, store, primaryConfigClusterListen, ":9443")
	assertConfigValue(t, store, primaryConfigEnrollmentListen, ":9444")
	assertConfigValue(t, store, primaryConfigAcmeHosts, "new.example.com")
}

func assertConfigValue(t *testing.T, store *sqlite.PrimaryStorage, key, want string) {
	t.Helper()
	got, configured, err := store.FetchConfigValue(key)
	if err != nil {
		t.Fatalf("fetch %s: %v", key, err)
	}
	if !configured {
		t.Fatalf("%s not configured", key)
	}
	if got != want {
		t.Fatalf("%s = %q; want %q", key, got, want)
	}
}
