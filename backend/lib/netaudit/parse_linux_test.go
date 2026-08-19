//go:build linux

package netaudit

import (
	"testing"

	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
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
