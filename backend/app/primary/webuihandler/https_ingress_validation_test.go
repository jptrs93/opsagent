package webuihandler

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/util/certu"
)

func TestHTTPSIngressUpdateOnWorkerWithPassthrough(t *testing.T) {
	dir := t.TempDir()
	store := state.Open(filepath.Join(dir, "primary.db"))
	secretManager, err := secrets.Initialize(dir, store)
	if err != nil {
		t.Fatalf("secrets.Initialize: %v", err)
	}
	primaryNode := store.EnsurePrimaryNode("primary", "primary")
	workerNode := store.EnsurePrimaryNode("worker-2", "worker-2")
	h := &Handler{Store: store, Secrets: secretManager, NodeID: primaryNode.ID}

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
		cfg := store.MustCreateDeploymentForNode(apigen.Context{}, 1, "tls-"+hostname, workerNode.ID, passthroughSpec(hostname))
		if err := h.validateNodeNetworkingClaims(workerNode.ID, cfg.ID, passthroughSpec(hostname)); err != nil {
			t.Fatalf("passthrough claims for %s rejected: %v", hostname, err)
		}
	}

	echoSpec := remoteDeploymentSpec("httpecho", apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL})
	echo := store.MustCreateDeploymentForNode(apigen.Context{}, 1, "https-echo-root", workerNode.ID, &echoSpec)

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
	validated, err := h.validateDeploymentSpec(&updated)
	if err != nil {
		t.Fatalf("validateDeploymentSpec rejected HTTPS ingress: %v", err)
	}
	if err := h.validateNodeNetworkingClaims(echo.NodeID, echo.ID, validated); err != nil {
		t.Fatalf("validateNodeNetworkingClaims rejected HTTPS ingress: %v", err)
	}
}
