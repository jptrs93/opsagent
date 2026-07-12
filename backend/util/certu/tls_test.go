package certu

import (
	"crypto/tls"
	"testing"
)

func TestGenerateSelfSignedServerCertificateProducesLoadableBundle(t *testing.T) {
	certPEM, keyPEM, err := GenerateSelfSignedServerCertificate([]string{"example.test"})
	if err != nil {
		t.Fatalf("GenerateSelfSignedServerCertificate: %v", err)
	}
	bundle := append(append([]byte{}, certPEM...), keyPEM...)
	if _, pairErr := tls.X509KeyPair(bundle, bundle); pairErr != nil {
		t.Fatalf("X509KeyPair generated bundle: %v", pairErr)
	}
}
