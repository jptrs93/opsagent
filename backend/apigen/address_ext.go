package apigen

import (
	"fmt"
	"net/netip"
)

// Addr converts a wire address to netip. The zero value of either alternative
// is an invalid Addr.
func (a IpAddress) Addr() netip.Addr {
	switch {
	case a.Value.Ipv4 != nil:
		if b, ok := netip.AddrFromSlice(a.Value.Ipv4.Octets); ok && b.Is4() {
			return b
		}
	case a.Value.Ipv6 != nil:
		if b, ok := netip.AddrFromSlice(a.Value.Ipv6.Octets); ok && b.Is6() {
			return b
		}
	}
	return netip.Addr{}
}

func (a IpAddress) String() string {
	return a.Addr().String()
}

// AddrOf builds the wire form of a netip address. Mapped IPv4 addresses are
// unmapped so the family is the one the address names.
func AddrOf(addr netip.Addr) IpAddress {
	addr = addr.Unmap()
	if addr.Is4() {
		b := addr.As4()
		return IpAddress{Value: IpAddressValueOneof{Ipv4: &IPv4Address{Octets: b[:]}}}
	}
	b := addr.As16()
	return IpAddress{Value: IpAddressValueOneof{Ipv6: &IPv6Address{Octets: b[:]}}}
}

// ParseAddr parses an IP literal into its wire form.
func ParseAddr(s string) (IpAddress, error) {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return IpAddress{}, err
	}
	return AddrOf(addr), nil
}

// Prefix converts a wire prefix to netip. An invalid prefix is the zero
// Prefix.
func (p IpPrefix) Prefix() netip.Prefix {
	switch {
	case p.Value.Ipv4 != nil:
		if a, ok := netip.AddrFromSlice(p.Value.Ipv4.Address.Octets); ok && a.Is4() {
			if pfx, err := a.Prefix(int(p.Value.Ipv4.PrefixLength)); err == nil {
				return pfx
			}
		}
	case p.Value.Ipv6 != nil:
		if a, ok := netip.AddrFromSlice(p.Value.Ipv6.Address.Octets); ok && a.Is6() {
			if pfx, err := a.Prefix(int(p.Value.Ipv6.PrefixLength)); err == nil {
				return pfx
			}
		}
	}
	return netip.Prefix{}
}

func (p IpPrefix) String() string {
	return p.Prefix().String()
}

// Masked reports whether the stored address has no bits set beyond the
// prefix length, the canonical form PrefixOf writes.
func (p IpPrefix) Masked() bool {
	var octets []byte
	switch {
	case p.Value.Ipv4 != nil:
		octets = p.Value.Ipv4.Address.Octets
	case p.Value.Ipv6 != nil:
		octets = p.Value.Ipv6.Address.Octets
	default:
		return false
	}
	addr, ok := netip.AddrFromSlice(octets)
	pfx := p.Prefix()
	return ok && pfx.IsValid() && pfx.Addr() == addr
}

// PrefixOf builds the wire form of a netip prefix, masked to its length.
func PrefixOf(pfx netip.Prefix) IpPrefix {
	pfx = pfx.Masked()
	addr := pfx.Addr().Unmap()
	if addr.Is4() {
		b := addr.As4()
		return IpPrefix{Value: IpPrefixValueOneof{Ipv4: &IPv4Prefix{Address: IPv4Address{Octets: b[:]}, PrefixLength: uint32(pfx.Bits())}}}
	}
	b := addr.As16()
	return IpPrefix{Value: IpPrefixValueOneof{Ipv6: &IPv6Prefix{Address: IPv6Address{Octets: b[:]}, PrefixLength: uint32(pfx.Bits())}}}
}

// ParsePrefix parses an IP or CIDR literal into its wire form; a bare address
// is the single-address prefix of its family.
func ParsePrefix(s string) (IpPrefix, error) {
	if pfx, err := netip.ParsePrefix(s); err == nil {
		return PrefixOf(pfx), nil
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return IpPrefix{}, fmt.Errorf("invalid address or prefix %q", s)
	}
	return PrefixOf(netip.PrefixFrom(addr, addr.BitLen())), nil
}
