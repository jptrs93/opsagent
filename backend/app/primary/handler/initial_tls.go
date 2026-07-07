package handler

import (
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
)

const initialWebTLSCertPEMFileName = "initial-web-tls.pem"

func initialWebTLSCertPEMHook(secretsMgr *secrets.Manager) func(*apigen.Config) error {
	return func(cfg *apigen.Config) error {
		bundle, cleanupPath, err := initialWebTLSCertPEM()
		if err != nil {
			return err
		}
		if len(bundle) == 0 {
			return nil
		}
		if _, err := tls.X509KeyPair(bundle, bundle); err != nil {
			return fmt.Errorf("OPENDEPLOY_INITIAL_WEB_TLS_CERT_PEM must contain a PEM certificate chain and private key: %w", err)
		}
		meta, err := secretsMgr.Set(secrets.TLSCertPEMSecretName, bundle, 0)
		if err != nil {
			return fmt.Errorf("creating initial Web TLS certificate secret: %w", err)
		}
		if cleanupPath != "" {
			if err := os.Remove(cleanupPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("removing initial Web TLS certificate file: %w", err)
			}
		}
		cfg.Settings.HttpsWeb.TlsSelfManaged = apigen.BoolSetting{Value: true}
		cfg.Settings.HttpsWeb.TlsCertPem = apigen.SecretRef{ID: meta.ID}
		return nil
	}
}

func initialWebTLSCertPEM() ([]byte, string, error) {
	raw := strings.TrimSpace(ainit.StaticConfig.InitialWebTLSCertPEM)
	path := strings.TrimSpace(ainit.StaticConfig.InitialWebTLSCertPEMFile)
	if raw != "" && path != "" {
		return nil, "", fmt.Errorf("set only one of OPENDEPLOY_INITIAL_WEB_TLS_CERT_PEM and OPENDEPLOY_INITIAL_WEB_TLS_CERT_PEM_FILE")
	}
	cleanupPath := ""
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, "", fmt.Errorf("reading OPENDEPLOY_INITIAL_WEB_TLS_CERT_PEM_FILE: %w", err)
		}
		raw = strings.TrimSpace(string(b))
		if filepath.Clean(path) == filepath.Join(ainit.StaticConfig.DataDir, initialWebTLSCertPEMFileName) {
			cleanupPath = path
		}
	}
	if raw == "" {
		return nil, "", nil
	}
	if !strings.Contains(raw, "\n") && strings.Contains(raw, `\n`) {
		raw = strings.ReplaceAll(raw, `\n`, "\n")
	}
	return []byte(raw), cleanupPath, nil
}
