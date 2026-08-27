//go:build linux

package netaudit

import (
	"net"
	"net/netip"
	"testing"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/jptrs93/opsagent/backend/lib/network"
	nl "github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

// dnatTestExprs mirrors dnatExprs in the network package.
func dnatTestExprs(proto byte, hostPort uint16, target []byte, targetPort uint16, family uint32) []expr.Any {
	return []expr.Any{
		&expr.Fib{Register: 1, ResultADDRTYPE: true, FlagDADDR: true},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(unix.RTN_LOCAL)},
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(hostPort)},
		&expr.Immediate{Register: 1, Data: target},
		&expr.Immediate{Register: 2, Data: binaryutil.BigEndian.PutUint16(targetPort)},
		&expr.NAT{Type: expr.NATTypeDestNAT, Family: family, RegAddrMin: 1, RegProtoMin: 2},
	}
}

func TestParseRuleExprsDNAT(t *testing.T) {
	exprs := dnatTestExprs(unix.IPPROTO_TCP, 443, targetV6.AsSlice(), 8443, unix.AF_INET6)
	parsed, ok := parseRuleExprs(exprs)
	if !ok || !parsed.DNAT || parsed.Masquerade {
		t.Fatalf("expected recognized DNAT rule, got %+v ok=%v", parsed, ok)
	}
	if parsed.Proto != unix.IPPROTO_TCP || parsed.HostPort != 443 || parsed.Target != targetV6 || parsed.TargetPort != 8443 {
		t.Fatalf("parsed rule fields wrong: %+v", parsed)
	}
}

func TestParseRuleExprsDNATWithSourceMatch(t *testing.T) {
	exprs := append([]expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: []byte{0xff, 0xff, 0xff, 0}, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{203, 0, 113, 0}},
	}, dnatTestExprs(unix.IPPROTO_TCP, 443, targetV4.AsSlice(), 8443, unix.AF_INET)...)
	parsed, ok := parseRuleExprs(exprs)
	if !ok || !parsed.DNAT {
		t.Fatalf("expected recognized DNAT rule, got %+v ok=%v", parsed, ok)
	}
	if parsed.Source != netip.MustParsePrefix("203.0.113.0/24") {
		t.Fatalf("parsed source = %v, want 203.0.113.0/24", parsed.Source)
	}
	if parsed.Proto != unix.IPPROTO_TCP || parsed.HostPort != 443 || parsed.Target != targetV4 || parsed.TargetPort != 8443 {
		t.Fatalf("parsed rule fields wrong: %+v", parsed)
	}
}

func TestParseRuleExprsDNATWithV6SourceMatch(t *testing.T) {
	src := netip.MustParsePrefix("2001:db8::/32")
	exprs := append([]expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 8, Len: 16},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 16, Mask: net.CIDRMask(32, 128), Xor: make([]byte, 16)},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: src.Addr().AsSlice()},
	}, dnatTestExprs(unix.IPPROTO_TCP, 443, targetV6.AsSlice(), 8443, unix.AF_INET6)...)
	parsed, ok := parseRuleExprs(exprs)
	if !ok || !parsed.DNAT || parsed.Source != src {
		t.Fatalf("expected recognized DNAT rule with source %v, got %+v ok=%v", src, parsed, ok)
	}
}

func TestParseRuleExprsMasqueradeSourceIgnored(t *testing.T) {
	exprs := []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: []byte{0xff, 0xff, 0, 0}, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{10, 100, 0, 0}},
		&expr.Masq{},
	}
	parsed, ok := parseRuleExprs(exprs)
	if !ok || !parsed.Masquerade || parsed.Source.IsValid() {
		t.Fatalf("expected masquerade with no source, got %+v ok=%v", parsed, ok)
	}
}

func TestParseRuleExprsMasquerade(t *testing.T) {
	exprs := []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: []byte{0xff, 0xff, 0, 0}, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{10, 100, 0, 0}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: []byte{0xff, 0xff, 0, 0}, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{10, 100, 0, 0}},
		&expr.Masq{},
	}
	parsed, ok := parseRuleExprs(exprs)
	if !ok || !parsed.Masquerade {
		t.Fatalf("expected recognized masquerade rule, got %+v ok=%v", parsed, ok)
	}
}

func TestParseRuleExprsUnrecognized(t *testing.T) {
	if _, ok := parseRuleExprs([]expr.Any{&expr.Counter{}}); ok {
		t.Fatal("expected foreign rule to be unrecognized")
	}
}

func filterTestExprs(rule network.FilterRule, is6 bool) []expr.Any {
	var exprs []expr.Any
	ifname := func(name string) []byte {
		b := make([]byte, 16)
		copy(b, name)
		return b
	}
	saddrPayload := func(register uint32) *expr.Payload {
		if is6 {
			return &expr.Payload{DestRegister: register, Base: expr.PayloadBaseNetworkHeader, Offset: 8, Len: 16}
		}
		return &expr.Payload{DestRegister: register, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4}
	}
	if rule.CtEstablished {
		exprs = append(exprs,
			&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
			&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4,
				Mask: binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED),
				Xor:  binaryutil.NativeEndian.PutUint32(0)},
			&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(0)},
		)
	}
	if rule.IifName != "" {
		exprs = append(exprs,
			&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ifname(rule.IifName)},
		)
	}
	if rule.IifSet != "" {
		exprs = append(exprs,
			&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
			&expr.Lookup{SourceRegister: 1, SetName: rule.IifSet},
		)
	}
	if rule.OifName != "" {
		exprs = append(exprs,
			&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ifname(rule.OifName)},
		)
	}
	if rule.OifSet != "" {
		exprs = append(exprs,
			&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
			&expr.Lookup{SourceRegister: 1, SetName: rule.OifSet},
		)
	}
	if rule.SrcPairSet != "" {
		exprs = append(exprs,
			&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
			saddrPayload(2),
			&expr.Lookup{SourceRegister: 1, SetName: rule.SrcPairSet, Invert: rule.SrcPairInvert},
		)
	}
	addrMatch := func(prefix netip.Prefix, source bool) []expr.Any {
		addr := prefix.Addr()
		length := uint32(addr.BitLen() / 8)
		var offset uint32
		switch {
		case addr.Is6() && source:
			offset = 8
		case addr.Is6():
			offset = 24
		case source:
			offset = 12
		default:
			offset = 16
		}
		return []expr.Any{
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: length},
			&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: length, Mask: net.CIDRMask(prefix.Bits(), int(length)*8), Xor: make([]byte, length)},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: addr.AsSlice()},
		}
	}
	if rule.Saddr.IsValid() {
		exprs = append(exprs, addrMatch(rule.Saddr, true)...)
	}
	if rule.Daddr.IsValid() {
		exprs = append(exprs, addrMatch(rule.Daddr, false)...)
	}
	if rule.DaddrVmap != "" {
		exprs = append(exprs,
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 24, Len: 16},
			&expr.Lookup{SourceRegister: 1, SetName: rule.DaddrVmap, DestRegister: 0, IsDestRegSet: true},
		)
	}
	if rule.Protocol != 0 {
		exprs = append(exprs,
			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{rule.Protocol}},
		)
		if rule.Port != 0 {
			exprs = append(exprs, &expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2})
			if rule.PortEnd != 0 {
				exprs = append(exprs,
					&expr.Cmp{Op: expr.CmpOpGte, Register: 1, Data: binaryutil.BigEndian.PutUint16(rule.Port)},
					&expr.Cmp{Op: expr.CmpOpLte, Register: 1, Data: binaryutil.BigEndian.PutUint16(rule.PortEnd)},
				)
			} else {
				exprs = append(exprs, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(rule.Port)})
			}
		}
	}
	if rule.Counter {
		exprs = append(exprs, &expr.Counter{})
	}
	switch rule.Verdict {
	case network.FilterVerdictAccept:
		exprs = append(exprs, &expr.Verdict{Kind: expr.VerdictAccept})
	case network.FilterVerdictDrop:
		exprs = append(exprs, &expr.Verdict{Kind: expr.VerdictDrop})
	case network.FilterVerdictReturn:
		exprs = append(exprs, &expr.Verdict{Kind: expr.VerdictReturn})
	case network.FilterVerdictJump:
		exprs = append(exprs, &expr.Verdict{Kind: expr.VerdictJump, Chain: rule.JumpTarget})
	}
	return exprs
}

func TestParseFilterRuleExprsRoundTrip(t *testing.T) {
	rules6 := []network.FilterRule{
		{CtEstablished: true, Verdict: network.FilterVerdictAccept},
		{IifName: "od7s0", Verdict: network.FilterVerdictJump, JumpTarget: "wl_dst_7"},
		{OifName: "od7s0", Verdict: network.FilterVerdictJump, JumpTarget: "wl_dst_7"},
		{Saddr: netip.MustParsePrefix("fd11:2233:4455:5::/64"), Verdict: network.FilterVerdictAccept},
		{Saddr: netip.MustParsePrefix("fd11:2233:4455::1/128"), Verdict: network.FilterVerdictReturn},
		{Saddr: netip.MustParsePrefix("fd11:2233:4455::/48"), Counter: true, Verdict: network.FilterVerdictDrop},
		{Protocol: unix.IPPROTO_UDP, Port: 53, Verdict: network.FilterVerdictAccept},
		{Saddr: netip.MustParsePrefix("fd11:2233:4455:6:0:800::/88"), Protocol: unix.IPPROTO_TCP, Port: 8000, PortEnd: 9000, Verdict: network.FilterVerdictAccept},
		{Counter: true, Verdict: network.FilterVerdictDrop},
	}
	rules6 = append(rules6, network.StaticFilterRules6()...)
	rules4 := []network.FilterRule{
		{Daddr: netip.MustParsePrefix("10.201.0.0/16"), Counter: true, Verdict: network.FilterVerdictDrop},
		{Saddr: netip.MustParsePrefix("10.201.0.2/32"), Verdict: network.FilterVerdictReturn},
	}
	rules4 = append(rules4, network.StaticFilterRules4()...)
	roundTrip := func(rules []network.FilterRule, is6 bool, family string) {
		for _, rule := range rules {
			parsed, ok := parseFilterRuleExprs(filterTestExprs(rule, is6))
			if !ok {
				t.Fatalf("rule %q not recognized", rule.Key(family, "c"))
			}
			if parsed != rule {
				t.Fatalf("round trip mismatch: got %q, want %q", parsed.Key(family, "c"), rule.Key(family, "c"))
			}
		}
	}
	roundTrip(rules6, true, "ip6")
	roundTrip(rules4, false, "ip")
}

func TestParseFilterRuleExprsRejectsForeignShapes(t *testing.T) {
	if _, ok := parseFilterRuleExprs([]expr.Any{&expr.Masq{}}); ok {
		t.Fatal("expected masquerade to be unrecognized in a filter chain")
	}
	if _, ok := parseFilterRuleExprs([]expr.Any{&expr.Counter{}}); ok {
		t.Fatal("expected rule without verdict to be unrecognized")
	}
	if _, ok := parseFilterRuleExprs(dnatTestExprs(unix.IPPROTO_TCP, 443, targetV6.AsSlice(), 8443, unix.AF_INET6)); ok {
		t.Fatal("expected DNAT rule to be unrecognized in a filter chain")
	}
}

func TestIsFilterChain(t *testing.T) {
	for _, name := range []string{"forward", "wl_dst_7"} {
		if !isFilterChain(name) {
			t.Fatalf("%s should be a filter chain", name)
		}
	}
	for _, name := range []string{"prerouting", "postrouting", "output", "wl_src_od7s0", "wl4_src_od7s0"} {
		if isFilterChain(name) {
			t.Fatalf("%s should not be a filter chain", name)
		}
	}
}

func ifnameBytes(name string) []byte {
	b := make([]byte, 16)
	copy(b, name)
	return b
}

func jumpVerdictVal(t *testing.T, chain string) []byte {
	t.Helper()
	var jump int32 = unix.NFT_JUMP
	val, err := nl.MarshalAttributes([]nl.Attribute{
		{Type: unix.NFTA_VERDICT_CODE, Data: binaryutil.BigEndian.PutUint32(uint32(jump))},
		{Type: unix.NFTA_VERDICT_CHAIN, Data: []byte(chain + "\x00")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return val
}

func TestParseSetElement(t *testing.T) {
	addr6 := netip.MustParseAddr("fd11:2233:4455:5::1")
	addr4 := netip.MustParseAddr("10.201.0.2")

	key, ok := parseSetElement("ip6", network.NftSetManaged, nftables.SetElement{Key: ifnameBytes("od7s0")})
	if !ok || key != "ip6 set managed od7s0" {
		t.Fatalf("managed element = %q ok=%v", key, ok)
	}
	key, ok = parseSetElement("ip6", network.NftSetSrcOK, nftables.SetElement{Key: append(ifnameBytes("od7s0"), addr6.AsSlice()...)})
	if !ok || key != "ip6 set src_ok od7s0 . "+addr6.String() {
		t.Fatalf("src_ok v6 element = %q ok=%v", key, ok)
	}
	key, ok = parseSetElement("ip", network.NftSetSrcOK, nftables.SetElement{Key: append(ifnameBytes("od7s0"), addr4.AsSlice()...)})
	if !ok || key != "ip set src_ok od7s0 . "+addr4.String() {
		t.Fatalf("src_ok v4 element = %q ok=%v", key, ok)
	}
	key, ok = parseSetElement("ip6", network.NftSetBlockedOut, nftables.SetElement{Key: ifnameBytes("od7s0")})
	if !ok || key != "ip6 set blocked_out od7s0" {
		t.Fatalf("blocked_out element = %q ok=%v", key, ok)
	}
	key, ok = parseSetElement("ip6", network.NftMapDstDispatch, nftables.SetElement{
		Key: addr6.AsSlice(),
		Val: jumpVerdictVal(t, "wl_dst_7"),
	})
	if !ok || key != "ip6 map dst_dispatch "+addr6.String()+" : jump wl_dst_7" {
		t.Fatalf("dispatch element = %q ok=%v", key, ok)
	}

	if _, ok := parseSetElement("ip", network.NftSetBlockedOut, nftables.SetElement{Key: ifnameBytes("od7s0")}); ok {
		t.Fatal("blocked_out must be ip6 only")
	}
	if _, ok := parseSetElement("ip6", network.NftSetSrcOK, nftables.SetElement{Key: ifnameBytes("od7s0")}); ok {
		t.Fatal("short src_ok key must be rejected")
	}
	if _, ok := parseSetElement("ip6", network.NftMapDstDispatch, nftables.SetElement{Key: addr6.AsSlice()}); ok {
		t.Fatal("dispatch element without verdict must be rejected")
	}
	if _, ok := parseSetElement("ip6", "unknown", nftables.SetElement{Key: ifnameBytes("od7s0")}); ok {
		t.Fatal("unknown set must be rejected")
	}
}
