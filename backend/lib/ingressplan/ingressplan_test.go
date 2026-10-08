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

var (
	anyIPv4 = apigen.PrefixOf(netip.MustParsePrefix("0.0.0.0/0"))
	anyIPv6 = apigen.PrefixOf(netip.MustParsePrefix("::/0"))
)

func httpsRoute(hostname, prefix string, listen ...apigen.IngressListen) Route {
	return Route{Kind: apigen.IngressKind_INGRESS_KIND_HTTPS, Hostname: hostname, PathPrefix: prefix, CertSource: "acme", Listen: listen}
}

func passthroughRoute(hostname string, hostPort uint32, listen ...apigen.IngressListen) Route {
	return Route{Kind: apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH, Hostname: hostname, HostPort: hostPort, Listen: listen}
}

func prefixes(values ...string) []apigen.IpPrefix {
	out := make([]apigen.IpPrefix, 0, len(values))
	for _, value := range values {
		prefix, err := apigen.ParsePrefix(value)
		if err != nil {
			panic(err)
		}
		out = append(out, prefix)
	}
	return out
}

func literal(values ...string) apigen.IngressListen {
	return apigen.IngressListen{Addresses: prefixes(values...)}
}

func family(f apigen.IpPrefix) apigen.IngressListen {
	return apigen.IngressListen{Addresses: []apigen.IpPrefix{f}}
}

func onNode(id uint64, addresses ...apigen.IpPrefix) apigen.IngressListen {
	node := apigen.IngressNode{Value: apigen.IngressNodeValueOneof{Specific: &apigen.SpecificNode{NodeID: id}}}
	return apigen.IngressListen{Node: apigen.Some(node), Addresses: addresses}
}

func publishSet(t *testing.T, result Result, nodeID uint64) map[string]bool {
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

func itoa(v uint32) string {
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
			{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("v4.example", "/", family(anyIPv4))}},
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
	route := httpsRoute("app.example", "/", onNode(2))
	result := Evaluate(Inputs{Nodes: nodes(), Deployments: []Deployment{{ID: 10, NodeID: 1, Routes: []Route{route}}}})
	if len(result.Publish[1])+len(result.Publish[2]) != 0 {
		t.Fatalf("node 2 is not reachable from node 1 yet; got %v", result.Publish)
	}
	result = Evaluate(Inputs{
		Nodes:       nodes(),
		Deployments: []Deployment{{ID: 10, NodeID: 1, Routes: []Route{route}}},
		Reachable:   func(uint64) []uint64 { return []uint64{1, 2} },
	})
	if !publishSet(t, result, 2)[v4B.String()+":443"] || len(result.Publish[1]) != 0 {
		t.Fatalf("with cross-node reachability the route publishes on node 2 only: %v", result.Publish)
	}
}

func TestDefaultNodeSelectorStaysOnScheduledNode(t *testing.T) {
	everywhere := func(uint64) []uint64 { return []uint64{1, 2} }
	result := Evaluate(Inputs{
		Nodes:       nodes(),
		Deployments: []Deployment{{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/")}}},
		Reachable:   everywhere,
	})
	if !publishSet(t, result, 1)[v4A.String()+":443"] || len(result.Publish[2]) != 0 {
		t.Fatalf("the default selector publishes on the scheduled node only, even with cross-node reachability: %v", result.Publish)
	}
	anyNode := apigen.IngressListen{Node: apigen.Some(apigen.IngressNode{Value: apigen.IngressNodeValueOneof{Any: &apigen.AnyNode{}}})}
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
	if got := SelectorSummary(&anyNode, nil); got != "any node, any address" {
		t.Fatalf("any summary = %q", got)
	}
	named := onNode(2, append([]apigen.IpPrefix{anyIPv6}, prefixes(v4A.String(), "203.0.113.0/24")...)...)
	if got := SelectorSummary(&named, func(id uint64) string { return "n" + itoa(uint32(id)) }); got != "node n2, any IPv6 address, 203.0.113.10, 203.0.113.0/24" {
		t.Fatalf("named summary = %q", got)
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

func TestReservationIntersectsEveryOverlappingSelector(t *testing.T) {
	reservations := []Reservation{{NodeID: 1, Port: 443, Name: "primary Web UI"}}
	for _, listen := range [][]apigen.IngressListen{nil, {family(anyIPv4)}, {literal(v4A.String())}, {literal("203.0.113.0/24")}, {literal("10.0.0.0/8")}} {
		result := Evaluate(Inputs{
			Nodes:        nodes(),
			Reservations: reservations,
			Candidate:    10,
			Deployments:  []Deployment{{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/", listen...)}}},
		})
		if len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Message, "reserved by the primary Web UI") {
			t.Fatalf("listen %v: any candidate selector intersecting a wildcard reservation is an error, got %q", listen, messages(result.Errors))
		}
		if got := publishSet(t, result, 1); got[v4A.String()+":443"] || got[v6A.String()+":443"] {
			t.Fatalf("listen %v: 443 must not be published beside the reservation: %v", listen, got)
		}
	}

	result := Evaluate(Inputs{
		Nodes:        nodes(),
		Reservations: reservations,
		Deployments:  []Deployment{{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/")}}},
	})
	if len(result.Errors) != 0 {
		t.Fatalf("a stored deployment overlapping a reservation is not an error: %s", messages(result.Errors))
	}
	if len(result.Excluded) != 1 || !strings.Contains(result.Excluded[0].Message, "reserved by the primary Web UI") {
		t.Fatalf("the stored overlap is reported as excluded, got %q", messages(result.Excluded))
	}
	got := publishSet(t, result, 1)
	if got[v4A.String()+":443"] || got[v6A.String()+":443"] || !got[v4A.String()+":80"] {
		t.Fatalf("443 is kept out of the publish set and 80 still publishes: %v", got)
	}

	result = Evaluate(Inputs{
		Nodes:       nodes(),
		Candidate:   10,
		Deployments: []Deployment{{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/")}}},
	})
	if len(result.Errors)+len(result.Excluded)+len(result.Warnings) != 0 {
		t.Fatalf("without reservations the same inputs are clean: %s", messages(append(result.Errors, append(result.Excluded, result.Warnings...)...)))
	}
	if got := publishSet(t, result, 1); !got[v4A.String()+":443"] || !got[v6A.String()+":443"] || !got[v4A.String()+":80"] {
		t.Fatalf("without reservations every address publishes: %v", got)
	}
}

func TestSelectorSetsCollideRegardlessOfInventory(t *testing.T) {
	evaluate := func(first, second []apigen.IngressListen) Result {
		return Evaluate(Inputs{
			Nodes:     nodes(),
			Candidate: 11,
			Deployments: []Deployment{
				{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/", first...)}},
				{ID: 11, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/", second...)}},
			},
		})
	}
	collide := func(name string, first, second []apigen.IngressListen) {
		t.Helper()
		result := evaluate(first, second)
		if len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Message, "already claimed by another deployment") {
			t.Fatalf("%s must collide, got %q", name, messages(result.Errors))
		}
	}
	disjoint := func(name string, first, second []apigen.IngressListen) {
		t.Helper()
		result := evaluate(first, second)
		if len(result.Errors) != 0 {
			t.Fatalf("%s must not collide: %s", name, messages(result.Errors))
		}
	}
	collide("whole space against a prefix no inventory address matches", nil, []apigen.IngressListen{literal("10.0.0.0/8")})
	collide("prefix against the whole space", []apigen.IngressListen{literal("10.0.0.0/8")}, nil)
	collide("containing prefixes", []apigen.IngressListen{literal("10.0.0.0/8")}, []apigen.IngressListen{literal("10.1.0.0/16")})
	collide("literal inside a prefix", []apigen.IngressListen{literal("10.0.0.0/8")}, []apigen.IngressListen{literal("10.1.2.3")})
	collide("family against a prefix of that family", []apigen.IngressListen{family(anyIPv4)}, []apigen.IngressListen{literal("10.0.0.0/8")})
	collide("family against the whole space", []apigen.IngressListen{family(anyIPv6)}, nil)
	disjoint("disjoint prefixes", []apigen.IngressListen{literal("10.0.0.0/8")}, []apigen.IngressListen{literal("192.168.0.0/16")})
	disjoint("different families", []apigen.IngressListen{family(anyIPv4)}, []apigen.IngressListen{family(anyIPv6)})
	disjoint("literal outside a prefix", []apigen.IngressListen{literal("10.0.0.0/8")}, []apigen.IngressListen{literal("192.0.2.1")})
	disjoint("IPv6 prefix against an IPv4 literal", []apigen.IngressListen{literal("2001:db8::/32")}, []apigen.IngressListen{literal(v4A.String())})
}

func TestLiteralReservationOnlyBlocksItsAddress(t *testing.T) {
	reservations := []Reservation{{NodeID: 1, Address: v6A, Port: 443, Name: "primary Web UI"}}
	result := Evaluate(Inputs{
		Nodes:        nodes(),
		Reservations: reservations,
		Candidate:    10,
		Deployments:  []Deployment{{ID: 10, NodeID: 1, Routes: []Route{httpsRoute("app.example", "/", family(anyIPv4))}}},
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

func TestTCPPortForwardConflictsWithIngress(t *testing.T) {
	result := Evaluate(Inputs{
		Nodes:     nodes(),
		Candidate: 11,
		Deployments: []Deployment{
			{ID: 10, NodeID: 1, TCPPorts: []uint32{5433}},
			{ID: 11, NodeID: 1, Routes: []Route{passthroughRoute("db.example", 5433)}},
		},
	})
	if !strings.Contains(messages(result.Errors), "TCP host port 5433 conflicts with ingress") {
		t.Fatalf("port forward and ingress on one port must be rejected: %q", messages(result.Errors))
	}
	result = Evaluate(Inputs{
		Nodes:     nodes(),
		Candidate: 11,
		Deployments: []Deployment{
			{ID: 10, NodeID: 1, TCPPorts: []uint32{5433}},
			{ID: 11, NodeID: 1, Routes: []Route{passthroughRoute("db.example", 5433, literal(v6A.String()))}},
		},
	})
	if !strings.Contains(messages(result.Errors), "TCP host port 5433 conflicts with ingress") {
		t.Fatalf("a port forward claims the port on every address of its node: %q", messages(result.Errors))
	}
	result = Evaluate(Inputs{
		Nodes:     nodes(),
		Candidate: 11,
		Deployments: []Deployment{
			{ID: 10, NodeID: 2, TCPPorts: []uint32{5433}},
			{ID: 11, NodeID: 1, Routes: []Route{passthroughRoute("db.example", 5433)}},
		},
	})
	if len(result.Errors) != 0 {
		t.Fatalf("a port forward on another node does not conflict: %q", messages(result.Errors))
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
