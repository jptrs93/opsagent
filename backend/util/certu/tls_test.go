package certu

import (
	gotls "crypto/tls"
	"testing"
)

func TestGenerateSelfSignedServerCertificateProducesLoadableBundle(t *testing.T) {
	certPEM, keyPEM, err := GenerateSelfSignedServerCertificate([]string{"example.test"})
	if err != nil {
		t.Fatalf("GenerateSelfSignedServerCertificate: %v", err)
	}
	bundle := append(append([]byte{}, certPEM...), keyPEM...)
	if _, err := gotls.X509KeyPair(bundle, bundle); err != nil {
		t.Fatalf("X509KeyPair generated bundle: %v", err)
	}
}
