package primarybootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
	"github.com/jptrs93/opsagent/backend/util/certu"
)

func TestInitializeCreatesCompletePrimaryState(t *testing.T) {
	dir := t.TempDir()
	initial := config.DefaultInitialConfig()
	initial.MasterPasswordHash = "test-hash"
	initial.WebTLSSelfManaged = true
	service := Service{DataDir: dir}
	result, err := service.Initialize(context.Background(), Options{
		Initial:     initial,
		PrimaryName: "primary.test",
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if !strings.HasPrefix(result.EnrollmentFingerprint, "sha256:") {
		t.Fatalf("EnrollmentFingerprint = %q", result.EnrollmentFingerprint)
	}
	if err := service.Validate(context.Background()); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	machineKeyPath := filepath.Join(dir, "machine.key")
	info, err := os.Stat(machineKeyPath)
	if err != nil {
		t.Fatalf("stat machine.key: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("machine.key mode = %o, want 600", got)
	}

	store := sqlite.NewPrimaryStorage(filepath.Join(dir, "primary.db"))
	defer store.Close()
	configService, err := config.NewService(store)
	if err != nil {
		t.Fatalf("config.NewService: %v", err)
	}
	if got := configService.Snapshot().MasterPasswordHash; got != "test-hash" {
		t.Fatalf("MasterPasswordHash = %q, want test-hash", got)
	}
	secretsMgr, err := secrets.Open(dir, store)
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}
	clusterMaterial, err := certu.LoadPrimary(secretsMgr)
	if err != nil {
		t.Fatalf("certu.LoadPrimary: %v", err)
	}
	nodes := store.ListNodes()
	if len(nodes) != 1 || nodes[0].Name != "primary" {
		t.Fatalf("primary nodes = %+v, want one named primary", nodes)
	}
	if nodes[0].Identifier == "" || nodes[0].Identifier == "primary.test" {
		t.Fatalf("primary identifier = %q, want generated value", nodes[0].Identifier)
	}
	if got := certu.MustCertCommonNameFromPEM(clusterMaterial.PrimaryCert); got != nodes[0].Identifier {
		t.Fatalf("primary certificate CN = %q, want node identifier %q", got, nodes[0].Identifier)
	}
	if bundle, err := certu.LoadWebUISelfSigned(secretsMgr); err != nil {
		t.Fatalf("certu.LoadWebUISelfSigned: %v", err)
	} else if len(bundle) == 0 {
		t.Fatal("self-managed Web TLS bundle is empty")
	}
}

func TestInitializeRejectsExistingDatabase(t *testing.T) {
	dir := t.TempDir()
	initial := config.DefaultInitialConfig()
	initial.MasterPasswordHash = "test-hash"
	service := Service{DataDir: dir}
	if _, err := service.Initialize(context.Background(), Options{Initial: initial, PrimaryName: "primary"}); err != nil {
		t.Fatalf("first Initialize: %v", err)
	}
	if _, err := service.Initialize(context.Background(), Options{Initial: initial, PrimaryName: "primary"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second Initialize error = %v, want already exists", err)
	}
}
