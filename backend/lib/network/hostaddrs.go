package network

import (
	"net/netip"
	"strings"
	"time"
)

// HostAddressPollInterval is how often agents re-check their host address set.
const HostAddressPollInterval = 30 * time.Second

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
