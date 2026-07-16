package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureTestCertsReusesCAForServerRenewal(t *testing.T) {
	if !cmdExists("openssl") {
		t.Skip("openssl is not installed")
	}

	dir := t.TempDir()
	c := &config{
		CertDir:          dir,
		CACert:           filepath.Join(dir, "ca.crt"),
		CAKey:            filepath.Join(dir, "ca.key"),
		ServerCert:       filepath.Join(dir, "server.crt"),
		ServerKey:        filepath.Join(dir, "server.key"),
		ServerBundle:     filepath.Join(dir, "server-bundle.pem"),
		WebHost:          "primary.opendeploy.test",
		RepoMirrorName:   "opendeploy-repo-mirror",
		RepoRegistryHost: "opendeploy-repo-mirror",
	}
	if err := c.ensureTestCerts(); err != nil {
		t.Fatal(err)
	}
	firstCA, err := os.ReadFile(c.CACert)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(c.ServerCert); err != nil {
		t.Fatal(err)
	}
	if err := c.ensureTestCerts(); err != nil {
		t.Fatal(err)
	}
	secondCA, err := os.ReadFile(c.CACert)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(firstCA, secondCA) {
		t.Fatal("test CA changed while renewing the server certificate")
	}
	if !certSignedBy(c.ServerCert, c.CACert) {
		t.Fatal("renewed server certificate is not signed by the test CA")
	}
	for _, host := range tlsIngressHosts {
		cert, _, bundle, _, _ := c.tlsIngressCertPaths(host)
		if !certSignedBy(cert, c.CACert) || !certContainsDNS(cert, host) || !fileNonEmpty(bundle) {
			t.Fatalf("TLS ingress certificate is not valid for %s", host)
		}
	}
}
