package network

import (
	"fmt"
	"net/netip"
	"strings"
)

func ParseFilterEntry(entry string) (netip.Prefix, error) {
	trimmed := strings.TrimSpace(entry)
	if trimmed == "" {
		return netip.Prefix{}, fmt.Errorf("entry must be an IP address or CIDR prefix")
	}
	if !strings.Contains(trimmed, "/") {
		addr, err := netip.ParseAddr(trimmed)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("%q is not a valid IP address or CIDR prefix", trimmed)
		}
		if addr.Zone() != "" || addr.Is4In6() {
			return netip.Prefix{}, fmt.Errorf("%q must be a plain IPv4 or IPv6 address", trimmed)
		}
		return netip.PrefixFrom(addr, addr.BitLen()), nil
	}
	prefix, err := netip.ParsePrefix(trimmed)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%q is not a valid IP address or CIDR prefix", trimmed)
	}
	if prefix.Addr().Is4In6() {
		return netip.Prefix{}, fmt.Errorf("%q must use the plain IPv4 form", trimmed)
	}
	if prefix.Masked() != prefix {
		return netip.Prefix{}, fmt.Errorf("%q has host bits set", trimmed)
	}
	return prefix, nil
}

func FilterEntryString(prefix netip.Prefix) string {
	if prefix.IsSingleIP() {
		return prefix.Addr().String()
	}
	return prefix.String()
}

func SplitFilterPrefixes(entries []string) (v4, v6 []netip.Prefix) {
	for _, entry := range entries {
		prefix, err := ParseFilterEntry(entry)
		if err != nil {
			continue
		}
		if prefix.Addr().Is4() {
			v4 = append(v4, prefix)
		} else {
			v6 = append(v6, prefix)
		}
	}
	return v4, v6
}
