package certu

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

// MustLoadTLSConfig builds a tls.Config for mutual TLS authentication, panicking
// on any failure. Both the cluster server and the worker client use the same
// config shape: each side presents its own cert and verifies the peer against
// the shared CA. A load failure here is a fatal misconfiguration with no
// sensible recovery, so it panics rather than returning an error.
func MustLoadTLSConfig(caPath, certPath, keyPath string) *tls.Config {
	caCert, err := os.ReadFile(caPath)
	if err != nil {
		panic(fmt.Sprintf("reading CA cert %q: %v", caPath, err))
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		panic(fmt.Sprintf("reading cluster cert %q: %v", certPath, err))
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		panic(fmt.Sprintf("reading cluster key %q: %v", keyPath, err))
	}
	return MustLoadTLSConfigFromPEM(caCert, certPEM, keyPEM)
}

func MustLoadTLSConfigFromPEM(caCertPEM, certPEM, keyPEM []byte) *tls.Config {
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCertPEM) {
		panic("CA cert contains no valid certificates")
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		panic(fmt.Sprintf("loading cert/key: %v", err))
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool, // client side: verify the server's cert
		ClientCAs:    caPool, // server side: verify the client's cert
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
}

func MustLoadServerTLSConfigFromPEM(certPEM, keyPEM []byte) *tls.Config {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		panic(fmt.Sprintf("loading cert/key: %v", err))
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}
}

func MustCertLoadCommonName(certPath string) string {
	certBytes, err := os.ReadFile(certPath)
	if err != nil {
		panic(fmt.Sprintf("reading cluster cert %q: %v", certPath, err))
	}
	return MustCertCommonNameFromPEM(certBytes)
}

func MustCertCommonNameFromPEM(certBytes []byte) string {
	block, _ := pem.Decode(certBytes)
	if block == nil {
		panic("cluster cert contains no PEM data")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		panic(fmt.Sprintf("parsing cluster cert: %v", err))
	}
	if cert.Subject.CommonName == "" {
		panic("cluster cert has no CN")
	}
	return cert.Subject.CommonName
}
