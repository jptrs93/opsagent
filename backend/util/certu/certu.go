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
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCert) {
		panic(fmt.Sprintf("CA cert %q contains no valid certificates", caPath))
	}

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		panic(fmt.Sprintf("loading cert/key (%q, %q): %v", certPath, keyPath, err))
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool, // client side: verify the server's cert
		ClientCAs:    caPool, // server side: verify the client's cert
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
}

func MustCertLoadCommonName(certPath string) string {
	certBytes, err := os.ReadFile(certPath)
	if err != nil {
		panic(fmt.Sprintf("reading cluster cert %q: %v", certPath, err))
	}
	block, _ := pem.Decode(certBytes)
	if block == nil {
		panic(fmt.Sprintf("cluster cert %q contains no PEM data", certPath))
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		panic(fmt.Sprintf("parsing cluster cert %q: %v", certPath, err))
	}
	if cert.Subject.CommonName == "" {
		panic(fmt.Sprintf("cluster cert %q has no CN", certPath))
	}
	return cert.Subject.CommonName
}
