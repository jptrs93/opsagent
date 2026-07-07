package secondary

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/util/certu"
)

func TestEnrollmentHTTPClientAcceptsPinnedCertificate(t *testing.T) {
	cert := enrollmentTestCertificate(t)
	fingerprint := certu.CertificateSPKISHA256(cert)
	client, err := enrollmentHTTPClient(fingerprint)
	if err != nil {
		t.Fatalf("enrollmentHTTPClient: %v", err)
	}
	verify := client.Transport.(*http.Transport).TLSClientConfig.VerifyConnection
	if err := verify(tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}); err != nil {
		t.Fatalf("VerifyConnection: %v", err)
	}
}

func TestEnrollmentHTTPClientRejectsMismatchedCertificate(t *testing.T) {
	cert := enrollmentTestCertificate(t)
	wrong := sha256.Sum256([]byte("wrong"))
	client, err := enrollmentHTTPClient(certu.FormatSHA256Fingerprint(wrong[:]))
	if err != nil {
		t.Fatalf("enrollmentHTTPClient: %v", err)
	}
	verify := client.Transport.(*http.Transport).TLSClientConfig.VerifyConnection
	if err := verify(tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}); err == nil {
		t.Fatal("VerifyConnection succeeded with mismatched fingerprint")
	}
}

func TestEnrollmentHTTPClientRequiresFingerprint(t *testing.T) {
	if _, err := enrollmentHTTPClient(""); err == nil {
		t.Fatal("enrollmentHTTPClient succeeded without fingerprint")
	}
}

func TestEnrollReturnsWhenContextCanceled(t *testing.T) {
	cert := enrollmentTestCertificate(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := Enroll(ctx, EnrollmentConfig{
		PrimaryEnrollmentAddr:        "127.0.0.1:1",
		PrimaryEnrollmentFingerprint: certu.CertificateSPKISHA256(cert),
		DataDir:                      t.TempDir(),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Enroll error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("Enroll took %s after context cancellation", elapsed)
	}
}

func enrollmentTestCertificate(t *testing.T) *x509.Certificate {
	t.Helper()
	caCert, caKey, err := certu.GenerateClusterCA("test-ca")
	if err != nil {
		t.Fatalf("GenerateClusterCA: %v", err)
	}
	certPEM, _, err := certu.GenerateNodeCertificate(caCert, caKey, "primary")
	if err != nil {
		t.Fatalf("GenerateNodeCertificate: %v", err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("certificate PEM did not decode")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return cert
}
