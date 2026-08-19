package certu

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/lib/secrets"
)

const (
	secretCACert         = "opendeploy.cluster.ca.cert"
	secretCAKey          = "opendeploy.cluster.ca.key"
	secretWorkloadCACert = "opendeploy.workload.ca.cert"
	secretWorkloadCAKey  = "opendeploy.workload.ca.key"
	secretPrimaryCert    = "opendeploy.cluster.primary.cert"
	secretPrimaryKey     = "opendeploy.cluster.primary.key"
	secretWebUITLS       = "opendeploy.webui.self_signed_tls_bundle"
)

type Material struct {
	CACert      []byte
	CAKey       []byte
	PrimaryCert []byte
	PrimaryKey  []byte
}

func BootstrapWebUISelfSigned(store *secrets.Manager, names []string) ([]byte, error) {
	bundle, err := LoadWebUISelfSigned(store)
	if err == nil {
		return bundle, nil
	}
	if !errors.Is(err, secrets.ErrNotFound) {
		return nil, err
	}
	certPEM, keyPEM, err := GenerateSelfSignedServerCertificate(names)
	if err != nil {
		return nil, err
	}
	bundle = append(certPEM, keyPEM...)
	if err := store.SetInternal(secretWebUITLS, bundle); err != nil {
		return nil, err
	}
	return bundle, nil
}

func LoadWebUISelfSigned(store *secrets.Manager) ([]byte, error) {
	return store.RevealInternal(secretWebUITLS)
}

func BootstrapWorkloadCA(store *secrets.Manager) (certPEM, keyPEM []byte, err error) {
	certPEM, certErr := store.RevealInternal(secretWorkloadCACert)
	keyPEM, keyErr := store.RevealInternal(secretWorkloadCAKey)
	if certErr == nil && keyErr == nil {
		return certPEM, keyPEM, nil
	}
	if (certErr != nil && !errors.Is(certErr, secrets.ErrNotFound)) || (keyErr != nil && !errors.Is(keyErr, secrets.ErrNotFound)) {
		return nil, nil, errors.Join(certErr, keyErr)
	}
	certPEM, keyPEM, err = GenerateWorkloadCA("opendeploy-workload-ca")
	if err != nil {
		return nil, nil, err
	}
	if err := store.SetInternal(secretWorkloadCAKey, keyPEM); err != nil {
		return nil, nil, err
	}
	if err := store.SetInternal(secretWorkloadCACert, certPEM); err != nil {
		return nil, nil, err
	}
	return certPEM, keyPEM, nil
}

func WebUISelfSignedNames(acmeHosts, listen string) []string {
	var names []string
	for _, name := range strings.Split(acmeHosts, ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(listen))
	host = strings.Trim(host, "[]")
	if err == nil && host != "" && host != "0.0.0.0" && host != "::" {
		names = append(names, host)
	}
	return names
}

func BootstrapPrimary(store *secrets.Manager, primaryIdentifier, primaryServerName string) (*Material, error) {
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
	primaryCert, primaryKey, err := GenerateNodeCertificateWithServerName(caCert, caKey, primaryIdentifier, primaryServerName)
	if err != nil {
		return nil, err
	}
	mat = &Material{CACert: caCert, CAKey: caKey, PrimaryCert: primaryCert, PrimaryKey: primaryKey}
	if err := store.SetInternal(secretCACert, mat.CACert); err != nil {
		return nil, err
	}
	if err := store.SetInternal(secretCAKey, mat.CAKey); err != nil {
		return nil, err
	}
	if err := store.SetInternal(secretPrimaryCert, mat.PrimaryCert); err != nil {
		return nil, err
	}
	if err := store.SetInternal(secretPrimaryKey, mat.PrimaryKey); err != nil {
		return nil, err
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

func SignWorkerCertificateRequest(store *secrets.Manager, csrPEM []byte, identifier string) (caCert, workerCert []byte, err error) {
	mat, err := LoadPrimary(store)
	if err != nil {
		return nil, nil, fmt.Errorf("loading cluster signing material: %w", err)
	}
	return SignWorkerCertificateRequestFromPEM(mat.CACert, mat.CAKey, csrPEM, identifier)
}

func RenewWorkerCertificate(store *secrets.Manager, identifier string, publicKey any) (caCert, workerCert []byte, notAfter time.Time, err error) {
	mat, err := LoadPrimary(store)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("loading cluster signing material: %w", err)
	}
	workerCert, notAfter, err = SignWorkerCertificateFromPublicKey(mat.CACert, mat.CAKey, identifier, publicKey)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	return mat.CACert, workerCert, notAfter, nil
}

func WorkerTLSPaths(tlsDir string) (caPath, certPath, keyPath string) {
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
