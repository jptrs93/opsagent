//go:build linux

package network

import (
	"fmt"
	"sort"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

const nftTableName = "opendeploy"

// reconcileNft rebuilds the opendeploy nftables tables from the manager's full
// desired state in one netlink batch: the IPv4 egress masquerade plus DNAT
// rules for every published host port (v4 to the container's machine-local
// address, v6 to the stable instance address). Caller holds m.mu.
func (m *Manager) reconcileNft() error {
	c := &nftables.Conn{}

	tbl4 := c.AddTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: nftTableName})
	c.FlushTable(tbl4)
	post4 := c.AddChain(&nftables.Chain{
		Name: "postrouting", Table: tbl4, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookPostrouting, Priority: nftables.ChainPriorityNATSource,
	})
	pre4 := c.AddChain(&nftables.Chain{
		Name: "prerouting", Table: tbl4, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookPrerouting, Priority: nftables.ChainPriorityNATDest,
	})
	out4 := c.AddChain(&nftables.Chain{
		Name: "output", Table: tbl4, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityNATDest,
	})

	tbl6 := c.AddTable(&nftables.Table{Family: nftables.TableFamilyIPv6, Name: nftTableName})
	c.FlushTable(tbl6)
	pre6 := c.AddChain(&nftables.Chain{
		Name: "prerouting", Table: tbl6, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookPrerouting, Priority: nftables.ChainPriorityNATDest,
	})
	out6 := c.AddChain(&nftables.Chain{
		Name: "output", Table: tbl6, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityNATDest,
	})

	c.AddRule(&nftables.Rule{Table: tbl4, Chain: post4, Exprs: masqueradeExprs()})

	// Deterministic rule order (cosmetic; rules are disjoint by port).
	ids := make([]int32, 0, len(m.hostPorts))
	for id := range m.hostPorts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		for _, rule := range m.hostPorts[id].rules {
			if rule.TargetV4.Is4() {
				c.AddRule(&nftables.Rule{Table: tbl4, Chain: pre4,
					Exprs: dnatExprs(rule.Protocol, rule.HostPort, rule.TargetV4.AsSlice(), rule.TargetPort, unix.AF_INET)})
				c.AddRule(&nftables.Rule{Table: tbl4, Chain: out4,
					Exprs: dnatExprs(rule.Protocol, rule.HostPort, rule.TargetV4.AsSlice(), rule.TargetPort, unix.AF_INET)})
			}
			if rule.TargetV6.Is6() {
				c.AddRule(&nftables.Rule{Table: tbl6, Chain: pre6,
					Exprs: dnatExprs(rule.Protocol, rule.HostPort, rule.TargetV6.AsSlice(), rule.TargetPort, unix.AF_INET6)})
				c.AddRule(&nftables.Rule{Table: tbl6, Chain: out6,
					Exprs: dnatExprs(rule.Protocol, rule.HostPort, rule.TargetV6.AsSlice(), rule.TargetPort, unix.AF_INET6)})
			}
		}
	}

	if err := c.Flush(); err != nil {
		return fmt.Errorf("applying nftables ruleset: %w", err)
	}
	return nil
}

// masqueradeExprs: ip saddr <V4CIDR> ip daddr != <V4CIDR> masquerade
func masqueradeExprs() []expr.Any {
	base := V4CIDR.Addr().AsSlice()
	mask := []byte{0xff, 0xff, 0x00, 0x00} // /16
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4}, // saddr
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: mask, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: base},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4}, // daddr
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: mask, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: base},
		&expr.Masq{},
	}
}

// dnatExprs: fib daddr type local <proto> dport <hostPort> dnat to <target>:<targetPort>
//
// The fib address-type match restricts DNAT to traffic addressed to the
// machine itself ("published on the machine's host interfaces"). The rule is
// installed in prerouting for external traffic and output for host-local clients.
func dnatExprs(proto byte, hostPort uint16, target []byte, targetPort uint16, family int) []expr.Any {
	return []expr.Any{
		&expr.Fib{Register: 1, ResultADDRTYPE: true, FlagDADDR: true},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(unix.RTN_LOCAL)},
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2}, // dport
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(hostPort)},
		&expr.Immediate{Register: 1, Data: target},
		&expr.Immediate{Register: 2, Data: binaryutil.BigEndian.PutUint16(targetPort)},
		&expr.NAT{Type: expr.NATTypeDestNAT, Family: uint32(family), RegAddrMin: 1, RegProtoMin: 2},
	}
}
