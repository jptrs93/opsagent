package secondary

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
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

func TestCacheEnrollmentBootstrapStateRequiresNetwork(t *testing.T) {
	accepted := enrollmentAcceptedWithBootstrap(t, "worker-1")
	accepted.ClusterNetwork = nil
	if err := cacheEnrollmentBootstrapState(EnrollmentConfig{DataDir: t.TempDir()}, accepted); err == nil {
		t.Fatal("cacheEnrollmentBootstrapState succeeded without network")
	}
}

func TestMustLoadRuntimeConfigLoadsCachedBootstrapState(t *testing.T) {
	dataDir := t.TempDir()
	accepted := enrollmentAcceptedWithBootstrap(t, "worker-1")

	if err := cacheEnrollmentBootstrapState(EnrollmentConfig{DataDir: dataDir}, accepted); err != nil {
		t.Fatalf("cacheEnrollmentBootstrapState: %v", err)
	}
	caPath, certPath, keyPath := writeRuntimeTLS(t, dataDir, "worker-1")
	cfg := MustLoadRuntimeConfig(ainit.StaticConfiguration{
		DataDir:            dataDir,
		GitCacheDir:        filepath.Join(dataDir, "git-cache"),
		ReleasesDir:        dataDir + "-releases",
		NetproxyStatePath:  filepath.Join(dataDir, "netproxy", "netstate.pb"),
		PrimaryClusterAddr: "primary:9443",
		PrimaryName:        "primary",
	}, caPath, certPath, keyPath)

	wantPrefix, err := network.ParsePrefix(accepted.ClusterNetwork.UlaPrefix)
	if err != nil {
		t.Fatalf("ParsePrefix: %v", err)
	}
	if cfg.TLS == nil || cfg.NodeIdentifier != "worker-1" || cfg.NodeID != 2 || cfg.ClusterPrefix != wantPrefix || cfg.NetDeploymentID != 11 {
		t.Fatalf("runtime config = %+v", cfg)
	}
}

func TestCacheEnrollmentBootstrapStatePersistsNetworkMap(t *testing.T) {
	dataDir := t.TempDir()
	accepted := enrollmentAcceptedWithBootstrap(t, "worker-1")
	accepted.ClusterNetMap = &apigen.ClusterNetMap{
		Generation:   "generation-a",
		Sequence:     1,
		TargetNodeID: 2,
		UlaPrefix:    accepted.ClusterNetwork.UlaPrefix,
		Nodes: []*apigen.ClusterNetMapNode{
			{NodeID: 2, UnderlayAddress: "192.0.2.2"},
		},
	}
	if err := cacheEnrollmentBootstrapState(EnrollmentConfig{DataDir: dataDir}, accepted); err != nil {
		t.Fatal(err)
	}
	store := sqlite.NewSecondaryStorage(filepath.Join(dataDir, "secondary.db"))
	defer store.Close()
	cached, _, ok, err := cachedClusterNetMap(store, 2, network.Prefix{})
	if err != nil || !ok {
		t.Fatalf("cached map: ok=%v err=%v", ok, err)
	}
	if cached.Generation != "generation-a" || cached.Sequence != 1 || cached.TargetNodeID != 2 {
		t.Fatalf("cached map = %+v", cached)
	}
}

func TestMustLoadRuntimeConfigRequiresCachedBootstrapState(t *testing.T) {
	dataDir := t.TempDir()
	caPath, certPath, keyPath := writeRuntimeTLS(t, dataDir, "worker-1")
	defer func() {
		if recover() == nil {
			t.Fatal("MustLoadRuntimeConfig did not panic without cached bootstrap state")
		}
	}()
	MustLoadRuntimeConfig(ainit.StaticConfiguration{DataDir: dataDir}, caPath, certPath, keyPath)
}

func enrollmentAcceptedWithBootstrap(t *testing.T, machine string) *apigen.EnrollmentAccepted {
	t.Helper()
	prefix := network.GeneratePrefix()
	return &apigen.EnrollmentAccepted{
		ID:             1,
		WorkerName:     machine,
		ClusterNetwork: &apigen.ClusterNetworkInfo{UlaPrefix: prefix.Bytes()},
		NodeDeployment: &apigen.DeploymentWithStatus{Config: apigen.DeploymentConfig{
			ID:     10,
			NodeID: 2,
			Spec:   *sqlite.SystemDeploymentSpec(),
			Identity: apigen.DeploymentIdentity{
				SpaceID: internaldeploy.SpaceID,
				Name:    internaldeploy.SelfName,
			},
		}},
		NodeNetDeployment: &apigen.DeploymentWithStatus{Config: apigen.DeploymentConfig{
			ID:     11,
			NodeID: 2,
			Spec:   *sqlite.NetproxyDeploymentSpec(),
			Identity: apigen.DeploymentIdentity{
				SpaceID: internaldeploy.SpaceID,
				Name:    internaldeploy.NetproxyName,
			},
		}},
	}
}

func writeRuntimeTLS(t *testing.T, dataDir, machine string) (string, string, string) {
	t.Helper()
	caCert, caKey, err := certu.GenerateClusterCA("test-ca")
	if err != nil {
		t.Fatalf("GenerateClusterCA: %v", err)
	}
	cert, key, err := certu.GenerateNodeCertificate(caCert, caKey, machine)
	if err != nil {
		t.Fatalf("GenerateNodeCertificate: %v", err)
	}
	caPath, certPath, keyPath := certu.WorkerTLSPaths(filepath.Join(dataDir, "tls"))
	if err := os.MkdirAll(filepath.Dir(caPath), 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	for path, contents := range map[string][]byte{caPath: caCert, certPath: cert, keyPath: key} {
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", path, err)
		}
	}
	return caPath, certPath, keyPath
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
