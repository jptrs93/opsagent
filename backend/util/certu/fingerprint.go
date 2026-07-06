package certu

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"strings"
)

const FingerprintPrefixSHA256 = "sha256:"

func CertificateSPKISHA256(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return FingerprintPrefixSHA256 + hex.EncodeToString(sum[:])
}

func CertificatePEMSPKISHA256(certPEM []byte) (string, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return "", fmt.Errorf("certificate PEM is empty or invalid")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parsing certificate: %w", err)
	}
	return CertificateSPKISHA256(cert), nil
}

func ParseSHA256Fingerprint(value string) ([]byte, error) {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	trimmed = strings.TrimPrefix(trimmed, FingerprintPrefixSHA256)
	trimmed = strings.ReplaceAll(trimmed, ":", "")
	if trimmed == "" {
		return nil, fmt.Errorf("fingerprint is required")
	}
	if len(trimmed) != sha256.Size*2 {
		return nil, fmt.Errorf("fingerprint must be sha256:<64 hex chars>")
	}
	b, err := hex.DecodeString(trimmed)
	if err != nil {
		return nil, fmt.Errorf("fingerprint must be sha256:<64 hex chars>")
	}
	return b, nil
}

func FormatSHA256Fingerprint(b []byte) string {
	return FingerprintPrefixSHA256 + hex.EncodeToString(b)
}
