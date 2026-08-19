//go:build linux

package netaudit

import (
	"encoding/binary"
	"net/netip"

	"github.com/google/nftables/expr"
)

// parsedRule classifies one rule from the opendeploy tables.
type parsedRule struct {
	Masquerade bool
	DNAT       bool
	Proto      uint8
	HostPort   uint16
	Target     netip.Addr
	TargetPort uint16
}

// parseRuleExprs recognizes the two rule shapes the manager writes (see
// dnatExprs and masqueradeExprs in the network package) from a decoded
// expression list. Anything else in the table is reported as unrecognized.
func parseRuleExprs(exprs []expr.Any) (parsedRule, bool) {
	var out parsedRule
	var protoSeen, dportSeen bool
	var immAddr, immPort []byte
	var pendingProto, pendingDport bool
	for _, e := range exprs {
		switch v := e.(type) {
		case *expr.Meta:
			pendingProto = v.Key == expr.MetaKeyL4PROTO
			pendingDport = false
		case *expr.Payload:
			pendingDport = v.Base == expr.PayloadBaseTransportHeader && v.Offset == 2 && v.Len == 2
			pendingProto = false
		case *expr.Cmp:
			if pendingProto && len(v.Data) == 1 {
				out.Proto = v.Data[0]
				protoSeen = true
			}
			if pendingDport && len(v.Data) == 2 {
				out.HostPort = binary.BigEndian.Uint16(v.Data)
				dportSeen = true
			}
			pendingProto, pendingDport = false, false
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
