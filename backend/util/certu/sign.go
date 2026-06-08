package certu

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"
)

func GenerateClusterCA(name string) (certPEM, keyPEM []byte, err error) {
	if name == "" {
		return nil, nil, fmt.Errorf("CA name is empty")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: name,
		},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return nil, nil, fmt.Errorf("signing CA certificate: %w", err)
	}
	keyPEM, err = marshalPrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), keyPEM, nil
}

func GenerateNodeCertificate(caCertPEM, caKeyPEM []byte, nodeName string) (certPEM, keyPEM []byte, err error) {
	if nodeName == "" {
		return nil, nil, fmt.Errorf("node name is empty")
	}
	_, caCert, err := parseCertificate(caCertPEM, "CA cert")
	if err != nil {
		return nil, nil, err
	}
	caKey, err := parsePrivateKey(caKeyPEM, "CA key")
	if err != nil {
		return nil, nil, err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating node key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: nodeName,
		},
		DNSNames:    []string{nodeName},
		NotBefore:   time.Now().Add(-time.Minute),
		NotAfter:    time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(nodeName); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, pub, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("signing node certificate: %w", err)
	}
	keyPEM, err = marshalPrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), keyPEM, nil
}

func GenerateWorkerCertificate(caPath, caKeyPath string, workerName string) ([]byte, []byte, []byte, error) {
	caPEM, _, err := loadCertificate(caPath)
	if err != nil {
		return nil, nil, nil, err
	}
	caKeyPEM, err := os.ReadFile(caKeyPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("reading CA key %q: %w", caKeyPath, err)
	}
	return GenerateWorkerCertificateFromPEM(caPEM, caKeyPEM, workerName)
}

func GenerateWorkerCertificateFromPEM(caCertPEM, caKeyPEM []byte, workerName string) ([]byte, []byte, []byte, error) {
	if workerName == "" {
		return nil, nil, nil, fmt.Errorf("worker name is empty")
	}
	_, caCert, err := parseCertificate(caCertPEM, "CA cert")
	if err != nil {
		return nil, nil, nil, err
	}
	caKey, err := parsePrivateKey(caKeyPEM, "CA key")
	if err != nil {
		return nil, nil, nil, err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generating worker key: %w", err)
	}
	keyPEM, err := marshalPrivateKey(priv)
	if err != nil {
		return nil, nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: workerName,
		},
		NotBefore:   time.Now().Add(-time.Minute),
		NotAfter:    time.Now().Add(90 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, pub, caKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("signing worker certificate: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return caCertPEM, certPEM, keyPEM, nil
}

func loadCertificate(path string) ([]byte, *x509.Certificate, error) {
	certPEM, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("reading CA cert %q: %w", path, err)
	}
	return parseCertificate(certPEM, fmt.Sprintf("CA cert %q", path))
}

func parseCertificate(certPEM []byte, label string) ([]byte, *x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, nil, fmt.Errorf("%s contains no certificate PEM", label)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing %s: %w", label, err)
	}
	return certPEM, cert, nil
}

func parsePrivateKey(keyPEM []byte, label string) (any, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("%s contains no PEM data", label)
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, fmt.Errorf("parsing %s: unsupported private key format", label)
}

func marshalPrivateKey(key any) ([]byte, error) {
	privDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshalling private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}), nil
}

func randomSerial() (*big.Int, error) {
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, fmt.Errorf("generating cert serial: %w", err)
	}
	return serial, nil
}
