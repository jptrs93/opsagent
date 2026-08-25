package netaudit

import (
	"net/netip"
	"testing"

	"github.com/jptrs93/opsagent/backend/lib/network"
)

const protoTCP = 6

var (
	targetV4 = netip.MustParseAddr("10.100.0.2")
	targetV6 = netip.MustParseAddr("fd5a:1::2")
)

func hostPortRule(hostPort uint16) network.HostPortRule {
	return network.HostPortRule{
		Protocol:   protoTCP,
		HostPort:   hostPort,
		TargetPort: hostPort,
		TargetV4:   targetV4,
		TargetV6:   targetV6,
	}
}

func kernelWithRules(rules ...network.HostPortRule) KernelState {
	kernel := KernelState{
		TableV4:    true,
		TableV6:    true,
		Masquerade: true,
		DNAT:       map[string]int{},
		Routes:     map[netip.Addr]int{},
	}
	for key := range expectedDNATKeys(rules) {
		kernel.DNAT[key]++
	}
	return kernel
}

func TestCompareNotInstalled(t *testing.T) {
	diff := Compare(network.AuditState{}, KernelState{})
	if !diff.NotInstalled {
		t.Fatalf("expected NotInstalled, got %+v", diff)
	}
}

func TestCompareInSync(t *testing.T) {
	rule := hostPortRule(443)
	desired := network.AuditState{
		HostPortRules:  []network.HostPortRule{rule},
		WorkloadRoutes: []network.AuditRoute{{Addr: targetV6, LinkIndex: 7}},
	}
	kernel := kernelWithRules(rule)
	kernel.Routes[targetV6] = 7
	if diff := Compare(desired, kernel); !diff.InSync() {
		t.Fatalf("expected in sync, got %+v", diff)
	}
}

func TestExpectedDNATKeysFiltered(t *testing.T) {
	rule := hostPortRule(443)
	rule.Filtered = true
	rule.AllowV4 = []netip.Prefix{
		netip.MustParsePrefix("203.0.113.7/32"),
		netip.MustParsePrefix("198.51.100.0/24"),
	}
	keys := expectedDNATKeys([]network.HostPortRule{rule})
	want := map[string]struct{}{
		"ip prerouting tcp saddr 203.0.113.7/32 dport 443 -> 10.100.0.2:443":  {},
		"ip prerouting tcp saddr 198.51.100.0/24 dport 443 -> 10.100.0.2:443": {},
		"ip output tcp dport 443 -> 10.100.0.2:443":                           {},
		"ip6 output tcp dport 443 -> [fd5a:1::2]:443":                         {},
	}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	for key := range want {
		if _, ok := keys[key]; !ok {
			t.Fatalf("missing key %q in %v", key, keys)
		}
	}
}

func TestCompareInSyncFiltered(t *testing.T) {
	rule := hostPortRule(443)
	rule.Filtered = true
	rule.AllowV4 = []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}
	rule.AllowV6 = []netip.Prefix{netip.MustParsePrefix("2001:db8::/32")}
	desired := network.AuditState{HostPortRules: []network.HostPortRule{rule}}
	if diff := Compare(desired, kernelWithRules(rule)); !diff.InSync() {
		t.Fatalf("expected in sync, got %+v", diff)
	}
	unfilteredKernel := kernelWithRules(hostPortRule(443))
	diff := Compare(desired, unfilteredKernel)
	if len(diff.MissingNft) != 2 || len(diff.UnexpectedNft) != 2 {
		t.Fatalf("expected filtered prerouting rules to diverge from unfiltered kernel, got %+v", diff)
	}
}

func TestCompareMissingAndUnexpectedNft(t *testing.T) {
	desired := network.AuditState{HostPortRules: []network.HostPortRule{hostPortRule(443)}}
	kernel := kernelWithRules(hostPortRule(8443))
	diff := Compare(desired, kernel)
	if len(diff.MissingNft) != 4 || len(diff.UnexpectedNft) != 4 {
		t.Fatalf("expected 4 missing and 4 unexpected rules, got %+v", diff)
	}
}

func TestCompareDuplicateRuleIsUnexpected(t *testing.T) {
	rule := hostPortRule(443)
	desired := network.AuditState{HostPortRules: []network.HostPortRule{rule}}
	kernel := kernelWithRules(rule)
	for key := range kernel.DNAT {
		kernel.DNAT[key]++
	}
	diff := Compare(desired, kernel)
	if len(diff.MissingNft) != 0 || len(diff.UnexpectedNft) != 4 {
		t.Fatalf("expected 4 duplicate rules flagged unexpected, got %+v", diff)
	}
}

func TestCompareRoutes(t *testing.T) {
	missing := netip.MustParseAddr("fd5a:1::10")
	wrongLink := netip.MustParseAddr("fd5a:1::11")
	unexpected := netip.MustParseAddr("fd5a:1::12")
	desired := network.AuditState{WorkloadRoutes: []network.AuditRoute{
		{Addr: missing, LinkIndex: 3},
		{Addr: wrongLink, LinkIndex: 4},
	}}
	kernel := kernelWithRules()
	kernel.Routes[wrongLink] = 9
	kernel.Routes[unexpected] = 5
	diff := Compare(desired, kernel)
	if len(diff.MissingRoutes) != 1 || len(diff.WrongLinkRoutes) != 1 || len(diff.UnexpectedRoutes) != 1 {
		t.Fatalf("expected one route in each divergence class, got %+v", diff)
	}
}

func TestCompareMissingMasquerade(t *testing.T) {
	kernel := kernelWithRules()
	kernel.Masquerade = false
	if diff := Compare(network.AuditState{}, kernel); !diff.MissingMasquerade {
		t.Fatalf("expected missing masquerade, got %+v", diff)
	}
}

func TestCompareFallbackRouteOnlyExpectedWithWorkloads(t *testing.T) {
	kernel := kernelWithRules()
	noWorkloads := network.AuditState{HasPrefix: true}
	if diff := Compare(noWorkloads, kernel); diff.MissingFallbackRoute {
		t.Fatalf("fallback route must not be expected before any workload exists: %+v", diff)
	}
	withWorkload := network.AuditState{
		HasPrefix:      true,
		WorkloadRoutes: []network.AuditRoute{{Addr: targetV6, LinkIndex: 2}},
	}
	kernel.Routes[targetV6] = 2
	if diff := Compare(withWorkload, kernel); !diff.MissingFallbackRoute {
		t.Fatalf("expected missing fallback route, got %+v", diff)
	}
	kernel.FallbackRoute = true
	if diff := Compare(withWorkload, kernel); !diff.InSync() {
		t.Fatalf("expected in sync with fallback route present, got %+v", diff)
	}
}
