package certu

import (
	"crypto/tls"
	"crypto/x509"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func newTestSecrets(t *testing.T) *secrets.Manager {
	t.Helper()
	dir := t.TempDir()
	store := state.Open(filepath.Join(dir, "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	mgr, err := secrets.Initialize(dir, store)
	if err != nil {
		t.Fatalf("secrets.Initialize: %v", err)
	}
	return mgr
}

func TestEnsureWebUILocalTLSIssuesUnderLocalCAAndReissuesForNewNames(t *testing.T) {
	store := newTestSecrets(t)
	bundle, caPEM, err := EnsureWebUILocalTLS(store, WebUITLSNames("opendeploy.local", ":8443"))
	if err != nil {
		t.Fatalf("EnsureWebUILocalTLS: %v", err)
	}
	if _, err := tls.X509KeyPair(bundle, bundle); err != nil {
		t.Fatalf("bundle is not a usable key pair: %v", err)
	}
	_, leaf, err := parseCertificate(bundle, "leaf")
	if err != nil {
		t.Fatal(err)
	}
	_, ca, err := parseCertificate(caPEM, "ca")
	if err != nil {
		t.Fatal(err)
	}
	if !ca.IsCA || ca.Subject.CommonName != webUILocalCAName {
		t.Fatalf("CA = %q IsCA=%v, want %q", ca.Subject.CommonName, ca.IsCA, webUILocalCAName)
	}
	if err := leaf.CheckSignatureFrom(ca); err != nil {
		t.Fatalf("leaf not signed by local CA: %v", err)
	}
	for _, want := range []string{"opendeploy.local", "localhost"} {
		if !contains(leaf.DNSNames, want) {
			t.Fatalf("leaf DNS names %v lack %q", leaf.DNSNames, want)
		}
	}
	if !webUIBundleCurrent(bundle, caPEM, WebUITLSNames("opendeploy.local", ":8443")) {
		t.Fatal("freshly issued bundle reported not current")
	}

	// Same names: the stored bundle is reused byte for byte.
	again, caAgain, err := EnsureWebUILocalTLS(store, WebUITLSNames("opendeploy.local", ":8443"))
	if err != nil {
		t.Fatalf("EnsureWebUILocalTLS (again): %v", err)
	}
	if string(again) != string(bundle) || string(caAgain) != string(caPEM) {
		t.Fatal("unchanged names reissued the bundle or CA")
	}

	// A new hostname reissues the leaf under the same CA.
	reissued, caSame, err := EnsureWebUILocalTLS(store, WebUITLSNames("opendeploy.local,other.local", ":8443"))
	if err != nil {
		t.Fatalf("EnsureWebUILocalTLS (new name): %v", err)
	}
	if string(reissued) == string(bundle) {
		t.Fatal("adding a hostname did not reissue the leaf")
	}
	if string(caSame) != string(caPEM) {
		t.Fatal("reissue replaced the CA; operators would have to re-trust it")
	}
	_, leaf2, _ := parseCertificate(reissued, "leaf")
	if !contains(leaf2.DNSNames, "other.local") {
		t.Fatalf("reissued leaf DNS names %v lack other.local", leaf2.DNSNames)
	}
	if time.Until(leaf2.NotAfter) < webUILeafValidity-time.Hour {
		t.Fatalf("reissued leaf validity too short: %v", leaf2.NotAfter)
	}

	// The exported CA file matches the stored CA and rewrites are idempotent.
	dir := t.TempDir()
	for range 2 {
		if err := WriteWebUILocalCAFile(dir, caPEM); err != nil {
			t.Fatalf("WriteWebUILocalCAFile: %v", err)
		}
	}
	loaded, err := LoadWebUILocalCA(store)
	if err != nil || string(loaded) != string(caPEM) {
		t.Fatalf("LoadWebUILocalCA = %v, mismatch=%v", err, string(loaded) != string(caPEM))
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func TestWebUILocalTLSUsesECDSAP256(t *testing.T) {
	store := newTestSecrets(t)
	bundle, caPEM, err := EnsureWebUILocalTLS(store, WebUITLSNames("", ":8443"))
	if err != nil {
		t.Fatalf("EnsureWebUILocalTLS: %v", err)
	}
	for _, c := range []struct {
		label string
		pem   []byte
	}{{"leaf", bundle}, {"ca", caPEM}} {
		_, cert, err := parseCertificate(c.pem, c.label)
		if err != nil {
			t.Fatal(err)
		}
		if cert.PublicKeyAlgorithm != x509.ECDSA {
			t.Fatalf("%s public key algorithm = %v, want ECDSA (browser and trust-store compatibility)", c.label, cert.PublicKeyAlgorithm)
		}
	}
}
