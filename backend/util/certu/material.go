package certu

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jptrs93/opsagent/backend/lib/secrets"
)

const (
	secretCACert      = "opendeploy.cluster.ca.cert"
	secretCAKey       = "opendeploy.cluster.ca.key"
	secretPrimaryCert = "opendeploy.cluster.primary.cert"
	secretPrimaryKey  = "opendeploy.cluster.primary.key"
)

type Material struct {
	CACert      []byte
	CAKey       []byte
	PrimaryCert []byte
	PrimaryKey  []byte
}

func BootstrapPrimary(store *secrets.Manager, primaryName string) (*Material, error) {
	mat, err := LoadPrimary(store)
	if err == nil {
		return mat, nil
	}
	if !errors.Is(err, secrets.ErrNotFound) {
		return nil, err
	}
	caCert, caKey, err := GenerateClusterCA("opendeploy-cluster")
	if err != nil {
		return nil, err
	}
	primaryCert, primaryKey, err := GenerateNodeCertificate(caCert, caKey, primaryName)
	if err != nil {
		return nil, err
	}
	mat = &Material{CACert: caCert, CAKey: caKey, PrimaryCert: primaryCert, PrimaryKey: primaryKey}
	if setCACertErr := store.SetInternal(secretCACert, mat.CACert); setCACertErr != nil {
		return nil, setCACertErr
	}
	if setCAKeyErr := store.SetInternal(secretCAKey, mat.CAKey); setCAKeyErr != nil {
		return nil, setCAKeyErr
	}
	if setPrimaryCertErr := store.SetInternal(secretPrimaryCert, mat.PrimaryCert); setPrimaryCertErr != nil {
		return nil, setPrimaryCertErr
	}
	if setPrimaryKeyErr := store.SetInternal(secretPrimaryKey, mat.PrimaryKey); setPrimaryKeyErr != nil {
		return nil, setPrimaryKeyErr
	}
	return mat, nil
}

func LoadPrimary(store *secrets.Manager) (*Material, error) {
	caCert, err := store.RevealInternal(secretCACert)
	if err != nil {
		return nil, err
	}
	caKey, err := store.RevealInternal(secretCAKey)
	if err != nil {
		return nil, err
	}
	primaryCert, err := store.RevealInternal(secretPrimaryCert)
	if err != nil {
		return nil, err
	}
	primaryKey, err := store.RevealInternal(secretPrimaryKey)
	if err != nil {
		return nil, err
	}
	return &Material{CACert: caCert, CAKey: caKey, PrimaryCert: primaryCert, PrimaryKey: primaryKey}, nil
}

func SignWorkerCertificateRequest(store *secrets.Manager, csrPEM []byte, workerName string) (caCert, workerCert []byte, err error) {
	mat, err := LoadPrimary(store)
	if err != nil {
		return nil, nil, fmt.Errorf("loading cluster signing material: %w", err)
	}
	return SignWorkerCertificateRequestFromPEM(mat.CACert, mat.CAKey, csrPEM, workerName)
}

func WorkerTLSPaths(dataDir string) (caPath, certPath, keyPath string) {
	tlsDir := filepath.Join(dataDir, "tls")
	return filepath.Join(tlsDir, "ca.crt"), filepath.Join(tlsDir, "node.crt"), filepath.Join(tlsDir, "node.key")
}

func WorkerTLSMaterialExists(paths ...string) bool {
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}
