package webuihandler

import (
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
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
	primaryNode := ensureTestNode(store, "primary", "primary")
	secondaryNode := ensureTestNode(store, "secondary-2", "secondary-2")
	configService, err := systemconfig.InitializeService(store, *systemconfig.Default(systemconfig.DefaultInitial()))
	if err != nil {
		t.Fatalf("systemconfig.InitializeService: %v", err)
	}
	h := &Handler{Store: store, Queries: store.Queries(), Secrets: secretManager, NodeID: primaryNode.ID, SystemConfig: configService, Config: func() *apigen.ClusterSettings {
		settings := configService.Snapshot().Settings
		return &settings
	}}

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
			Mode:    apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
			Ingress: []apigen.Ingress{tlsPassthroughIngress(hostname, 8443)},
		})
		return &spec
	}
	for _, hostname := range []string{"one.ingress.opendeploy.test", "two.ingress.opendeploy.test"} {
		cfg := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, 1, "tls-"+hostname, secondaryNode.ID, passthroughSpec(hostname))
		if err := deployments.ValidateNodeNetworkingClaims(liveNodes(h.Store.Queries()), h.webUIReservations(), secondaryNode.ID, cfg.Deployment.ID, passthroughSpec(hostname)); err != nil {
			t.Fatalf("passthrough claims for %s rejected: %v", hostname, err)
		}
	}

	echoSpec := remoteDeploymentSpec("httpecho", apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL})
	echo := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, 1, "https-echo-root", secondaryNode.ID, &echoSpec)

	updated := remoteDeploymentSpec("httpecho", apigen.NetworkingConfig{
		Mode:    apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
		Ingress: []apigen.Ingress{httpsIngress("web.ingress.opendeploy.test", 8080, secretCertSource(certSecret.Ref()))},
	})
	validated, err := deployments.ValidateSpec(h.Store.Queries(), h.Secrets, &updated)
	if err != nil {
		t.Fatalf("deployments.ValidateSpec rejected HTTPS ingress: %v", err)
	}
	if err := deployments.ValidateNodeNetworkingClaims(liveNodes(h.Store.Queries()), h.webUIReservations(), echo.Deployment.PlacementNodeID(), echo.Deployment.ID, validated); err != nil {
		t.Fatalf("ValidateNodeNetworkingClaims rejected HTTPS ingress: %v", err)
	}
}
