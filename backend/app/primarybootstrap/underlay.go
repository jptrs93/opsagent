package primarybootstrap

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// ResolvePrimaryUnderlayAddress picks the primary's underlay address: the
// explicit one when given, else the cluster listen host when it is a concrete
// address, else the first global unicast address on the host.
func ResolvePrimaryUnderlayAddress(explicit, clusterListen string) (netip.Addr, error) {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		addr, err := netip.ParseAddr(explicit)
		if err != nil {
			return netip.Addr{}, fmt.Errorf("parsing primary underlay address %q: %w", explicit, err)
		}
		return addr.Unmap(), nil
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(clusterListen))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("resolving primary underlay address from cluster listen address: %w", err)
	}
	host = strings.Trim(host, "[]")
	if host != "" {
		ipAddr, resolveErr := net.ResolveIPAddr("ip", host)
		if resolveErr == nil && ipAddr.IP != nil && !ipAddr.IP.IsUnspecified() {
			if addr, ok := netip.AddrFromSlice(ipAddr.IP); ok {
				return addr.Unmap(), nil
			}
		}
	}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return netip.Addr{}, fmt.Errorf("listing addresses for primary underlay: %w", err)
	}
	for _, addr := range addrs {
		ip, _, err := net.ParseCIDR(addr.String())
		if err == nil && ip.IsGlobalUnicast() {
			if parsed, ok := netip.AddrFromSlice(ip); ok {
				return parsed.Unmap(), nil
			}
		}
	}
	return netip.Addr{}, fmt.Errorf("no non-loopback address found for primary underlay")
}
