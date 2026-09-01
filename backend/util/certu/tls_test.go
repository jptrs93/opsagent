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
	if _, err := tls.X509KeyPair(bundle, bundle); err != nil {
		t.Fatalf("X509KeyPair generated bundle: %v", err)
	}
}

func TestSignSecondaryCertificateRequestUsesRequestedIdentifier(t *testing.T) {
	caCert, caKey, err := GenerateClusterCA("test-ca")
	if err != nil {
		t.Fatalf("GenerateClusterCA: %v", err)
	}
	csr, _, err := GenerateSecondaryCertificateRequest("secondary-id")
	if err != nil {
		t.Fatalf("GenerateSecondaryCertificateRequest: %v", err)
	}
	_, certPEM, err := SignSecondaryCertificateRequestFromPEM(caCert, caKey, csr, "secondary-id")
	if err != nil {
		t.Fatalf("SignSecondaryCertificateRequestFromPEM: %v", err)
	}
	if got := MustCertCommonNameFromPEM(certPEM); got != "secondary-id" {
		t.Fatalf("certificate CN = %q, want secondary-id", got)
	}
	if _, _, err := SignSecondaryCertificateRequestFromPEM(caCert, caKey, csr, "other-id"); err == nil {
		t.Fatal("SignSecondaryCertificateRequestFromPEM accepted a mismatched CSR CN")
	}
}
