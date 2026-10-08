package webuihandler

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
	"github.com/jptrs93/opsagent/backend/lib/ingressplan"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func httpsSpec(hostname string, listen ...apigen.IngressListen) *apigen.DeploymentSpec {
	spec := remoteDeploymentSpec("httpecho", apigen.NetworkingConfig{
		Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
		Ingress: []apigen.Ingress{{
			Hostname: hostname,
			Listen:   listen,
			Config:   apigen.IngressConfig{Value: apigen.IngressConfigValueOneof{Https: &apigen.HttpsConfig{ContainerPort: 8080, PathPrefix: "/", BackendProtocol: apigen.HttpBackendProtocol_HTTP_BACKEND_PROTOCOL_HTTP1}}},
		}},
	})
	return &spec
}

func listenAddresses(values ...string) apigen.IngressListen {
	return apigen.IngressListen{Addresses: mustPrefixes(values...)}
}

func listenNode(nodeID uint64) apigen.IngressListen {
	return apigen.IngressListen{Node: apigen.Some(apigen.IngressNode{Value: apigen.IngressNodeValueOneof{Specific: &apigen.SpecificNode{NodeID: nodeID}}})}
}

func hostAddresses(values ...string) []apigen.IpAddress {
	out := make([]apigen.IpAddress, 0, len(values))
	for _, v := range values {
		out = append(out, mustAddr(v))
	}
	return out
}

func TestIngressListenOnPrimaryAgainstWebUIReservation(t *testing.T) {
	dir := t.TempDir()
	store := state.Open(filepath.Join(dir, "primary.db"))
	defer store.Close()
	primary := ensureTestNode(store, "primary", "primary-id")
	nodes.ReportNode(store, "primary-id", apigen.NodeReported{Identifier: "primary-id", HostAddresses: hostAddresses("203.0.113.10", "2001:db8::10")})
	echo := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, 1, "echo", primary.ID, httpsSpec("web.example.test"))

	wildcardWebUI := ingressplan.WebUIReservations(primary.ID, true, ":443", false, "")
	// Default listen: every address intersects the wildcard reservation, so
	// the save is rejected naming the Web UI.
	err := deployments.ValidateNodeNetworkingClaims(liveNodes(store.Queries()), wildcardWebUI, primary.ID, echo.Deployment.ID, httpsSpec("web.example.test"))
	if err == nil || !strings.Contains(err.Error(), "reserved by the primary Web UI") {
		t.Fatalf("default listen on a wildcard-reserved port must be rejected, got %v", err)
	}
	// Literal address on the reserved port: rejected the same way.
	literal := listenAddresses("203.0.113.10")
	err = deployments.ValidateNodeNetworkingClaims(liveNodes(store.Queries()), wildcardWebUI, primary.ID, echo.Deployment.ID, httpsSpec("web.example.test", literal))
	if err == nil || !strings.Contains(err.Error(), "reserved by the primary Web UI") {
		t.Fatalf("literal listen on a wildcard-reserved port must be rejected, got %v", err)
	}
	// Web UI bound to the IPv6 address: the IPv4 literal is fine, the IPv6
	// literal is not.
	v6WebUI := ingressplan.WebUIReservations(primary.ID, true, "[2001:db8::10]:443", false, "")
	if err := deployments.ValidateNodeNetworkingClaims(liveNodes(store.Queries()), v6WebUI, primary.ID, echo.Deployment.ID, httpsSpec("web.example.test", literal)); err != nil {
		t.Fatalf("IPv4 literal beside an IPv6 Web UI listen rejected: %v", err)
	}
	v6Literal := listenAddresses("2001:db8::10")
	if err := deployments.ValidateNodeNetworkingClaims(liveNodes(store.Queries()), v6WebUI, primary.ID, echo.Deployment.ID, httpsSpec("web.example.test", v6Literal)); err == nil {
		t.Fatal("the literal equal to the Web UI address must be rejected")
	}
	// Node selectors must name a registered node, and only the hosting node
	// until cross-node backend dialling exists.
	unknown := listenNode(99)
	if err := deployments.ValidateNodeNetworkingClaims(liveNodes(store.Queries()), nil, primary.ID, echo.Deployment.ID, httpsSpec("web.example.test", unknown)); err == nil || !strings.Contains(err.Error(), "unknown node id 99") {
		t.Fatalf("unknown node selector must be rejected, got %v", err)
	}
	other := ensureTestNode(store, "worker-2", "worker-2-id")
	crossNode := listenNode(other.ID)
	err = deployments.ValidateNodeNetworkingClaims(liveNodes(store.Queries()), nil, primary.ID, echo.Deployment.ID, httpsSpec("web.example.test", crossNode))
	if err == nil || !strings.Contains(err.Error(), `node "worker-2" cannot publish a route for a deployment on another node`) {
		t.Fatalf("cross-node listen selector must be rejected, got %v", err)
	}
	ownNode := listenNode(primary.ID)
	if err := deployments.ValidateNodeNetworkingClaims(liveNodes(store.Queries()), nil, primary.ID, echo.Deployment.ID, httpsSpec("web.example.test", ownNode)); err != nil {
		t.Fatalf("own-node listen selector rejected: %v", err)
	}
}

func TestIngressListenCollisionBetweenDeployments(t *testing.T) {
	dir := t.TempDir()
	store := state.Open(filepath.Join(dir, "primary.db"))
	defer store.Close()
	worker := ensureTestNode(store, "worker-2", "worker-2-id")
	nodes.ReportNode(store, "worker-2-id", apigen.NodeReported{Identifier: "worker-2-id", HostAddresses: hostAddresses("203.0.113.20", "203.0.113.21")})
	root := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, 1, "root", worker.ID, httpsSpec("web.example.test"))
	api := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, 1, "api", worker.ID, &remoteVirtualSpec)
	_ = root
	overlap := listenAddresses("203.0.113.20")
	err := deployments.ValidateNodeNetworkingClaims(liveNodes(store.Queries()), nil, worker.ID, api.Deployment.ID, httpsSpec("web.example.test", overlap))
	if err == nil || !strings.Contains(err.Error(), "already claimed by another deployment") {
		t.Fatalf("overlapping selector must be rejected, got %v", err)
	}
	// A create (id 0) is evaluated as the newcomer too.
	if err := deployments.ValidateNodeNetworkingClaims(liveNodes(store.Queries()), nil, worker.ID, 0, httpsSpec("web.example.test")); err == nil {
		t.Fatal("a new deployment claiming an owned hostname must be rejected")
	}
	if err := deployments.ValidateNodeNetworkingClaims(liveNodes(store.Queries()), nil, worker.ID, 0, httpsSpec("other.example.test")); err != nil {
		t.Fatalf("a distinct hostname is accepted: %v", err)
	}
}

var remoteVirtualSpec = remoteDeploymentSpec("httpecho", apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL})

func TestValidateIngressListenShape(t *testing.T) {
	cases := []struct {
		name    string
		entry   apigen.IngressListen
		wantErr string
		want    []string
	}{
		{name: "empty", entry: apigen.IngressListen{}},
		{name: "node without a choice", entry: apigen.IngressListen{Node: apigen.Some(apigen.IngressNode{})}, wantErr: "any or specific is required"},
		{name: "literal canonical", entry: listenAddresses("203.0.113.10", "2001:DB8::1/128", "198.51.100.7/24"), want: []string{"203.0.113.10/32", "2001:db8::1/128", "198.51.100.0/24"}},
		{name: "garbage", entry: apigen.IngressListen{Addresses: []apigen.IpPrefix{{}}}, wantErr: "not an IP address or CIDR prefix"},
		{name: "duplicate", entry: listenAddresses("203.0.113.10", "203.0.113.10/32"), wantErr: "duplicate entry"},
	}
	for _, tc := range cases {
		entries := []apigen.IngressListen{tc.entry}
		err := deployments.ValidateIngressListen(entries)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("%s: got %v, want %q", tc.name, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if tc.want != nil {
			got := entries[0].Addresses
			if len(got) != len(tc.want) {
				t.Fatalf("%s: prefixes %v, want %v", tc.name, got, tc.want)
			}
			for i := range got {
				if got[i].Prefix().String() != tc.want[i] {
					t.Fatalf("%s: prefix %d = %s, want %s", tc.name, i, got[i].Prefix(), tc.want[i])
				}
			}
		}
	}
}

func TestSettingsChangeRejectedWhenLiteralClaimBecomesReserved(t *testing.T) {
	dir := t.TempDir()
	store := state.Open(filepath.Join(dir, "primary.db"))
	defer store.Close()
	primary := ensureTestNode(store, "primary", "primary-id")
	nodes.ReportNode(store, "primary-id", apigen.NodeReported{Identifier: "primary-id", HostAddresses: hostAddresses("203.0.113.10", "2001:db8::10")})
	literal := listenAddresses("203.0.113.10")
	statetest.MustCreateDeploymentForNode(store, apigen.Context{}, 1, "echo", primary.ID, httpsSpec("web.example.test", literal))

	settings := func(listen string) *apigen.ClusterSettings {
		return &apigen.ClusterSettings{
			HttpsWeb: apigen.HttpsWebSettings{Enabled: systemconfig.BoolLiteral(true), Listen: systemconfig.StringLiteral(listen)},
		}
	}
	if err := deployments.ValidateIngressAgainstSettings(context.Background(), store.Queries(), primary.ID, settings("[2001:db8::10]:443")); err != nil {
		t.Fatalf("Web UI on the other address is fine: %v", err)
	}
	if err := deployments.ValidateIngressAgainstSettings(context.Background(), store.Queries(), primary.ID, settings(":443")); err == nil || !strings.Contains(err.Error(), `deployment "echo"`) {
		t.Fatalf("a wildcard Web UI listen must be rejected while a literal claim exists, got %v", err)
	}
}
