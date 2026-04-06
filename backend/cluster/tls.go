// Package cluster provides the mTLS transport layer for opsagent's
// primary/slave communication. The protocol is a framed bidirectional stream
// of protobuf messages over a raw TLS TCP connection.
package cluster

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// LoadTLSConfig builds a tls.Config for mutual TLS authentication.
// Both server and client use the same config shape: each side presents its
// own cert and verifies the peer's cert against the shared CA.
func LoadTLSConfig(caPath, certPath, keyPath string) (*tls.Config, error) {
	caCert, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("reading CA cert %q: %w", caPath, err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("CA cert %q contains no valid certificates", caPath)
	}

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("loading cert/key (%q, %q): %w", certPath, keyPath, err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool, // client side: verify the server's cert
		ClientCAs:    caPool, // server side: verify the client's cert
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}
