//go:build linux

package netaudit

import (
	"bytes"
	"encoding/binary"
	"net"
	"net/netip"
	"strings"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

// parsedRule classifies one rule from the opendeploy tables.
type parsedRule struct {
	Masquerade bool
	DNAT       bool
	Proto      uint8
	HostPort   uint16
	Source     netip.Prefix
	Dest       netip.Prefix
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
	var pendingProto, pendingDport, pendingSaddr, pendingDaddr bool
	var addrLen uint32
	var addrOnes int
	for _, e := range exprs {
		switch v := e.(type) {
		case *expr.Meta:
			pendingProto = v.Key == expr.MetaKeyL4PROTO
			pendingDport, pendingSaddr, pendingDaddr = false, false, false
		case *expr.Payload:
			pendingDport = v.Base == expr.PayloadBaseTransportHeader && v.Offset == 2 && v.Len == 2
			pendingProto = false
			network := v.Base == expr.PayloadBaseNetworkHeader
			pendingSaddr = network && ((v.Offset == 12 && v.Len == 4) || (v.Offset == 8 && v.Len == 16))
			pendingDaddr = network && ((v.Offset == 16 && v.Len == 4) || (v.Offset == 24 && v.Len == 16))
			if pendingSaddr || pendingDaddr {
				addrLen = v.Len
				addrOnes = int(v.Len) * 8
			}
		case *expr.Bitwise:
			if (pendingSaddr || pendingDaddr) && v.Len == addrLen {
				ones, bits := net.IPMask(v.Mask).Size()
				if bits == int(addrLen)*8 {
					addrOnes = ones
				} else {
					pendingSaddr, pendingDaddr = false, false
				}
			} else {
				pendingSaddr, pendingDaddr = false, false
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
			if (pendingSaddr || pendingDaddr) && v.Op == expr.CmpOpEq && len(v.Data) == int(addrLen) {
				if addr, ok := netip.AddrFromSlice(v.Data); ok {
					if pendingSaddr {
						out.Source = netip.PrefixFrom(addr, addrOnes)
					} else {
						out.Dest = netip.PrefixFrom(addr, addrOnes)
					}
				}
			}
			pendingProto, pendingDport, pendingSaddr, pendingDaddr = false, false, false, false
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
		out.Source, out.Dest = netip.Prefix{}, netip.Prefix{}
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

func isFilterChain(name string) bool {
	return name == "forward" || strings.HasPrefix(name, "wl_dst_")
}

func parseSetElement(family, setName string, elem nftables.SetElement) (string, bool) {
	switch setName {
	case network.NftSetManaged, network.NftSetBlockedOut:
		if len(elem.Key) != 16 {
			return "", false
		}
		veth := ifnameString(elem.Key)
		if veth == "" {
			return "", false
		}
		if setName == network.NftSetBlockedOut {
			if family != "ip6" {
				return "", false
			}
			return network.BlockedOutElementKey(veth), true
		}
		return network.ManagedElementKey(family, veth), true
	case network.NftSetSrcOK:
		addrLen := 4
		if family == "ip6" {
			addrLen = 16
		}
		if len(elem.Key) != 16+addrLen {
			return "", false
		}
		veth := ifnameString(elem.Key[:16])
		addr, ok := netip.AddrFromSlice(elem.Key[16:])
		if veth == "" || !ok {
			return "", false
		}
		return network.SrcOKElementKey(family, veth, addr), true
	case network.NftMapDstDispatch:
		if family != "ip6" || len(elem.Key) != 16 {
			return "", false
		}
		addr, ok := netip.AddrFromSlice(elem.Key)
		if !ok {
			return "", false
		}
		chain, ok := parseJumpVerdict(elem.Val)
		if !ok {
			return "", false
		}
		return network.DispatchElementKey(addr, chain), true
	}
	return "", false
}

func ifnameString(key []byte) string {
	return string(bytes.TrimRight(key, "\x00"))
}

func parseJumpVerdict(val []byte) (string, bool) {
	ad, err := netlink.NewAttributeDecoder(val)
	if err != nil {
		return "", false
	}
	ad.ByteOrder = binary.BigEndian
	var code uint32
	var codeSeen bool
	var chain string
	for ad.Next() {
		switch ad.Type() {
		case unix.NFTA_VERDICT_CODE:
			code = ad.Uint32()
			codeSeen = true
		case unix.NFTA_VERDICT_CHAIN:
			chain = strings.TrimRight(ad.String(), "\x00")
		}
	}
	if ad.Err() != nil || !codeSeen || int32(code) != unix.NFT_JUMP || chain == "" {
		return "", false
	}
	return chain, true
}

func parseFilterRuleExprs(exprs []expr.Any) (network.FilterRule, bool) {
	const (
		pendingNone = iota
		pendingCtMask
		pendingCtCmp
		pendingIif
		pendingOif
		pendingProto
		pendingPort
		pendingPortEnd
		pendingAddrMask
		pendingAddrCmp
		pendingPairLookup
	)
	var out network.FilterRule
	state := pendingNone
	addrSource := false
	var addrLen uint32
	var addrOnes int
	verdictSeen := false
	ctMask := binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED)
	for _, e := range exprs {
		switch v := e.(type) {
		case *expr.Ct:
			if v.Key != expr.CtKeySTATE || v.SourceRegister || state != pendingNone {
				return network.FilterRule{}, false
			}
			state = pendingCtMask
		case *expr.Meta:
			if state != pendingNone {
				return network.FilterRule{}, false
			}
			switch v.Key {
			case expr.MetaKeyIIFNAME:
				state = pendingIif
			case expr.MetaKeyOIFNAME:
				state = pendingOif
			case expr.MetaKeyL4PROTO:
				state = pendingProto
			default:
				return network.FilterRule{}, false
			}
		case *expr.Payload:
			if state == pendingIif && v.Base == expr.PayloadBaseNetworkHeader &&
				((v.Len == 16 && v.Offset == 8) || (v.Len == 4 && v.Offset == 12)) {
				state = pendingPairLookup
				continue
			}
			if state != pendingNone {
				return network.FilterRule{}, false
			}
			if v.Base == expr.PayloadBaseTransportHeader && v.Offset == 2 && v.Len == 2 {
				state = pendingPort
				continue
			}
			if v.Base != expr.PayloadBaseNetworkHeader {
				return network.FilterRule{}, false
			}
			switch {
			case v.Len == 16 && v.Offset == 8:
				addrSource = true
			case v.Len == 16 && v.Offset == 24:
				addrSource = false
			case v.Len == 4 && v.Offset == 12:
				addrSource = true
			case v.Len == 4 && v.Offset == 16:
				addrSource = false
			default:
				return network.FilterRule{}, false
			}
			addrLen = v.Len
			addrOnes = int(v.Len) * 8
			state = pendingAddrMask
		case *expr.Bitwise:
			switch state {
			case pendingCtMask:
				if v.Len != 4 || !bytes.Equal(v.Mask, ctMask) {
					return network.FilterRule{}, false
				}
				state = pendingCtCmp
			case pendingAddrMask:
				ones, bits := net.IPMask(v.Mask).Size()
				if v.Len != addrLen || bits != int(addrLen)*8 {
					return network.FilterRule{}, false
				}
				addrOnes = ones
				state = pendingAddrCmp
			default:
				return network.FilterRule{}, false
			}
		case *expr.Cmp:
			switch state {
			case pendingCtCmp:
				if v.Op != expr.CmpOpNeq || len(v.Data) != 4 || !bytes.Equal(v.Data, []byte{0, 0, 0, 0}) {
					return network.FilterRule{}, false
				}
				out.CtEstablished = true
				state = pendingNone
			case pendingIif, pendingOif:
				if v.Op != expr.CmpOpEq || len(v.Data) != 16 {
					return network.FilterRule{}, false
				}
				name := string(bytes.TrimRight(v.Data, "\x00"))
				if state == pendingIif {
					out.IifName = name
				} else {
					out.OifName = name
				}
				state = pendingNone
			case pendingProto:
				if v.Op != expr.CmpOpEq || len(v.Data) != 1 {
					return network.FilterRule{}, false
				}
				out.Protocol = v.Data[0]
				state = pendingNone
			case pendingPort:
				if len(v.Data) != 2 {
					return network.FilterRule{}, false
				}
				port := binary.BigEndian.Uint16(v.Data)
				switch v.Op {
				case expr.CmpOpEq:
					out.Port = port
					state = pendingNone
				case expr.CmpOpGte:
					out.Port = port
					state = pendingPortEnd
				default:
					return network.FilterRule{}, false
				}
			case pendingPortEnd:
				if v.Op != expr.CmpOpLte || len(v.Data) != 2 {
					return network.FilterRule{}, false
				}
				out.PortEnd = binary.BigEndian.Uint16(v.Data)
				state = pendingNone
			case pendingAddrCmp:
				if v.Op != expr.CmpOpEq || len(v.Data) != int(addrLen) {
					return network.FilterRule{}, false
				}
				addr, ok := netip.AddrFromSlice(v.Data)
				if !ok {
					return network.FilterRule{}, false
				}
				prefix := netip.PrefixFrom(addr, addrOnes)
				if addrSource {
					out.Saddr = prefix
				} else {
					out.Daddr = prefix
				}
				state = pendingNone
			default:
				return network.FilterRule{}, false
			}
		case *expr.Lookup:
			switch state {
			case pendingIif:
				if v.Invert || v.IsDestRegSet {
					return network.FilterRule{}, false
				}
				out.IifSet = v.SetName
			case pendingOif:
				if v.Invert || v.IsDestRegSet {
					return network.FilterRule{}, false
				}
				out.OifSet = v.SetName
			case pendingPairLookup:
				if v.IsDestRegSet {
					return network.FilterRule{}, false
				}
				out.SrcPairSet = v.SetName
				out.SrcPairInvert = v.Invert
			case pendingAddrMask:
				if !v.IsDestRegSet || v.Invert || addrSource || addrLen != 16 {
					return network.FilterRule{}, false
				}
				out.DaddrVmap = v.SetName
			default:
				return network.FilterRule{}, false
			}
			state = pendingNone
		case *expr.Counter:
			if state != pendingNone {
				return network.FilterRule{}, false
			}
			out.Counter = true
		case *expr.Verdict:
			if state != pendingNone || verdictSeen {
				return network.FilterRule{}, false
			}
			switch v.Kind {
			case expr.VerdictAccept:
				out.Verdict = network.FilterVerdictAccept
			case expr.VerdictDrop:
				out.Verdict = network.FilterVerdictDrop
			case expr.VerdictReturn:
				out.Verdict = network.FilterVerdictReturn
			case expr.VerdictJump:
				out.Verdict = network.FilterVerdictJump
				out.JumpTarget = v.Chain
			default:
				return network.FilterRule{}, false
			}
			verdictSeen = true
		default:
			return network.FilterRule{}, false
		}
	}
	if (!verdictSeen && out.DaddrVmap == "") || state != pendingNone {
		return network.FilterRule{}, false
	}
	return out, true
}
