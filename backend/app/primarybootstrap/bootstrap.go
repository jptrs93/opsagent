package primarybootstrap

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/util/certu"
)

type Service struct {
	DataDir string
}

type Options struct {
	Initial       config.InitialConfig
	PrimaryName   string
	WebTLSCertPEM []byte
}

type Result struct {
	EnrollmentFingerprint string
}

func (s Service) Initialize(_ context.Context, opts Options) (*Result, error) {
	if err := validateOptions(opts); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(s.DataDir, "primary.db")
	if _, err := os.Stat(dbPath); err == nil {
		return nil, fmt.Errorf("primary database already exists at %s", dbPath)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("checking primary database: %w", err)
	}

	store := state.Open(dbPath)
	complete := false
	defer func() {
		_ = store.Close()
		if !complete {
			cleanupBootstrapArtifacts(s.DataDir)
		}
	}()
	secretsMgr, err := secrets.Initialize(s.DataDir, store)
	if err != nil {
		return nil, err
	}
	cfg := config.DefaultConfig(opts.Initial)
	if len(opts.WebTLSCertPEM) != 0 {
		meta, err := secretsMgr.SetByName(secrets.TLSCertPEMSecretName, opts.WebTLSCertPEM, 0)
		if err != nil {
			return nil, fmt.Errorf("storing initial Web TLS certificate: %w", err)
		}
		cfg.Settings.HttpsWeb.TlsSelfManaged = apigen.BoolSetting{Value: true}
		cfg.Settings.HttpsWeb.TlsCertPem = apigen.SecretRef{VersionID: meta.ID}
	}
	primaryIdentifier := uuid.NewString()
	store.EnsurePrimaryNode("primary", primaryIdentifier)
	clusterMaterial, err := certu.BootstrapPrimary(secretsMgr, primaryIdentifier, opts.PrimaryName)
	if err != nil {
		return nil, fmt.Errorf("initializing cluster TLS material: %w", err)
	}
	if cfg.Settings.HttpsWeb.Enabled.Value && cfg.Settings.HttpsWeb.TlsSelfManaged.Value && cfg.Settings.HttpsWeb.TlsCertPem.VersionID == 0 {
		if _, err := certu.BootstrapWebUISelfSigned(secretsMgr, certu.WebUISelfSignedNames(cfg.Settings.HttpsWeb.AcmeHosts.Value, cfg.Settings.HttpsWeb.Listen.Value)); err != nil {
			return nil, fmt.Errorf("initializing self-managed Web TLS material: %w", err)
		}
	}
	if _, err := config.InitializeService(store, *cfg); err != nil {
		return nil, err
	}
	fingerprint, err := certu.CertificatePEMSPKISHA256(clusterMaterial.PrimaryCert)
	if err != nil {
		return nil, fmt.Errorf("computing enrollment TLS fingerprint: %w", err)
	}
	if err := store.Close(); err != nil {
		return nil, fmt.Errorf("closing initialized primary database: %w", err)
	}
	complete = true
	return &Result{EnrollmentFingerprint: fingerprint}, nil
}

func (s Service) Validate(_ context.Context) error {
	dbPath := filepath.Join(s.DataDir, "primary.db")
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("primary database does not exist at %s", dbPath)
		}
		return err
	}
	store := state.Open(dbPath)
	defer store.Close()
	secretsMgr, err := secrets.Open(s.DataDir, store)
	if err != nil {
		return err
	}
	if unlocked, _ := secretsMgr.Status(); !unlocked {
		return secrets.ErrLocked
	}
	configService, err := config.NewService(store)
	if err != nil {
		return err
	}
	if _, err := certu.LoadPrimary(secretsMgr); err != nil {
		return fmt.Errorf("loading cluster TLS material: %w", err)
	}
	settings := configService.Snapshot().Settings
	if configService.MustLoadConfigBoolValue(settings.HttpsWeb.Enabled) && configService.MustLoadConfigBoolValue(settings.HttpsWeb.TlsSelfManaged) {
		var bundle []byte
		if settings.HttpsWeb.TlsCertPem.VersionID != 0 {
			bundle, err = secretsMgr.RevealByID(settings.HttpsWeb.TlsCertPem.VersionID)
		} else {
			bundle, err = certu.LoadWebUISelfSigned(secretsMgr)
		}
		if err != nil {
			return fmt.Errorf("loading self-managed Web TLS material: %w", err)
		}
		if _, err := tls.X509KeyPair(bundle, bundle); err != nil {
			return fmt.Errorf("loading self-managed Web TLS material: %w", err)
		}
	}
	return nil
}

func validateOptions(opts Options) error {
	if strings.TrimSpace(opts.PrimaryName) == "" {
		return fmt.Errorf("primary name is required")
	}
	if strings.TrimSpace(opts.Initial.MasterPasswordHash) == "" {
		return fmt.Errorf("initial master password hash is required")
	}
	if !opts.Initial.WebHTTPEnabled && !opts.Initial.WebHTTPSEnabled {
		return fmt.Errorf("at least one Web listener must be enabled")
	}
	for name, value := range map[string]string{
		"HTTP Web":   enabledValue(opts.Initial.WebHTTPEnabled, opts.Initial.WebHTTPListen),
		"HTTPS Web":  enabledValue(opts.Initial.WebHTTPSEnabled, opts.Initial.WebHTTPSListen),
		"cluster":    opts.Initial.ClusterListen,
		"enrollment": opts.Initial.EnrollmentListen,
	} {
		if value == "" {
			continue
		}
		if _, port, err := net.SplitHostPort(strings.TrimSpace(value)); err != nil || port == "" {
			return fmt.Errorf("%s listen address must look like :8080", name)
		}
	}
	if opts.Initial.WebHTTPEnabled && opts.Initial.WebHTTPSEnabled && strings.TrimSpace(opts.Initial.WebHTTPListen) == strings.TrimSpace(opts.Initial.WebHTTPSListen) {
		return fmt.Errorf("HTTP and HTTPS Web listen addresses must differ")
	}
	if len(opts.WebTLSCertPEM) != 0 {
		if _, err := tls.X509KeyPair(opts.WebTLSCertPEM, opts.WebTLSCertPEM); err != nil {
			return fmt.Errorf("initial Web TLS certificate must contain a certificate chain and private key: %w", err)
		}
	}
	return nil
}

func enabledValue(enabled bool, value string) string {
	if !enabled {
		return ""
	}
	return value
}

func cleanupBootstrapArtifacts(dataDir string) {
	dbPath := filepath.Join(dataDir, "primary.db")
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm", filepath.Join(dataDir, "machine.key")} {
		_ = os.Remove(path)
	}
}
