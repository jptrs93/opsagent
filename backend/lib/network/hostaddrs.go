package network

import (
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// HostAddressPollInterval is how often agents re-check their host address set.
const HostAddressPollInterval = 30 * time.Second

// EnumerateHostAddresses lists the machine's global unicast addresses on
// interfaces the agent does not create: the candidates an ingress listen
// selector can expand to. Loopback, link-local, the WireGuard underlay, the
// workload veths, the cluster ULA prefix, and the IPv4 egress range are all
// agent-owned or unreachable from outside and are excluded. The result is
// canonical and sorted so equal sets compare equal.
func EnumerateHostAddresses(prefix Prefix, hasPrefix bool) ([]netip.Addr, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 || isManagedInterface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, raw := range addrs {
			ipNet, ok := raw.(*net.IPNet)
			if !ok {
				continue
			}
			addr, ok := netip.AddrFromSlice(ipNet.IP)
			if !ok {
				continue
			}
			addr = addr.Unmap()
			if !eligibleHostAddress(addr, prefix, hasPrefix) {
				continue
			}
			out = append(out, addr)
		}
	}
	slices.SortFunc(out, netip.Addr.Compare)
	return slices.Compact(out), nil
}

func isManagedInterface(name string) bool {
	if name == WGLinkName {
		return true
	}
	// Workload veths are named od<deployment>s<slot>; see hostVethName.
	rest, ok := strings.CutPrefix(name, "od")
	return ok && len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9'
}

func eligibleHostAddress(addr netip.Addr, prefix Prefix, hasPrefix bool) bool {
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsLinkLocalUnicast() || addr.Zone() != "" {
		return false
	}
	if addr.Is4() && V4CIDR.Contains(addr) {
		return false
	}
	if hasPrefix && addr.Is6() && prefix.CIDR().Contains(addr) {
		return false
	}
	return true
}

// HostAddressStrings renders an address list for the wire.
func HostAddressStrings(addrs []netip.Addr) []string {
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		out = append(out, addr.String())
	}
	return out
}
