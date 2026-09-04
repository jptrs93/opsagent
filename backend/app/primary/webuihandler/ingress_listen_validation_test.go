package webuihandler

import (
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/ingressplan"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func httpsSpec(hostname string, listen ...*apigen.IngressListen) *apigen.DeploymentSpec {
	spec := remoteDeploymentSpec("httpecho", apigen.NetworkingConfig{
		Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
		Ingress: []*apigen.Ingress{{
			Kind:        apigen.IngressKind_INGRESS_KIND_HTTPS,
			Hostname:    hostname,
			HttpsConfig: &apigen.HttpsConfig{ContainerPort: 8080, PathPrefix: "/"},
			Listen:      listen,
		}},
	})
	return &spec
}

func TestIngressListenOnPrimaryAgainstWebUIReservation(t *testing.T) {
	dir := t.TempDir()
	store := state.Open(filepath.Join(dir, "primary.db"))
	defer store.Close()
	primary := store.EnsurePrimaryNode("primary", "primary-id")
	store.SetNodeHostAddresses("primary-id", []string{"203.0.113.10", "2001:db8::10"})
	echo := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, 1, "echo", primary.ID, httpsSpec("web.example.test"))

	wildcardWebUI := ingressplan.WebUIReservations(primary.ID, true, ":443", false, "")
	// Default listen: accepted; the reservation drops the 443 claims instead
	// of rejecting the route.
	if err := validateNodeNetworkingClaims(store.LiveState(), wildcardWebUI, primary.ID, echo.ID, httpsSpec("web.example.test")); err != nil {
		t.Fatalf("default listen on the primary rejected: %v", err)
	}
	// Literal address on the reserved port: rejected naming the Web UI.
	literal := &apigen.IngressListen{Address: &apigen.AddressSelector{Prefixes: []string{"203.0.113.10"}}}
	err := validateNodeNetworkingClaims(store.LiveState(), wildcardWebUI, primary.ID, echo.ID, httpsSpec("web.example.test", literal))
	if err == nil || !strings.Contains(err.Error(), "reserved by the primary Web UI") {
		t.Fatalf("literal listen on a wildcard-reserved port must be rejected, got %v", err)
	}
	// Web UI bound to the IPv6 address: ipv4() and the IPv4 literal are fine,
	// the IPv6 literal is not.
	v6WebUI := ingressplan.WebUIReservations(primary.ID, true, "[2001:db8::10]:443", false, "")
	ipv4 := &apigen.IngressListen{Address: &apigen.AddressSelector{Family: apigen.AddressFamily_ADDRESS_FAMILY_IPV4}}
	if err := validateNodeNetworkingClaims(store.LiveState(), v6WebUI, primary.ID, echo.ID, httpsSpec("web.example.test", ipv4)); err != nil {
		t.Fatalf("ipv4() beside an IPv6 Web UI listen rejected: %v", err)
	}
	if err := validateNodeNetworkingClaims(store.LiveState(), v6WebUI, primary.ID, echo.ID, httpsSpec("web.example.test", literal)); err != nil {
		t.Fatalf("IPv4 literal beside an IPv6 Web UI listen rejected: %v", err)
	}
	v6Literal := &apigen.IngressListen{Address: &apigen.AddressSelector{Prefixes: []string{"2001:db8::10"}}}
	if err := validateNodeNetworkingClaims(store.LiveState(), v6WebUI, primary.ID, echo.ID, httpsSpec("web.example.test", v6Literal)); err == nil {
		t.Fatal("the literal equal to the Web UI address must be rejected")
	}
	// Node selectors must name a registered node, and only the hosting node
	// until cross-node backend dialling exists.
	unknown := &apigen.IngressListen{Node: &apigen.NodeSelector{NodeID: 99}}
	if err := validateNodeNetworkingClaims(store.LiveState(), nil, primary.ID, echo.ID, httpsSpec("web.example.test", unknown)); err == nil || !strings.Contains(err.Error(), "unknown node id 99") {
		t.Fatalf("unknown node selector must be rejected, got %v", err)
	}
	other := store.EnsurePrimaryNode("worker-2", "worker-2-id")
	crossNode := &apigen.IngressListen{Node: &apigen.NodeSelector{NodeID: other.ID}}
	err = validateNodeNetworkingClaims(store.LiveState(), nil, primary.ID, echo.ID, httpsSpec("web.example.test", crossNode))
	if err == nil || !strings.Contains(err.Error(), `node "worker-2" cannot publish a route for a deployment on another node`) {
		t.Fatalf("cross-node listen selector must be rejected, got %v", err)
	}
	ownNode := &apigen.IngressListen{Node: &apigen.NodeSelector{NodeID: primary.ID}}
	if err := validateNodeNetworkingClaims(store.LiveState(), nil, primary.ID, echo.ID, httpsSpec("web.example.test", ownNode)); err != nil {
		t.Fatalf("own-node listen selector rejected: %v", err)
	}
}

func TestIngressListenCollisionBetweenDeployments(t *testing.T) {
	dir := t.TempDir()
	store := state.Open(filepath.Join(dir, "primary.db"))
	defer store.Close()
	worker := store.EnsurePrimaryNode("worker-2", "worker-2-id")
	store.SetNodeHostAddresses("worker-2-id", []string{"203.0.113.20", "203.0.113.21"})
	root := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, 1, "root", worker.ID, httpsSpec("web.example.test"))
	api := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, 1, "api", worker.ID, &remoteVirtualSpec)
	_ = root
	overlap := &apigen.IngressListen{Address: &apigen.AddressSelector{Prefixes: []string{"203.0.113.20"}}}
	err := validateNodeNetworkingClaims(store.LiveState(), nil, worker.ID, api.ID, httpsSpec("web.example.test", overlap))
	if err == nil || !strings.Contains(err.Error(), "already claimed by another deployment") {
		t.Fatalf("overlapping selector must be rejected, got %v", err)
	}
	// A create (id 0) is evaluated as the newcomer too.
	if err := validateNodeNetworkingClaims(store.LiveState(), nil, worker.ID, 0, httpsSpec("web.example.test")); err == nil {
		t.Fatal("a new deployment claiming an owned hostname must be rejected")
	}
	if err := validateNodeNetworkingClaims(store.LiveState(), nil, worker.ID, 0, httpsSpec("other.example.test")); err != nil {
		t.Fatalf("a distinct hostname is accepted: %v", err)
	}
}

var remoteVirtualSpec = remoteDeploymentSpec("httpecho", apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL})

func TestValidateIngressListenShape(t *testing.T) {
	cases := []struct {
		name    string
		entry   *apigen.IngressListen
		wantErr string
		want    []string
	}{
		{name: "empty", entry: &apigen.IngressListen{}},
		{name: "any and node", entry: &apigen.IngressListen{Node: &apigen.NodeSelector{Any: true, NodeID: 1}}, wantErr: "mutually exclusive"},
		{name: "literal canonical", entry: &apigen.IngressListen{Address: &apigen.AddressSelector{Prefixes: []string{" 203.0.113.10 ", "2001:DB8::1/128", "198.51.100.7/24"}}}, want: []string{"203.0.113.10", "2001:db8::1", "198.51.100.0/24"}},
		{name: "family mismatch", entry: &apigen.IngressListen{Address: &apigen.AddressSelector{Family: apigen.AddressFamily_ADDRESS_FAMILY_IPV6, Prefixes: []string{"203.0.113.10"}}}, wantErr: "does not belong to the selected family"},
		{name: "garbage", entry: &apigen.IngressListen{Address: &apigen.AddressSelector{Prefixes: []string{"example.com"}}}, wantErr: "not an IP address or CIDR prefix"},
		{name: "duplicate", entry: &apigen.IngressListen{Address: &apigen.AddressSelector{Prefixes: []string{"203.0.113.10", "203.0.113.10/32"}}}, wantErr: "duplicate entry"},
	}
	for _, tc := range cases {
		err := validateIngressListen([]*apigen.IngressListen{tc.entry})
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
			got := tc.entry.Address.Prefixes
			if len(got) != len(tc.want) {
				t.Fatalf("%s: prefixes %v, want %v", tc.name, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("%s: prefixes %v, want %v", tc.name, got, tc.want)
				}
			}
		}
	}
	if _, err := netip.ParseAddr("203.0.113.10"); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsChangeRejectedWhenLiteralClaimBecomesReserved(t *testing.T) {
	dir := t.TempDir()
	store := state.Open(filepath.Join(dir, "primary.db"))
	defer store.Close()
	primary := store.EnsurePrimaryNode("primary", "primary-id")
	store.SetNodeHostAddresses("primary-id", []string{"203.0.113.10", "2001:db8::10"})
	h := &Handler{Store: store, NodeID: primary.ID}
	literal := &apigen.IngressListen{Address: &apigen.AddressSelector{Prefixes: []string{"203.0.113.10"}}}
	statetest.MustCreateDeploymentForNode(store, apigen.Context{}, 1, "echo", primary.ID, httpsSpec("web.example.test", literal))

	// The handler runs this under the global lock; mirror that here.
	defer store.GlobalLock()()
	settings := func(listen string) *apigen.ClusterSettings {
		return &apigen.ClusterSettings{
			HttpsWeb: apigen.HttpsWebSettings{Enabled: apigen.BoolSetting{Value: true}, Listen: apigen.StringSetting{Value: listen}},
		}
	}
	if err := h.validateIngressAgainstSettings(settings("[2001:db8::10]:443")); err != nil {
		t.Fatalf("Web UI on the other address is fine: %v", err)
	}
	if err := h.validateIngressAgainstSettings(settings(":443")); err == nil || !strings.Contains(err.Error(), `deployment "echo"`) {
		t.Fatalf("a wildcard Web UI listen must be rejected while a literal claim exists, got %v", err)
	}
}
