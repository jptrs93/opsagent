//go:build linux

package netaudit

import (
	"encoding/binary"
	"net"
	"net/netip"

	"github.com/google/nftables/expr"
)

// parsedRule classifies one rule from the opendeploy tables.
type parsedRule struct {
	Masquerade bool
	DNAT       bool
	Proto      uint8
	HostPort   uint16
	Source     netip.Prefix
	Target     netip.Addr
	TargetPort uint16
}

// parseRuleExprs recognizes the rule shapes the manager writes (see
// dnatExprs, saddrMatchExprs, and masqueradeExprs in the network package)
// from a decoded expression list. Anything else in the table is reported as
// unrecognized.
func parseRuleExprs(exprs []expr.Any) (parsedRule, bool) {
	var out parsedRule
	var protoSeen, dportSeen bool
	var immAddr, immPort []byte
	var pendingProto, pendingDport, pendingSaddr bool
	var saddrLen uint32
	var saddrOnes int
	for _, e := range exprs {
		switch v := e.(type) {
		case *expr.Meta:
			pendingProto = v.Key == expr.MetaKeyL4PROTO
			pendingDport, pendingSaddr = false, false
		case *expr.Payload:
			pendingDport = v.Base == expr.PayloadBaseTransportHeader && v.Offset == 2 && v.Len == 2
			pendingProto = false
			pendingSaddr = v.Base == expr.PayloadBaseNetworkHeader && ((v.Offset == 12 && v.Len == 4) || (v.Offset == 8 && v.Len == 16))
			if pendingSaddr {
				saddrLen = v.Len
				saddrOnes = int(v.Len) * 8
			}
		case *expr.Bitwise:
			if pendingSaddr && v.Len == saddrLen {
				ones, bits := net.IPMask(v.Mask).Size()
				if bits == int(saddrLen)*8 {
					saddrOnes = ones
				} else {
					pendingSaddr = false
				}
			} else {
				pendingSaddr = false
			}
		case *expr.Cmp:
			if pendingProto && len(v.Data) == 1 {
				out.Proto = v.Data[0]
				protoSeen = true
			}
			if pendingDport && len(v.Data) == 2 {
				out.HostPort = binary.BigEndian.Uint16(v.Data)
				dportSeen = true
			}
			if pendingSaddr && v.Op == expr.CmpOpEq && len(v.Data) == int(saddrLen) {
				if addr, ok := netip.AddrFromSlice(v.Data); ok {
					out.Source = netip.PrefixFrom(addr, saddrOnes)
				}
			}
			pendingProto, pendingDport, pendingSaddr = false, false, false
		case *expr.Immediate:
			switch v.Register {
			case 1:
				immAddr = v.Data
			case 2:
				immPort = v.Data
			}
		case *expr.NAT:
			out.DNAT = v.Type == expr.NATTypeDestNAT
		case *expr.Masq:
			out.Masquerade = true
		}
	}
	if out.Masquerade {
		out.Source = netip.Prefix{}
		return out, true
	}
	addr, addrOK := netip.AddrFromSlice(immAddr)
	if out.DNAT && protoSeen && dportSeen && addrOK && len(immPort) == 2 {
		out.Target = addr
		out.TargetPort = binary.BigEndian.Uint16(immPort)
		return out, true
	}
	return parsedRule{}, false
}
