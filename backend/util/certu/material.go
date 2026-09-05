package certu

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/lib/secrets"
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

// Self-managed Web UI TLS without an operator-supplied bundle uses a locally
// generated CA and a leaf signed by it, rather than a bare self-signed leaf.
// The CA is what an operator trusts once, in the OS or browser store, so
// that the leaf can be reissued freely whenever the configured names change.
const (
	webUILocalCAName    = "OpenDeploy Local CA"
	webUILeafCommonName = "opendeploy-webui"
	webUILeafValidity   = 2 * 365 * 24 * time.Hour
	// webUILocalCAValidity is long because every re-trust is a manual step on
	// every operator machine; the leaf is what rotates.
	webUILocalCAValidity = 20 * 365 * 24 * time.Hour
	// webUILeafRenewBefore reissues a leaf that is within this much of expiry.
	webUILeafRenewBefore = 30 * 24 * time.Hour
	// WebUILocalCAFileName is the CA certificate copy written next to the
	// primary database so the installer can print its path.
	WebUILocalCAFileName = "web-ca.crt"
)

// EnsureWebUILocalTLS returns the Web UI certificate bundle (leaf cert + key
// PEM) for the given names, creating the local CA and reissuing the leaf when
// none exists, when it no longer covers every name, when it was not signed by
// the current CA, or when it is close to expiry. It also returns the CA
// certificate PEM for export.
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
	certPEM, keyPEM, err := SignWebUICertificate(caCertPEM, caKeyPEM, webUILeafCommonName, names, webUILeafValidity)
	if err != nil {
		return nil, nil, fmt.Errorf("issuing Web UI certificate: %w", err)
	}
	bundle = append(certPEM, keyPEM...)
	if err := store.SetInternal(secretWebUILocalTLS, bundle); err != nil {
		return nil, nil, err
	}
	return bundle, caCertPEM, nil
}

// LoadWebUILocalCA returns the local Web UI CA certificate PEM, or
// secrets.ErrNotFound when none has been generated.
func LoadWebUILocalCA(store *secrets.Manager) ([]byte, error) {
	return store.RevealInternal(secretWebUILocalCACert)
}

// WebUILocalCAPath is where the CA certificate is exported for operators.
func WebUILocalCAPath(dataDir string) string {
	return filepath.Join(dataDir, WebUILocalCAFileName)
}

// WriteWebUILocalCAFile exports the CA certificate world-readable; it holds
// no secret, and the whole point is for other tools and people to read it.
func WriteWebUILocalCAFile(dataDir string, caCertPEM []byte) error {
	path := WebUILocalCAPath(dataDir)
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, caCertPEM) {
		return nil
	}
	return os.WriteFile(path, caCertPEM, 0o644)
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
	certPEM, keyPEM, err = GenerateWebUICA(webUILocalCAName, webUILocalCAValidity)
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

// webUIBundleCurrent reports whether a stored bundle is still the one we
// would issue now: signed by the CA, covering every requested name, and not
// about to expire.
func webUIBundleCurrent(bundle, caCertPEM []byte, names []string) bool {
	_, leaf, err := parseCertificate(bundle, "Web UI certificate")
	if err != nil {
		return false
	}
	_, ca, err := parseCertificate(caCertPEM, "Web UI CA")
	if err != nil {
		return false
	}
	if err := leaf.CheckSignatureFrom(ca); err != nil {
		return false
	}
	if time.Until(leaf.NotAfter) < webUILeafRenewBefore {
		return false
	}
	wantDNS, wantIPs := serverCertificateNames(names)
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

// WebUITLSNames are the names the Web UI certificate must cover: the
// configured hostnames plus the listen host. Loopback names are added by the
// signer.
func WebUITLSNames(acmeHosts, listen string) []string {
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

func SignSecondaryCertificateRequest(store *secrets.Manager, csrPEM []byte, identifier string) (caCert, secondaryCert []byte, err error) {
	mat, err := LoadPrimary(store)
	if err != nil {
		return nil, nil, fmt.Errorf("loading cluster signing material: %w", err)
	}
	return SignSecondaryCertificateRequestFromPEM(mat.CACert, mat.CAKey, csrPEM, identifier)
}

func RenewSecondaryCertificate(store *secrets.Manager, identifier string, publicKey any) (caCert, secondaryCert []byte, notAfter time.Time, err error) {
	mat, err := LoadPrimary(store)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("loading cluster signing material: %w", err)
	}
	secondaryCert, notAfter, err = SignSecondaryCertificateFromPublicKey(mat.CACert, mat.CAKey, identifier, publicKey)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	return mat.CACert, secondaryCert, notAfter, nil
}

func SecondaryTLSPaths(tlsDir string) (caPath, certPath, keyPath string) {
	return filepath.Join(tlsDir, "ca.crt"), filepath.Join(tlsDir, "node.crt"), filepath.Join(tlsDir, "node.key")
}

func SecondaryTLSMaterialExists(paths ...string) bool {
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}
