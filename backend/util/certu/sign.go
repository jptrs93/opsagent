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
	"strings"
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
		NotAfter:              time.Now().Add(caValidity),
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

const (
	caValidity         = 100 * 365 * 24 * time.Hour
	workerCertValidity = 365 * 24 * time.Hour
)

func GenerateWorkloadCA(name string) (certPEM, keyPEM []byte, err error) {
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
		NotAfter:              time.Now().Add(caValidity),
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
	return GenerateNodeCertificateWithServerName(caCertPEM, caKeyPEM, nodeName, nodeName)
}

func GenerateNodeCertificateWithServerName(caCertPEM, caKeyPEM []byte, commonName, serverName string) (certPEM, keyPEM []byte, err error) {
	if commonName == "" {
		return nil, nil, fmt.Errorf("node name is empty")
	}
	if serverName == "" {
		return nil, nil, fmt.Errorf("server name is empty")
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
			CommonName: commonName,
		},
		DNSNames:    []string{serverName},
		NotBefore:   time.Now().Add(-time.Minute),
		NotAfter:    time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(serverName); ip != nil {
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

func SignWorkloadCertificate(caCertPEM, caKeyPEM []byte, commonName string, names []string, validity time.Duration) (certPEM, keyPEM []byte, err error) {
	if commonName == "" {
		return nil, nil, fmt.Errorf("workload certificate common name is empty")
	}
	_, caCert, err := parseCertificate(caCertPEM, "workload CA cert")
	if err != nil {
		return nil, nil, err
	}
	caKey, err := parsePrivateKey(caKeyPEM, "workload CA key")
	if err != nil {
		return nil, nil, err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating workload key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	dnsNames, ipAddresses := serverCertificateNames(names)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: commonName,
		},
		DNSNames:    dnsNames,
		IPAddresses: ipAddresses,
		NotBefore:   time.Now().Add(-time.Minute),
		NotAfter:    time.Now().Add(validity),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, pub, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("signing workload certificate: %w", err)
	}
	keyPEM, err = marshalPrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), keyPEM, nil
}

func GenerateSelfSignedServerCertificate(names []string) (certPEM, keyPEM []byte, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating server key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	dnsNames, ipAddresses := serverCertificateNames(names)
	commonName := "opendeploy"
	if len(dnsNames) > 0 {
		commonName = dnsNames[0]
	} else if len(ipAddresses) > 0 {
		commonName = ipAddresses[0].String()
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: commonName,
		},
		DNSNames:              dnsNames,
		IPAddresses:           ipAddresses,
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(2 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return nil, nil, fmt.Errorf("signing self-signed server certificate: %w", err)
	}
	keyPEM, err = marshalPrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), keyPEM, nil
}

func serverCertificateNames(names []string) ([]string, []net.IP) {
	dnsSeen := map[string]bool{}
	ipSeen := map[string]bool{}
	dnsNames := []string{}
	ipAddresses := []net.IP{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || name == "0.0.0.0" || name == "::" {
			return
		}
		if host, _, err := net.SplitHostPort(name); err == nil {
			name = strings.Trim(host, "[]")
		}
		if ip := net.ParseIP(name); ip != nil {
			key := ip.String()
			if !ipSeen[key] {
				ipSeen[key] = true
				ipAddresses = append(ipAddresses, ip)
			}
			return
		}
		if !dnsSeen[name] {
			dnsSeen[name] = true
			dnsNames = append(dnsNames, name)
		}
	}
	for _, name := range names {
		add(name)
	}
	add("localhost")
	add("127.0.0.1")
	add("::1")
	return dnsNames, ipAddresses
}

func GenerateWorkerCertificateRequest(requestingMachineID string) (csrPEM, keyPEM []byte, err error) {
	if requestingMachineID == "" {
		return nil, nil, fmt.Errorf("requesting machine ID is empty")
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating worker key: %w", err)
	}
	keyPEM, err = marshalPrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: requestingMachineID},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, tmpl, priv)
	if err != nil {
		return nil, nil, fmt.Errorf("creating worker CSR: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}), keyPEM, nil
}

func SignWorkerCertificateRequestFromPEM(caCertPEM, caKeyPEM, csrPEM []byte, identifier string) ([]byte, []byte, error) {
	if identifier == "" {
		return nil, nil, fmt.Errorf("worker identifier is empty")
	}
	_, caCert, err := parseCertificate(caCertPEM, "CA cert")
	if err != nil {
		return nil, nil, err
	}
	caKey, err := parsePrivateKey(caKeyPEM, "CA key")
	if err != nil {
		return nil, nil, err
	}
	csr, err := parseCertificateRequest(csrPEM)
	if err != nil {
		return nil, nil, err
	}
	if signatureErr := csr.CheckSignature(); signatureErr != nil {
		return nil, nil, fmt.Errorf("validating worker CSR signature: %w", signatureErr)
	}
	if csr.Subject.CommonName != identifier {
		return nil, nil, fmt.Errorf("worker CSR common name does not match identifier")
	}
	certPEM, _, err := signWorkerCertificate(caCert, caKey, identifier, csr.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	return caCertPEM, certPEM, nil
}

func SignWorkerCertificateFromPublicKey(caCertPEM, caKeyPEM []byte, identifier string, publicKey any) (certPEM []byte, notAfter time.Time, err error) {
	if identifier == "" {
		return nil, time.Time{}, fmt.Errorf("worker identifier is empty")
	}
	_, caCert, err := parseCertificate(caCertPEM, "CA cert")
	if err != nil {
		return nil, time.Time{}, err
	}
	caKey, err := parsePrivateKey(caKeyPEM, "CA key")
	if err != nil {
		return nil, time.Time{}, err
	}
	return signWorkerCertificate(caCert, caKey, identifier, publicKey)
}

func signWorkerCertificate(caCert *x509.Certificate, caKey any, identifier string, publicKey any) (certPEM []byte, notAfter time.Time, err error) {
	serial, err := randomSerial()
	if err != nil {
		return nil, time.Time{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: identifier,
		},
		NotBefore:   time.Now().Add(-time.Minute),
		NotAfter:    time.Now().Add(workerCertValidity),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, publicKey, caKey)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("signing worker certificate: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), tmpl.NotAfter, nil
}

// CertificateNotAfter returns the NotAfter time of the first certificate in certPEM.
func CertificateNotAfter(certPEM []byte) (time.Time, error) {
	_, cert, err := parseCertificate(certPEM, "certificate")
	if err != nil {
		return time.Time{}, err
	}
	return cert.NotAfter, nil
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

func parseCertificateRequest(csrPEM []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, fmt.Errorf("worker CSR contains no certificate request PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing worker CSR: %w", err)
	}
	return csr, nil
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
