package netaudit

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/jptrs93/opsagent/backend/lib/network"
)

// KernelState is what the kernel actually holds, reduced to the shapes the
// manager installs.
type KernelState struct {
	TableV4, TableV6 bool
	Masquerade       bool
	// DNAT holds one key per DNAT rule found in the opendeploy tables.
	DNAT map[string]int
	// Filter holds one key per filter rule found in the opendeploy tables'
	// forward and workload chains.
	Filter map[string]int
	// Elements holds one key per set/map element found in the opendeploy
	// tables' named sets.
	Elements map[string]int
	// Unrecognized counts rules in the opendeploy tables that are neither the
	// masquerade rule nor a DNAT rule of the manager's shape.
	Unrecognized []string
	// Routes maps each protocol-200 /128 route to its output link index.
	Routes map[netip.Addr]int
	// FallbackRoute reports the cluster-prefix unreachable route.
	FallbackRoute bool
	// WG is the managed WireGuard device's live configuration, nil when the
	// device does not exist.
	WG *KernelWGState
}

// KernelWGState reduces the live WireGuard device to the shape the manager
// installs. Handshake and traffic counters are deliberately absent — sessions
// are traffic-driven and an idle peer legitimately has none.
type KernelWGState struct {
	PublicKey  string
	ListenPort uint16
	Peers      map[string]KernelWGPeer // keyed by peer public key
}

type KernelWGPeer struct {
	Endpoint   string
	AllowedIPs string // canonical sorted comma-joined prefixes
}

// Diff is the reportable difference between desired and kernel state.
type Diff struct {
	NotInstalled bool // nothing desired and no opendeploy tables: agent has not touched the kernel yet

	MissingNft           []string
	UnexpectedNft        []string
	UnrecognizedNft      []string
	MissingFilter        []string
	UnexpectedFilter     []string
	MissingElements      []string
	UnexpectedElements   []string
	MissingMasquerade    bool
	MissingRoutes        []string
	WrongLinkRoutes      []string
	UnexpectedRoutes     []string
	MissingFallbackRoute bool

	MissingWGDevice    bool
	UnexpectedWGDevice bool
	// WGDeviceDrift covers local key and listen port mismatches.
	WGDeviceDrift []string
	// MissingWGPeers/UnexpectedWGPeers report peer set membership by public
	// key; WGPeerDrift reports endpoint or allowed-ips divergence on peers
	// present on both sides. Allowed-ips drift is the one that weakens the
	// node-level source check, so it is reported distinctly.
	MissingWGPeers    []string
	UnexpectedWGPeers []string
	WGPeerDrift       []string
}

func (d Diff) InSync() bool {
	return !d.MissingMasquerade && !d.MissingFallbackRoute &&
		len(d.MissingNft) == 0 && len(d.UnexpectedNft) == 0 && len(d.UnrecognizedNft) == 0 &&
		len(d.MissingFilter) == 0 && len(d.UnexpectedFilter) == 0 &&
		len(d.MissingElements) == 0 && len(d.UnexpectedElements) == 0 &&
		len(d.MissingRoutes) == 0 && len(d.WrongLinkRoutes) == 0 && len(d.UnexpectedRoutes) == 0 &&
		!d.MissingWGDevice && !d.UnexpectedWGDevice && len(d.WGDeviceDrift) == 0 &&
		len(d.MissingWGPeers) == 0 && len(d.UnexpectedWGPeers) == 0 && len(d.WGPeerDrift) == 0
}

// Compare reduces desired and kernel state to a reportable diff.
func Compare(desired network.AuditState, kernel KernelState) Diff {
	var d Diff
	if !kernel.TableV4 && !kernel.TableV6 && kernel.WG == nil &&
		len(desired.HostPortRules) == 0 && len(desired.WorkloadRoutes) == 0 && len(desired.FilterNets) == 0 && desired.WG == nil {
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

	filterState := desired.FilterState()
	d.MissingFilter, d.UnexpectedFilter = diffCounts(filterState.RuleKeys(), kernel.Filter)
	d.MissingElements, d.UnexpectedElements = diffCounts(filterState.ElementKeys(), kernel.Elements)

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

	compareWG(&d, desired.WG, kernel.WG)

	for _, list := range [][]string{d.MissingNft, d.UnexpectedNft, d.UnrecognizedNft, d.MissingFilter, d.UnexpectedFilter, d.MissingElements, d.UnexpectedElements, d.MissingRoutes, d.WrongLinkRoutes, d.UnexpectedRoutes, d.WGDeviceDrift, d.MissingWGPeers, d.UnexpectedWGPeers, d.WGPeerDrift} {
		sort.Strings(list)
	}
	return d
}

func compareWG(d *Diff, desired *network.WGAuditState, kernel *KernelWGState) {
	if desired == nil {
		d.UnexpectedWGDevice = kernel != nil
		return
	}
	if kernel == nil {
		d.MissingWGDevice = true
		return
	}
	if kernel.PublicKey != desired.PublicKey {
		d.WGDeviceDrift = append(d.WGDeviceDrift, fmt.Sprintf("public key %s want %s", kernel.PublicKey, desired.PublicKey))
	}
	if kernel.ListenPort != desired.ListenPort {
		d.WGDeviceDrift = append(d.WGDeviceDrift, fmt.Sprintf("listen port %d want %d", kernel.ListenPort, desired.ListenPort))
	}
	seen := make(map[string]struct{}, len(desired.Peers))
	for _, peer := range desired.Peers {
		seen[peer.PublicKey] = struct{}{}
		live, ok := kernel.Peers[peer.PublicKey]
		if !ok {
			d.MissingWGPeers = append(d.MissingWGPeers, fmt.Sprintf("node %d %s", peer.NodeID, peer.PublicKey))
			continue
		}
		if live.Endpoint != peer.Endpoint.String() {
			d.WGPeerDrift = append(d.WGPeerDrift, fmt.Sprintf("node %d endpoint %s want %s", peer.NodeID, live.Endpoint, peer.Endpoint))
		}
		if want := joinPrefixes(peer.AllowedIPs); live.AllowedIPs != want {
			d.WGPeerDrift = append(d.WGPeerDrift, fmt.Sprintf("node %d allowed-ips [%s] want [%s]", peer.NodeID, live.AllowedIPs, want))
		}
	}
	for key := range kernel.Peers {
		if _, ok := seen[key]; !ok {
			d.UnexpectedWGPeers = append(d.UnexpectedWGPeers, key)
		}
	}
}

func joinPrefixes(prefixes []netip.Prefix) string {
	parts := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		parts = append(parts, prefix.String())
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func diffCounts(expected, actual map[string]int) (missing, unexpected []string) {
	remaining := make(map[string]int, len(actual))
	for key, count := range actual {
		remaining[key] = count
	}
	for key, count := range expected {
		for range count {
			if remaining[key] > 0 {
				remaining[key]--
				continue
			}
			missing = append(missing, key)
		}
	}
	for key, count := range remaining {
		for range count {
			unexpected = append(unexpected, key)
		}
	}
	return missing, unexpected
}

// expectedDNATKeys mirrors reconcileNft: each published host port renders an
// unconditional output rule per address family it has a target for, plus
// prerouting rules — unconditional when unfiltered, one per allowed source
// prefix of the family when filtered.
func expectedDNATKeys(rules []network.HostPortRule) map[string]struct{} {
	keys := make(map[string]struct{})
	for _, rule := range rules {
		if rule.TargetV4.Is4() {
			addExpectedDNATKeys(keys, "ip", rule, rule.TargetV4, rule.AllowV4)
		}
		if rule.TargetV6.Is6() {
			addExpectedDNATKeys(keys, "ip6", rule, rule.TargetV6, rule.AllowV6)
		}
	}
	return keys
}

func addExpectedDNATKeys(keys map[string]struct{}, family string, rule network.HostPortRule, target netip.Addr, allow []netip.Prefix) {
	if rule.Filtered {
		for _, src := range allow {
			keys[dnatKey(family, "prerouting", rule.Protocol, rule.HostPort, src, target, rule.TargetPort)] = struct{}{}
		}
	} else {
		keys[dnatKey(family, "prerouting", rule.Protocol, rule.HostPort, netip.Prefix{}, target, rule.TargetPort)] = struct{}{}
	}
	keys[dnatKey(family, "output", rule.Protocol, rule.HostPort, netip.Prefix{}, target, rule.TargetPort)] = struct{}{}
}

func dnatKey(family, chain string, proto uint8, hostPort uint16, source netip.Prefix, target netip.Addr, targetPort uint16) string {
	if source.IsValid() {
		return fmt.Sprintf("%s %s %s saddr %s dport %d -> %s", family, chain, protoName(proto), source, hostPort,
			netip.AddrPortFrom(target, targetPort))
	}
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
