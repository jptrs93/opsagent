package certu

import (
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"
)

func TestSignWorkloadCertificate(t *testing.T) {
	caCert, caKey, err := GenerateWorkloadCA("opendeploy-workload-ca")
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := SignWorkloadCertificate(caCert, caKey, "s3-1.space-1.internal", []string{"s3-1.space-1.internal", "10.7.20.100"}, 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(keyPEM) == 0 {
		t.Fatal("no key returned")
	}
	block, _ := pem.Decode(certPEM)
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caCert) {
		t.Fatal("cannot load CA cert")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		DNSName:   "s3-1.space-1.internal",
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("verify against workload CA: %v", err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err == nil {
		t.Fatal("workload leaf must not verify for client auth")
	}
	foundIP := false
	for _, ip := range leaf.IPAddresses {
		if ip.String() == "10.7.20.100" {
			foundIP = true
		}
	}
	if !foundIP {
		t.Fatalf("missing IP SAN, got %v", leaf.IPAddresses)
	}
}

func TestSignWorkerCertificateFromPublicKey(t *testing.T) {
	caCert, caKey, err := GenerateClusterCA("opendeploy-cluster")
	if err != nil {
		t.Fatal(err)
	}
	csrPEM, _, err := GenerateWorkerCertificateRequest("machine-1")
	if err != nil {
		t.Fatal(err)
	}
	_, originalPEM, err := SignWorkerCertificateRequestFromPEM(caCert, caKey, csrPEM, "machine-1")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(originalPEM)
	original, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	renewedPEM, notAfter, err := SignWorkerCertificateFromPublicKey(caCert, caKey, "machine-1", original.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if time.Until(notAfter) < 364*24*time.Hour {
		t.Fatalf("renewed cert expires too soon: %v", notAfter)
	}
	block, _ = pem.Decode(renewedPEM)
	renewed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Subject.CommonName != "machine-1" {
		t.Fatalf("renewed cert CN = %q", renewed.Subject.CommonName)
	}
	if !renewed.PublicKey.(interface{ Equal(x crypto.PublicKey) bool }).Equal(original.PublicKey) {
		t.Fatal("renewed cert does not keep the original public key")
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caCert)
	if _, err := renewed.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("renewed cert does not verify for client auth: %v", err)
	}
}
