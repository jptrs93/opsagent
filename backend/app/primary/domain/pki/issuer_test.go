package pki

import (
	"crypto/x509"
	"encoding/pem"
	"slices"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
)

func issuedTLSEvent(spaceID uint64, extraNames ...string) *apigen.DeploymentRecord {
	return &apigen.DeploymentRecord{Deployment: apigen.Deployment{ID: 7, Name: "api", SpaceID: spaceID, Spec: apigen.DeploymentSpec{
		Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL},
		Workload: apigen.Workload{Value: apigen.WorkloadValueOneof{Container: &apigen.ContainerSpec{
			Source:          apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{RemoteImage: &apigen.RemoteImage{Image: "example/api"}}},
			UpgradeStrategy: apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE,
			Runtime:         apigen.ContainerRuntime{IssuedTlsMount: apigen.Some(apigen.IssuedTLSMount{ContainerPath: "/tls", ExtraNames: extraNames})},
		}}},
	}}}
}

func TestIssueRejectsExtraNamesOutsideOwnSpace(t *testing.T) {
	issuer := &Issuer{Secrets: newTestSecrets(t)}
	_, err := issuer.Issue(issuedTLSEvent(2, "api.space-1.internal"))
	if err == nil || !strings.Contains(err.Error(), "api.space-1.internal") {
		t.Fatalf("err = %v, want rejection of another space's name", err)
	}
	res, err := issuer.Issue(issuedTLSEvent(2, "api.example.com", "alias.space-2.internal"))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(res.CertPem)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{network.DeploymentDNSName("api", 2), "api.example.com", "alias.space-2.internal"} {
		if !slices.Contains(cert.DNSNames, want) {
			t.Errorf("DNSNames %v missing %s", cert.DNSNames, want)
		}
	}
}
