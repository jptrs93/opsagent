package primary

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
	"github.com/jptrs93/opsagent/backend/util/certu"
)

func TestInitialWebTLSCertPEMHookCreatesSecretRef(t *testing.T) {
	dir := t.TempDir()
	store := sqlite.NewPrimaryStorage(filepath.Join(dir, "primary.db"))
	secretMgr, err := secrets.Open(dir, store)
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}

	certPEM, keyPEM, err := certu.GenerateSelfSignedServerCertificate([]string{"primary.opendeploy.test"})
	if err != nil {
		t.Fatalf("GenerateSelfSignedServerCertificate: %v", err)
	}
	bundle := append(certPEM, keyPEM...)

	oldCertPEM := ainit.StaticConfig.InitialWebTLSCertPEM
	oldCertPEMFile := ainit.StaticConfig.InitialWebTLSCertPEMFile
	oldSelfManaged := ainit.StaticConfig.InitialWebTLSSelfManaged
	t.Cleanup(func() {
		ainit.StaticConfig.InitialWebTLSCertPEM = oldCertPEM
		ainit.StaticConfig.InitialWebTLSCertPEMFile = oldCertPEMFile
		ainit.StaticConfig.InitialWebTLSSelfManaged = oldSelfManaged
	})
	ainit.StaticConfig.InitialWebTLSCertPEM = strings.ReplaceAll(string(bundle), "\n", `\n`)
	ainit.StaticConfig.InitialWebTLSCertPEMFile = ""
	ainit.StaticConfig.InitialWebTLSSelfManaged = false

	service, err := config.NewServiceWithInitialConfigHook(store, initialWebTLSCertPEMHook(secretMgr))
	if err != nil {
		t.Fatalf("NewServiceWithInitialConfigHook: %v", err)
	}

	settings := service.Snapshot().Settings
	if !settings.HttpsWeb.TlsSelfManaged.Value {
		t.Fatal("TlsSelfManaged = false, want true")
	}
	if settings.HttpsWeb.TlsCertPem.ID == 0 {
		t.Fatal("TlsCertPem.ID = 0, want secret ref")
	}
	meta, ok := secretMgr.MetaByID(settings.HttpsWeb.TlsCertPem.ID)
	if !ok || meta.Name != secrets.TLSCertPEMSecretName {
		t.Fatalf("TLS cert secret meta = %#v, %v; want %q", meta, ok, secrets.TLSCertPEMSecretName)
	}
	stored, err := secretMgr.RevealByID(settings.HttpsWeb.TlsCertPem.ID)
	if err != nil {
		t.Fatalf("RevealByID: %v", err)
	}
	if !bytes.Equal(stored, bundle) {
		t.Fatal("stored TLS cert bundle did not match initial PEM")
	}
}

func TestInitialWebTLSCertPEMHookReadsAndRemovesOneShotFile(t *testing.T) {
	dir := t.TempDir()
	store := sqlite.NewPrimaryStorage(filepath.Join(dir, "primary.db"))
	secretMgr, err := secrets.Open(dir, store)
	if err != nil {
		t.Fatalf("secrets.Open: %v", err)
	}

	certPEM, keyPEM, err := certu.GenerateSelfSignedServerCertificate([]string{"primary.opendeploy.test"})
	if err != nil {
		t.Fatalf("GenerateSelfSignedServerCertificate: %v", err)
	}
	bundle := append(certPEM, keyPEM...)
	certPath := filepath.Join(dir, initialWebTLSCertPEMFileName)
	if writeErr := os.WriteFile(certPath, bundle, 0o600); writeErr != nil {
		t.Fatalf("WriteFile: %v", writeErr)
	}

	old := ainit.StaticConfig
	t.Cleanup(func() { ainit.StaticConfig = old })
	ainit.StaticConfig.DataDir = dir
	ainit.StaticConfig.InitialWebTLSCertPEM = ""
	ainit.StaticConfig.InitialWebTLSCertPEMFile = certPath
	ainit.StaticConfig.InitialWebTLSSelfManaged = false

	service, err := config.NewServiceWithInitialConfigHook(store, initialWebTLSCertPEMHook(secretMgr))
	if err != nil {
		t.Fatalf("NewServiceWithInitialConfigHook: %v", err)
	}
	if _, statErr := os.Stat(certPath); !os.IsNotExist(statErr) {
		t.Fatalf("one-shot cert file still exists or stat failed: %v", statErr)
	}
	stored, err := secretMgr.RevealByID(service.Snapshot().Settings.HttpsWeb.TlsCertPem.ID)
	if err != nil {
		t.Fatalf("RevealByID: %v", err)
	}
	if !bytes.Equal(bytes.TrimSpace(stored), bytes.TrimSpace(bundle)) {
		t.Fatal("stored TLS cert bundle did not match initial PEM file")
	}
}
