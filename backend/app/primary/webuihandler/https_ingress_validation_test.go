package webuihandler

import (
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
	"github.com/jptrs93/opsagent/backend/util/certu"
)

func TestHTTPSIngressUpdateOnSecondaryWithPassthrough(t *testing.T) {
	dir := t.TempDir()
	store := state.Open(filepath.Join(dir, "primary.db"))
	secretManager, err := secrets.Initialize(dir, store)
	if err != nil {
		t.Fatalf("secrets.Initialize: %v", err)
	}
	primaryNode := nodes.EnsurePrimaryNode(store, "primary", "primary")
	secondaryNode := nodes.EnsurePrimaryNode(store, "secondary-2", "secondary-2")
	h := &Handler{Store: store, Queries: store.Queries(), Secrets: secretManager, NodeID: primaryNode.ID}

	certPEM, keyPEM, err := certu.GenerateSelfSignedServerCertificate([]string{"web.ingress.opendeploy.test"})
	if err != nil {
		t.Fatalf("generating certificate: %v", err)
	}
	certSecret, err := secretManager.Create("e2e.tls.ingress.web", append(certPEM, keyPEM...), 0, 1, 0)
	if err != nil {
		t.Fatalf("creating cert secret: %v", err)
	}

	passthroughSpec := func(hostname string) *apigen.DeploymentSpec {
		spec := remoteDeploymentSpec("nginx", apigen.NetworkingConfig{
			Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
			Ingress: []*apigen.Ingress{{
				Kind:                 apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH,
				Hostname:             hostname,
				TlsPassthroughConfig: &apigen.TlsPassthroughConfig{ContainerPort: 8443},
			}},
		})
		return &spec
	}
	for _, hostname := range []string{"one.ingress.opendeploy.test", "two.ingress.opendeploy.test"} {
		cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, 1, "tls-"+hostname, secondaryNode.ID, passthroughSpec(hostname))
		if err := deployments.ValidateNodeNetworkingClaims(nodes.MustReadLiveState(h.Store.Queries()), h.webUIReservations(), secondaryNode.ID, cfg.DeploymentID, passthroughSpec(hostname)); err != nil {
			t.Fatalf("passthrough claims for %s rejected: %v", hostname, err)
		}
	}

	echoSpec := remoteDeploymentSpec("httpecho", apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL})
	echo := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, 1, "https-echo-root", secondaryNode.ID, &echoSpec)

	updated := remoteDeploymentSpec("httpecho", apigen.NetworkingConfig{
		Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
		Ingress: []*apigen.Ingress{{
			Kind:     apigen.IngressKind_INGRESS_KIND_HTTPS,
			Hostname: "web.ingress.opendeploy.test",
			HttpsConfig: &apigen.HttpsConfig{
				ContainerPort: 8080,
				CertSource:    &apigen.CertSource{Secret: &apigen.SecretCertSource{SecretVersionID: certSecret.ID}},
			},
		}},
	})
	validated, err := deployments.ValidateSpec(h.Store, h.Secrets, &updated)
	if err != nil {
		t.Fatalf("deployments.ValidateSpec rejected HTTPS ingress: %v", err)
	}
	if err := deployments.ValidateNodeNetworkingClaims(nodes.MustReadLiveState(h.Store.Queries()), h.webUIReservations(), echo.Value.NodeID, echo.DeploymentID, validated); err != nil {
		t.Fatalf("ValidateNodeNetworkingClaims rejected HTTPS ingress: %v", err)
	}
}
