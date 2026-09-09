package network

import (
	"net"
	"net/netip"
	"slices"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// EnumerateHostAddresses excludes unstable IPv6 addresses before they become
// ingress targets or versioned node facts. IPv4 SECONDARY shares the TEMPORARY
// bit, so that flag is only tested for IPv6.
func EnumerateHostAddresses(prefix Prefix, hasPrefix bool) ([]netip.Addr, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, link := range links {
		attrs := link.Attrs()
		if attrs.Flags&net.FlagLoopback != 0 || isManagedInterface(attrs.Name) {
			continue
		}
		addresses, err := netlink.AddrList(link, netlink.FAMILY_ALL)
		if err != nil {
			return nil, err
		}
		for _, raw := range addresses {
			addr, ok := netip.AddrFromSlice(raw.IP)
			if !ok {
				continue
			}
			addr = addr.Unmap()
			if !stableHostAddress(addr, raw.Flags) || !eligibleHostAddress(addr, prefix, hasPrefix) {
				continue
			}
			out = append(out, addr)
		}
	}
	slices.SortFunc(out, netip.Addr.Compare)
	return slices.Compact(out), nil
}

func stableHostAddress(addr netip.Addr, flags int) bool {
	return flags&(unix.IFA_F_DEPRECATED|unix.IFA_F_TENTATIVE) == 0 &&
		(!addr.Is6() || flags&unix.IFA_F_TEMPORARY == 0)
}
