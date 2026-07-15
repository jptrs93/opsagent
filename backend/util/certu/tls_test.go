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

func TestSignWorkerCertificateRequestUsesRequestedIdentifier(t *testing.T) {
	caCert, caKey, err := GenerateClusterCA("test-ca")
	if err != nil {
		t.Fatalf("GenerateClusterCA: %v", err)
	}
	csr, _, err := GenerateWorkerCertificateRequest("worker-id")
	if err != nil {
		t.Fatalf("GenerateWorkerCertificateRequest: %v", err)
	}
	_, certPEM, err := SignWorkerCertificateRequestFromPEM(caCert, caKey, csr, "worker-id")
	if err != nil {
		t.Fatalf("SignWorkerCertificateRequestFromPEM: %v", err)
	}
	if got := MustCertCommonNameFromPEM(certPEM); got != "worker-id" {
		t.Fatalf("certificate CN = %q, want worker-id", got)
	}
	if _, _, err := SignWorkerCertificateRequestFromPEM(caCert, caKey, csr, "other-id"); err == nil {
		t.Fatal("SignWorkerCertificateRequestFromPEM accepted a mismatched CSR CN")
	}
}
