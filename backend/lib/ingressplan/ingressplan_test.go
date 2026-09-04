package ingressplan

import (
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

var (
	v4A = netip.MustParseAddr("203.0.113.10")
	v4B = netip.MustParseAddr("203.0.113.11")
	v6A = netip.MustParseAddr("2001:db8::10")
)

func nodes() []Node {
	return []Node{
		{ID: 1, HostAddresses: []netip.Addr{v4A, v6A}},
		{ID: 2, HostAddresses: []netip.Addr{v4B}},
	}
}

func httpsRoute(hostname, prefix string, listen ...*apigen.IngressListen) Route {
	return Route{Kind: apigen.IngressKind_INGRESS_KIND_HTTPS, Hostname: hostname, PathPrefix: prefix, CertSource: "acme", Listen: listen}
}

func passthroughRoute(hostname string, hostPort int32, listen ...*apigen.IngressListen) Route {
	return Route{Kind: apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH, Hostname: hostname, HostPort: hostPort, Listen: listen}
}

func literal(values ...string) *apigen.IngressListen {
	return &apigen.IngressListen{Address: &apigen.AddressSelector{Prefixes: values}}
}

func family(f apigen.AddressFamily) *apigen.IngressListen {
	return &apigen.IngressListen{Address: &apigen.AddressSelector{Family: f}}
}

func onNode(id int32, address *apigen.AddressSelector) *apigen.IngressListen {
	return &apigen.IngressListen{Node: &apigen.NodeSelector{NodeID: id}, Address: address}
}

func publishSet(t *testing.T, result Result, nodeID int32) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, entry := range result.Publish[nodeID] {
		addr := "*"
		if entry.Address.IsValid() {
			addr = entry.Address.String()
		}
		out[addr+":"+itoa(entry.Port)] = true
	}
	return out
}

func itoa(v int32) string {
	return strconv.Itoa(int(v))
}

func messages(diags []Diagnostic) string {
	parts := make([]string, 0, len(diags))
	for _, d := range diags {
		parts = append(parts, d.Message)
	}
	return strings.Join(parts, "\n")
}

func TestDefaultSelectorPublishesEveryAddressOfHostingNode(t *testing.T) {
	result := Evaluate(Inputs{
		Nodes:       nodes(),
		Deployments: []Deployment{{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/")}}},
	})
	if len(result.Errors) != 0 {
		t.Fatalf("errors: %s", messages(result.Errors))
	}
	got := publishSet(t, result, 1)
	for _, want := range []string{v4A.String() + ":443", v4A.String() + ":80", v6A.String() + ":443", v6A.String() + ":80"} {
		if !got[want] {
			t.Fatalf("publish set %v missing %s", got, want)
		}
	}
	if len(result.Publish[2]) != 0 {
		t.Fatalf("node 2 must publish nothing, got %v", result.Publish[2])
	}
}

func TestFamilyAndLiteralSelectors(t *testing.T) {
	result := Evaluate(Inputs{
		Nodes: nodes(),
		Deployments: []Deployment{
			{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("v4.example", "/", family(apigen.AddressFamily_ADDRESS_FAMILY_IPV4))}},
			{ID: 11, NodeID: 1, Routes: []Route{passthroughRoute("db.example", 5433, literal(v6A.String(), "198.51.100.7"))}},
			{ID: 12, NodeID: 1, Routes: []Route{passthroughRoute("cidr.example", 5434, literal("203.0.113.0/24"))}},
		},
	})
	if len(result.Errors) != 0 {
		t.Fatalf("errors: %s", messages(result.Errors))
	}
	got := publishSet(t, result, 1)
	if !got[v4A.String()+":443"] || got[v6A.String()+":443"] {
		t.Fatalf("ipv4() must publish only the IPv4 address: %v", got)
	}
	if !got[v6A.String()+":5433"] || got[v4A.String()+":5433"] || got["198.51.100.7:5433"] {
		t.Fatalf("literal selector must publish only matching inventory addresses: %v", got)
	}
	if !got[v4A.String()+":5434"] || got[v6A.String()+":5434"] {
		t.Fatalf("CIDR selector must publish the covered inventory address only: %v", got)
	}
}

func TestNodeSelectorIntersectsReachability(t *testing.T) {
	route := httpsRoute("app.example", "/", onNode(2, nil))
	result := Evaluate(Inputs{Nodes: nodes(), Deployments: []Deployment{{ID: 10, NodeID: 1, Routes: []Route{route}}}})
	if len(result.Publish[1])+len(result.Publish[2]) != 0 {
		t.Fatalf("node 2 is not reachable from node 1 yet; got %v", result.Publish)
	}
	result = Evaluate(Inputs{
		Nodes:       nodes(),
		Deployments: []Deployment{{ID: 10, NodeID: 1, Routes: []Route{route}}},
		Reachable:   func(int32) []int32 { return []int32{1, 2} },
	})
	if !publishSet(t, result, 2)[v4B.String()+":443"] || len(result.Publish[1]) != 0 {
		t.Fatalf("with cross-node reachability the route publishes on node 2 only: %v", result.Publish)
	}
}

func TestDefaultNodeSelectorStaysOnScheduledNode(t *testing.T) {
	everywhere := func(int32) []int32 { return []int32{1, 2} }
	result := Evaluate(Inputs{
		Nodes:       nodes(),
		Deployments: []Deployment{{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/")}}},
		Reachable:   everywhere,
	})
	if !publishSet(t, result, 1)[v4A.String()+":443"] || len(result.Publish[2]) != 0 {
		t.Fatalf("the default selector publishes on the scheduled node only, even with cross-node reachability: %v", result.Publish)
	}
	anyNode := &apigen.IngressListen{Node: &apigen.NodeSelector{Any: true}}
	result = Evaluate(Inputs{
		Nodes:       nodes(),
		Deployments: []Deployment{{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/", anyNode)}}},
		Reachable:   everywhere,
	})
	if !publishSet(t, result, 1)[v4A.String()+":443"] || !publishSet(t, result, 2)[v4B.String()+":443"] {
		t.Fatalf("any_node() publishes on every reachable node: %v", result.Publish)
	}
	if got := SelectorSummary(nil, nil); got != "scheduled node, any address" {
		t.Fatalf("default summary = %q", got)
	}
	if got := SelectorSummary(anyNode, nil); got != "any node, any address" {
		t.Fatalf("any summary = %q", got)
	}
}

func TestUnknownInventoryPublishesWildcardAndLiterals(t *testing.T) {
	result := Evaluate(Inputs{
		Nodes: []Node{{ID: 3}},
		Deployments: []Deployment{
			{ID: 10, NodeID: 3, Routes: []Route{httpsRoute("app.example", "/")}},
			{ID: 11, NodeID: 3, Routes: []Route{passthroughRoute("db.example", 5433, literal("198.51.100.7"))}},
			{ID: 12, NodeID: 3, Routes: []Route{passthroughRoute("cidr.example", 5434, literal("198.51.100.0/24"))}},
		},
	})
	got := publishSet(t, result, 3)
	if !got["*:443"] || !got["*:80"] {
		t.Fatalf("wildcard selector on an unknown inventory must publish the wildcard: %v", got)
	}
	if !got["198.51.100.7:5433"] {
		t.Fatalf("literal selector on an unknown inventory must publish its literal: %v", got)
	}
	if got["*:5434"] || got["198.51.100.0:5434"] {
		t.Fatalf("CIDR selector on an unknown inventory must publish nothing: %v", got)
	}
}

func TestWildcardReservationDropsWildcardClaimsAndRejectsLiterals(t *testing.T) {
	reservations := []Reservation{{NodeID: 1, Port: 443, Name: "primary Web UI"}}
	result := Evaluate(Inputs{
		Nodes:        nodes(),
		Reservations: reservations,
		Deployments:  []Deployment{{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/")}}},
	})
	if len(result.Errors) != 0 {
		t.Fatalf("wildcard claims against a reservation are dropped, not errors: %s", messages(result.Errors))
	}
	got := publishSet(t, result, 1)
	if got[v4A.String()+":443"] || got[v6A.String()+":443"] {
		t.Fatalf("443 must not be published on a node whose Web UI binds :443: %v", got)
	}
	if !got[v4A.String()+":80"] {
		t.Fatalf("port 80 is not reserved and must still publish: %v", got)
	}
	if len(result.Excluded) == 0 || !strings.Contains(messages(result.Excluded), "reserved by the primary Web UI") {
		t.Fatalf("exclusion must be reported, got %q", messages(result.Excluded))
	}

	result = Evaluate(Inputs{
		Nodes:        nodes(),
		Reservations: reservations,
		Candidate:    10,
		Deployments:  []Deployment{{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/", literal(v4A.String()))}}},
	})
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Message, "reserved by the primary Web UI") {
		t.Fatalf("a literal claim on a reserved port is an error, got %q", messages(result.Errors))
	}
}

func TestLiteralReservationOnlyBlocksItsAddress(t *testing.T) {
	reservations := []Reservation{{NodeID: 1, Address: v6A, Port: 443, Name: "primary Web UI"}}
	result := Evaluate(Inputs{
		Nodes:        nodes(),
		Reservations: reservations,
		Candidate:    10,
		Deployments:  []Deployment{{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/", family(apigen.AddressFamily_ADDRESS_FAMILY_IPV4))}}},
	})
	if len(result.Errors) != 0 {
		t.Fatalf("errors: %s", messages(result.Errors))
	}
	if got := publishSet(t, result, 1); !got[v4A.String()+":443"] || got[v6A.String()+":443"] {
		t.Fatalf("ipv4() beside an IPv6 Web UI listen publishes the IPv4 address only: %v", got)
	}
	result = Evaluate(Inputs{
		Nodes:        nodes(),
		Reservations: reservations,
		Candidate:    10,
		Deployments:  []Deployment{{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/", literal(v6A.String()))}}},
	})
	if len(result.Errors) != 1 {
		t.Fatalf("the literal equal to the Web UI address is an error, got %q", messages(result.Errors))
	}
}

func TestSameHostnameOverlapIsAnErrorForTheCandidate(t *testing.T) {
	result := Evaluate(Inputs{
		Nodes:     nodes(),
		Candidate: 11,
		Deployments: []Deployment{
			{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/")}},
			{ID: 11, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/", literal(v4A.String()))}},
		},
	})
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Message, "already claimed by another deployment") {
		t.Fatalf("overlapping claims must be rejected: %q", messages(result.Errors))
	}
	result = Evaluate(Inputs{
		Nodes:     nodes(),
		Candidate: 11,
		Deployments: []Deployment{
			{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/", literal(v4A.String()))}},
			{ID: 11, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/", literal(v6A.String()))}},
		},
	})
	if len(result.Errors) != 0 {
		t.Fatalf("disjoint address sets do not collide: %s", messages(result.Errors))
	}
	result = Evaluate(Inputs{
		Nodes:     nodes(),
		Candidate: 11,
		Deployments: []Deployment{
			{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/")}},
			{ID: 11, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/api")}},
		},
	})
	if len(result.Errors) != 0 {
		t.Fatalf("distinct prefixes compose: %s", messages(result.Errors))
	}
}

func TestCertSourceAndKindConflicts(t *testing.T) {
	secret := httpsRoute("app.example", "/other")
	secret.CertSource = "secret:7"
	result := Evaluate(Inputs{
		Nodes:     nodes(),
		Candidate: 11,
		Deployments: []Deployment{
			{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/")}},
			{ID: 11, NodeID: 1, Routes: []Route{secret}},
		},
	})
	if !strings.Contains(messages(result.Errors), "certSource for app.example must match") {
		t.Fatalf("cert source mismatch must be rejected: %q", messages(result.Errors))
	}
	result = Evaluate(Inputs{
		Nodes:       nodes(),
		Candidate:   10,
		Deployments: []Deployment{{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/"), secret}}},
	})
	if !strings.Contains(messages(result.Errors), "certSource for app.example must match") {
		t.Fatalf("cert source mismatch within one deployment must be rejected: %q", messages(result.Errors))
	}
	result = Evaluate(Inputs{
		Nodes:     nodes(),
		Candidate: 11,
		Deployments: []Deployment{
			{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/")}},
			{ID: 11, NodeID: 1, Routes: []Route{passthroughRoute("app.example", 0)}},
		},
	})
	if !strings.Contains(messages(result.Errors), "cannot use both HTTPS and TLS_PASSTHROUGH") {
		t.Fatalf("cross-kind hostname claims must be rejected: %q", messages(result.Errors))
	}
}

func TestStoredCollisionResolvesToLowerIDWithWarnings(t *testing.T) {
	result := Evaluate(Inputs{
		Nodes: nodes(),
		Deployments: []Deployment{
			{ID: 20, NodeID: 1, Name: "second", Routes: []Route{httpsRoute("app.example", "/")}},
			{ID: 10, NodeID: 1, Name: "first", Routes: []Route{httpsRoute("app.example", "/")}},
		},
	})
	if len(result.Errors) != 0 {
		t.Fatalf("stored collisions are warnings: %s", messages(result.Errors))
	}
	if len(result.Warnings) != 4 {
		t.Fatalf("both deployments are warned per address, got %d: %s", len(result.Warnings), messages(result.Warnings))
	}
	seen := map[int32]bool{}
	for _, w := range result.Warnings {
		seen[w.DeploymentID] = true
	}
	if !seen[10] || !seen[20] {
		t.Fatalf("warnings must name both deployments: %+v", result.Warnings)
	}
	if got := publishSet(t, result, 1); !got[v4A.String()+":443"] {
		t.Fatalf("the winning route still publishes: %v", got)
	}
}

func TestTCPPortForwardConflictsWithIngress(t *testing.T) {
	result := Evaluate(Inputs{
		Nodes:     nodes(),
		Candidate: 11,
		Deployments: []Deployment{
			{ID: 10, NodeID: 1, TCPPorts: []int32{5433}},
			{ID: 11, NodeID: 1, Routes: []Route{passthroughRoute("db.example", 5433)}},
		},
	})
	if !strings.Contains(messages(result.Errors), "TCP host port 5433 conflicts with ingress") {
		t.Fatalf("port forward and ingress on one port must be rejected: %q", messages(result.Errors))
	}
}

func TestHostModeWarning(t *testing.T) {
	result := Evaluate(Inputs{
		Nodes: nodes(),
		Deployments: []Deployment{
			{ID: 5, NodeID: 1, Name: "legacy", HostMode: true},
			{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/")}},
			{ID: 11, NodeID: 1, Routes: []Route{httpsRoute("lit.example", "/", literal(v4A.String()))}},
		},
	})
	if len(result.Warnings) != 1 || result.Warnings[0].DeploymentID != 10 || !strings.Contains(result.Warnings[0].Message, "legacy") {
		t.Fatalf("only the wildcard route on a node with host-mode deployments is warned: %+v", result.Warnings)
	}
}

func TestWebUIReservations(t *testing.T) {
	got := WebUIReservations(7, true, ":443", true, "[2001:db8::1]:80")
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Address.IsValid() || got[0].Port != 443 || got[0].NodeID != 7 {
		t.Fatalf("wildcard listen must reserve every address: %+v", got[0])
	}
	if got[1].Address != netip.MustParseAddr("2001:db8::1") || got[1].Port != 80 {
		t.Fatalf("literal listen must reserve its address: %+v", got[1])
	}
	if got := WebUIReservations(7, false, ":443", false, ":80"); len(got) != 0 {
		t.Fatalf("disabled servers reserve nothing: %+v", got)
	}
	if got := WebUIReservations(7, true, "0.0.0.0:8443", false, ""); len(got) != 1 || got[0].Address.IsValid() {
		t.Fatalf("an unspecified host is a wildcard: %+v", got)
	}
}
