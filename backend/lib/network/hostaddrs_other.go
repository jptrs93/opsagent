//go:build !linux

package network

import (
	"net"
	"net/netip"
	"slices"
)

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
