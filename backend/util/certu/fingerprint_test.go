package certu

import (
	"crypto/sha256"
	"strings"
	"testing"
)

func TestParseSHA256Fingerprint(t *testing.T) {
	sum := sha256.Sum256([]byte("test"))
	want := sum[:]
	formatted := FormatSHA256Fingerprint(want)
	colonSeparated := formatted[:len(FingerprintPrefixSHA256)] + strings.Join(hexPairs(formatted[len(FingerprintPrefixSHA256):]), ":")

	for _, value := range []string{
		formatted,
		strings.ToUpper(formatted),
		colonSeparated,
	} {
		got, err := ParseSHA256Fingerprint(value)
		if err != nil {
			t.Fatalf("ParseSHA256Fingerprint(%q): %v", value, err)
		}
		if string(got) != string(want) {
			t.Fatalf("ParseSHA256Fingerprint(%q) = %x, want %x", value, got, want)
		}
	}
}

func hexPairs(value string) []string {
	parts := make([]string, 0, len(value)/2)
	for i := 0; i < len(value); i += 2 {
		parts = append(parts, value[i:i+2])
	}
	return parts
}

func TestParseSHA256FingerprintRejectsInvalid(t *testing.T) {
	for _, value := range []string{"", "sha256:abc", "sha256:not-hex"} {
		if _, err := ParseSHA256Fingerprint(value); err == nil {
			t.Fatalf("ParseSHA256Fingerprint(%q) succeeded", value)
		}
	}
}

func TestCertificatePEMSPKISHA256(t *testing.T) {
	caCert, caKey, err := GenerateClusterCA("test-ca")
	if err != nil {
		t.Fatalf("GenerateClusterCA: %v", err)
	}
	certPEM, _, err := GenerateNodeCertificate(caCert, caKey, "primary")
	if err != nil {
		t.Fatalf("GenerateNodeCertificate: %v", err)
	}
	fingerprint, err := CertificatePEMSPKISHA256(certPEM)
	if err != nil {
		t.Fatalf("CertificatePEMSPKISHA256: %v", err)
	}
	if !strings.HasPrefix(fingerprint, FingerprintPrefixSHA256) {
		t.Fatalf("fingerprint = %q, want sha256 prefix", fingerprint)
	}
	if _, err := ParseSHA256Fingerprint(fingerprint); err != nil {
		t.Fatalf("ParseSHA256Fingerprint(%q): %v", fingerprint, err)
	}
}
