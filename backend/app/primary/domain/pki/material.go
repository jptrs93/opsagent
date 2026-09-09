package pki

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/util/certu"
)

const (
	secretCACert           = "opendeploy.cluster.ca.cert"
	secretCAKey            = "opendeploy.cluster.ca.key"
	secretWorkloadCACert   = "opendeploy.workload.ca.cert"
	secretWorkloadCAKey    = "opendeploy.workload.ca.key"
	secretPrimaryCert      = "opendeploy.cluster.primary.cert"
	secretPrimaryKey       = "opendeploy.cluster.primary.key"
	secretWebUILocalCACert = "opendeploy.webui.local_ca.cert"
	secretWebUILocalCAKey  = "opendeploy.webui.local_ca.key"
	secretWebUILocalTLS    = "opendeploy.webui.local_tls_bundle"
)

type Material struct {
	CACert      []byte
	CAKey       []byte
	PrimaryCert []byte
	PrimaryKey  []byte
}

const (
	webUILocalCAName     = "OpenDeploy Local CA"
	webUILeafCommonName  = "opendeploy-webui"
	webUILeafValidity    = 2 * 365 * 24 * time.Hour
	webUILocalCAValidity = 20 * 365 * 24 * time.Hour
	webUILeafRenewBefore = 30 * 24 * time.Hour
)

func EnsureWebUILocalTLS(store *secrets.Manager, names []string) (bundle, caCertPEM []byte, err error) {
	caCertPEM, caKeyPEM, err := ensureWebUILocalCA(store)
	if err != nil {
		return nil, nil, err
	}
	bundle, err = store.RevealInternal(secretWebUILocalTLS)
	if err == nil && webUIBundleCurrent(bundle, caCertPEM, names) {
		return bundle, caCertPEM, nil
	}
	if err != nil && !errors.Is(err, secrets.ErrNotFound) {
		return nil, nil, err
	}
	certPEM, keyPEM, err := certu.SignWebUICertificate(caCertPEM, caKeyPEM, webUILeafCommonName, names, webUILeafValidity)
	if err != nil {
		return nil, nil, fmt.Errorf("issuing Web UI certificate: %w", err)
	}
	bundle = append(certPEM, keyPEM...)
	if err := store.SetInternal(secretWebUILocalTLS, bundle); err != nil {
		return nil, nil, err
	}
	return bundle, caCertPEM, nil
}
func LoadWebUILocalCA(store *secrets.Manager) ([]byte, error) {
	return store.RevealInternal(secretWebUILocalCACert)
}

func ensureWebUILocalCA(store *secrets.Manager) (certPEM, keyPEM []byte, err error) {
	certPEM, certErr := store.RevealInternal(secretWebUILocalCACert)
	keyPEM, keyErr := store.RevealInternal(secretWebUILocalCAKey)
	if certErr == nil && keyErr == nil {
		return certPEM, keyPEM, nil
	}
	if (certErr != nil && !errors.Is(certErr, secrets.ErrNotFound)) || (keyErr != nil && !errors.Is(keyErr, secrets.ErrNotFound)) {
		return nil, nil, errors.Join(certErr, keyErr)
	}
	certPEM, keyPEM, err = certu.GenerateWebUICA(webUILocalCAName, webUILocalCAValidity)
	if err != nil {
		return nil, nil, err
	}
	if err := store.SetInternal(secretWebUILocalCAKey, keyPEM); err != nil {
		return nil, nil, err
	}
	if err := store.SetInternal(secretWebUILocalCACert, certPEM); err != nil {
		return nil, nil, err
	}
	return certPEM, keyPEM, nil
}
func webUIBundleCurrent(bundle, caCertPEM []byte, names []string) bool {
	_, leaf, err := certu.ParseCertificate(bundle, "Web UI certificate")
	if err != nil {
		return false
	}
	_, ca, err := certu.ParseCertificate(caCertPEM, "Web UI CA")
	if err != nil {
		return false
	}
	if err := leaf.CheckSignatureFrom(ca); err != nil {
		return false
	}
	if time.Until(leaf.NotAfter) < webUILeafRenewBefore {
		return false
	}
	wantDNS, wantIPs := certu.ServerCertificateNames(names)
	for _, name := range wantDNS {
		if !slices.Contains(leaf.DNSNames, name) {
			return false
		}
	}
	for _, ip := range wantIPs {
		if !slices.ContainsFunc(leaf.IPAddresses, ip.Equal) {
			return false
		}
	}
	return true
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
	certPEM, keyPEM, err = certu.GenerateWorkloadCA("opendeploy-workload-ca")
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

func BootstrapPrimary(store *secrets.Manager, primaryIdentifier, primaryServerName string) (*Material, error) {
	mat, err := LoadPrimary(store)
	if err == nil {
		return mat, nil
	}
	if !errors.Is(err, secrets.ErrNotFound) {
		return nil, err
	}
	caCert, caKey, err := certu.GenerateClusterCA("opendeploy-cluster")
	if err != nil {
		return nil, err
	}
	primaryCert, primaryKey, err := certu.GenerateNodeCertificateWithServerName(caCert, caKey, primaryIdentifier, primaryServerName)
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

func SignSecondaryCertificateRequest(store *secrets.Manager, csrPEM []byte, identifier string) (caCert, secondaryCert []byte, err error) {
	mat, err := LoadPrimary(store)
	if err != nil {
		return nil, nil, fmt.Errorf("loading cluster signing material: %w", err)
	}
	return certu.SignSecondaryCertificateRequestFromPEM(mat.CACert, mat.CAKey, csrPEM, identifier)
}

func RenewSecondaryCertificate(store *secrets.Manager, identifier string, publicKey any) (caCert, secondaryCert []byte, notAfter time.Time, err error) {
	mat, err := LoadPrimary(store)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("loading cluster signing material: %w", err)
	}
	secondaryCert, notAfter, err = certu.SignSecondaryCertificateFromPublicKey(mat.CACert, mat.CAKey, identifier, publicKey)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	return mat.CACert, secondaryCert, notAfter, nil
}
