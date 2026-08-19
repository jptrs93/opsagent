package netaudit

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"

	"github.com/jptrs93/opsagent/backend/lib/network"
)

// KernelState is what the kernel actually holds, reduced to the shapes the
// manager installs.
type KernelState struct {
	TableV4, TableV6 bool
	Masquerade       bool
	// DNAT holds one key per DNAT rule found in the opendeploy tables.
	DNAT map[string]int
	// Unrecognized counts rules in the opendeploy tables that are neither the
	// masquerade rule nor a DNAT rule of the manager's shape.
	Unrecognized []string
	// Routes maps each protocol-200 /128 route to its output link index.
	Routes map[netip.Addr]int
	// FallbackRoute reports the cluster-prefix unreachable route.
	FallbackRoute bool
}

// Diff is the reportable difference between desired and kernel state.
type Diff struct {
	NotInstalled bool // nothing desired and no opendeploy tables: agent has not touched the kernel yet

	MissingNft           []string
	UnexpectedNft        []string
	UnrecognizedNft      []string
	MissingMasquerade    bool
	MissingRoutes        []string
	WrongLinkRoutes      []string
	UnexpectedRoutes     []string
	MissingFallbackRoute bool
}

func (d Diff) InSync() bool {
	return !d.MissingMasquerade && !d.MissingFallbackRoute &&
		len(d.MissingNft) == 0 && len(d.UnexpectedNft) == 0 && len(d.UnrecognizedNft) == 0 &&
		len(d.MissingRoutes) == 0 && len(d.WrongLinkRoutes) == 0 && len(d.UnexpectedRoutes) == 0
}

// Compare reduces desired and kernel state to a reportable diff.
func Compare(desired network.AuditState, kernel KernelState) Diff {
	var d Diff
	if !kernel.TableV4 && !kernel.TableV6 && len(desired.HostPortRules) == 0 && len(desired.WorkloadRoutes) == 0 {
		d.NotInstalled = true
		return d
	}

	expected := expectedDNATKeys(desired.HostPortRules)
	actual := make(map[string]int, len(kernel.DNAT))
	for key, count := range kernel.DNAT {
		actual[key] = count
	}
	for key := range expected {
		if actual[key] > 0 {
			actual[key]--
			continue
		}
		d.MissingNft = append(d.MissingNft, key)
	}
	for key, count := range actual {
		for range count {
			d.UnexpectedNft = append(d.UnexpectedNft, key)
		}
	}
	d.UnrecognizedNft = append(d.UnrecognizedNft, kernel.Unrecognized...)
	d.MissingMasquerade = !kernel.Masquerade

	desiredRoutes := make(map[netip.Addr]int, len(desired.WorkloadRoutes))
	for _, route := range desired.WorkloadRoutes {
		desiredRoutes[route.Addr] = route.LinkIndex
	}
	for addr, linkIndex := range desiredRoutes {
		actualLink, ok := kernel.Routes[addr]
		switch {
		case !ok:
			d.MissingRoutes = append(d.MissingRoutes, fmt.Sprintf("%s link=%d", addr, linkIndex))
		case actualLink != linkIndex:
			d.WrongLinkRoutes = append(d.WrongLinkRoutes, fmt.Sprintf("%s link=%d want=%d", addr, actualLink, linkIndex))
		}
	}
	for addr, linkIndex := range kernel.Routes {
		if _, ok := desiredRoutes[addr]; !ok {
			d.UnexpectedRoutes = append(d.UnexpectedRoutes, fmt.Sprintf("%s link=%d", addr, linkIndex))
		}
	}
	// The fallback route is installed alongside the first container network, so
	// only expect it once workload routes are desired.
	d.MissingFallbackRoute = desired.HasPrefix && len(desired.WorkloadRoutes) > 0 && !kernel.FallbackRoute

	for _, list := range [][]string{d.MissingNft, d.UnexpectedNft, d.UnrecognizedNft, d.MissingRoutes, d.WrongLinkRoutes, d.UnexpectedRoutes} {
		sort.Strings(list)
	}
	return d
}

// expectedDNATKeys mirrors reconcileNft: each published host port renders a
// prerouting and an output rule per address family it has a target for.
func expectedDNATKeys(rules []network.HostPortRule) map[string]struct{} {
	keys := make(map[string]struct{})
	for _, rule := range rules {
		for _, chain := range []string{"prerouting", "output"} {
			if rule.TargetV4.Is4() {
				keys[dnatKey("ip", chain, rule.Protocol, rule.HostPort, rule.TargetV4, rule.TargetPort)] = struct{}{}
			}
			if rule.TargetV6.Is6() {
				keys[dnatKey("ip6", chain, rule.Protocol, rule.HostPort, rule.TargetV6, rule.TargetPort)] = struct{}{}
			}
		}
	}
	return keys
}

func dnatKey(family, chain string, proto uint8, hostPort uint16, target netip.Addr, targetPort uint16) string {
	return fmt.Sprintf("%s %s %s dport %d -> %s", family, chain, protoName(proto), hostPort,
		netip.AddrPortFrom(target, targetPort))
}

func protoName(proto uint8) string {
	switch proto {
	case 6:
		return "tcp"
	case 17:
		return "udp"
	}
	return strconv.Itoa(int(proto))
}
