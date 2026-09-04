//go:build linux

package network

import (
	"fmt"
	"hash/fnv"
	"net"
	"net/netip"
	"sort"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

const nftTableName = "opendeploy"

const nftNetlinkReadBuffer = 8 << 20

func NewNftConn() (*nftables.Conn, error) {
	return nftables.New(nftables.WithSockOptions(func(nc *netlink.Conn) error {
		return nc.SetReadBuffer(nftNetlinkReadBuffer)
	}))
}

type nftHandles struct {
	tbl4, tbl6                             *nftables.Table
	post4, pre4, out4, fwd4                *nftables.Chain
	pre6, out6, fwd6                       *nftables.Chain
	managed4, srcOK4                       *nftables.Set
	managed6, srcOK6, blockedOut, dispatch *nftables.Set
}

func newNftHandles() *nftHandles {
	h := &nftHandles{
		tbl4: &nftables.Table{Family: nftables.TableFamilyIPv4, Name: nftTableName},
		tbl6: &nftables.Table{Family: nftables.TableFamilyIPv6, Name: nftTableName},
	}
	h.post4 = &nftables.Chain{
		Name: "postrouting", Table: h.tbl4, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookPostrouting, Priority: nftables.ChainPriorityNATSource,
	}
	h.pre4 = &nftables.Chain{
		Name: "prerouting", Table: h.tbl4, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookPrerouting, Priority: nftables.ChainPriorityNATDest,
	}
	h.out4 = &nftables.Chain{
		Name: "output", Table: h.tbl4, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityNATDest,
	}
	h.pre6 = &nftables.Chain{
		Name: "prerouting", Table: h.tbl6, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookPrerouting, Priority: nftables.ChainPriorityNATDest,
	}
	h.out6 = &nftables.Chain{
		Name: "output", Table: h.tbl6, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityNATDest,
	}
	policy4 := nftables.ChainPolicyAccept
	policy6 := nftables.ChainPolicyAccept
	h.fwd4 = &nftables.Chain{
		Name: "forward", Table: h.tbl4, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookForward, Priority: nftables.ChainPriorityFilter,
		Policy: &policy4,
	}
	h.fwd6 = &nftables.Chain{
		Name: "forward", Table: h.tbl6, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookForward, Priority: nftables.ChainPriorityFilter,
		Policy: &policy6,
	}
	h.managed4 = &nftables.Set{Table: h.tbl4, Name: NftSetManaged, KeyType: nftables.TypeIFName}
	h.srcOK4 = &nftables.Set{
		Table: h.tbl4, Name: NftSetSrcOK, Concatenation: true,
		KeyType: nftables.MustConcatSetType(nftables.TypeIFName, nftables.TypeIPAddr),
	}
	h.managed6 = &nftables.Set{Table: h.tbl6, Name: NftSetManaged, KeyType: nftables.TypeIFName}
	h.srcOK6 = &nftables.Set{
		Table: h.tbl6, Name: NftSetSrcOK, Concatenation: true,
		KeyType: nftables.MustConcatSetType(nftables.TypeIFName, nftables.TypeIP6Addr),
	}
	h.blockedOut = &nftables.Set{Table: h.tbl6, Name: NftSetBlockedOut, KeyType: nftables.TypeIFName}
	h.dispatch = &nftables.Set{
		Table: h.tbl6, Name: NftMapDstDispatch, IsMap: true,
		KeyType: nftables.TypeIP6Addr, DataType: nftables.TypeVerdict,
	}
	return h
}

func (h *nftHandles) sets() []*nftables.Set {
	return []*nftables.Set{h.managed4, h.srcOK4, h.managed6, h.srcOK6, h.blockedOut, h.dispatch}
}

func (h *nftHandles) natChains() []*nftables.Chain {
	return []*nftables.Chain{h.pre4, h.out4, h.pre6, h.out6}
}

func buildSkeleton(c *nftables.Conn, h *nftHandles) error {
	c.AddTable(h.tbl4)
	c.DelTable(h.tbl4)
	c.AddTable(h.tbl4)
	c.AddTable(h.tbl6)
	c.DelTable(h.tbl6)
	c.AddTable(h.tbl6)
	for _, chain := range []*nftables.Chain{h.post4, h.pre4, h.out4, h.fwd4, h.pre6, h.out6, h.fwd6} {
		c.AddChain(chain)
	}
	for _, set := range h.sets() {
		if err := c.AddSet(set, nil); err != nil {
			return fmt.Errorf("creating set %s: %w", set.Name, err)
		}
	}
	c.AddRule(&nftables.Rule{Table: h.tbl4, Chain: h.post4, Exprs: masqueradeExprs()})
	for _, rule := range StaticFilterRules4() {
		c.AddRule(&nftables.Rule{Table: h.tbl4, Chain: h.fwd4, Exprs: filterExprs(rule, false)})
	}
	for _, rule := range StaticFilterRules6() {
		c.AddRule(&nftables.Rule{Table: h.tbl6, Chain: h.fwd6, Exprs: filterExprs(rule, true)})
	}
	return nil
}

func (m *Manager) reconcileNft() error {
	c, err := NewNftConn()
	if err != nil {
		return fmt.Errorf("opening nftables connection: %w", err)
	}
	h := newNftHandles()
	fresh := !m.nftSkeletonReady
	if fresh {
		if err := buildSkeleton(c, h); err != nil {
			return m.reconcileFailedLocked(err)
		}
	} else {
		for _, set := range h.sets() {
			c.FlushSet(set)
		}
	}

	state := RenderFilterState(m.prefix, m.hasPrefix, m.netproxyDeploymentID, m.filterNetList(), m.policyRules)

	desiredChains := make(map[string]uint64, len(state.DstChains))
	for _, chain := range state.DstChains {
		desiredChains[chain.Name] = dstChainHash(chain)
	}
	if !fresh {
		for name := range m.nftDstChains {
			if _, ok := desiredChains[name]; ok {
				continue
			}
			stale := &nftables.Chain{Name: name, Table: h.tbl6}
			c.FlushChain(stale)
			c.DelChain(stale)
		}
	}
	for _, chain := range state.DstChains {
		target := &nftables.Chain{Name: chain.Name, Table: h.tbl6}
		if !fresh {
			if prev, ok := m.nftDstChains[chain.Name]; ok {
				if prev == desiredChains[chain.Name] {
					continue
				}
				c.FlushChain(target)
			}
		}
		c.AddChain(target)
		for _, rule := range chain.Rules {
			c.AddRule(&nftables.Rule{Table: h.tbl6, Chain: target, Exprs: filterExprs(rule, true)})
		}
	}

	if err := addFilterElements(c, h, state); err != nil {
		return m.reconcileFailedLocked(err)
	}

	natHash := m.natSignatureLocked()
	if fresh || natHash != m.nftNatHash {
		if !fresh {
			for _, chain := range h.natChains() {
				c.FlushChain(chain)
			}
		}
		m.addNatRulesLocked(c, h)
	}

	if err := c.Flush(); err != nil {
		return m.reconcileFailedLocked(fmt.Errorf("applying nftables ruleset: %w", err))
	}
	m.nftSkeletonReady = true
	m.nftNatHash = natHash
	m.nftDstChains = desiredChains
	return nil
}

func (m *Manager) reconcileFailedLocked(err error) error {
	m.nftSkeletonReady = false
	m.nftNatHash = 0
	m.nftDstChains = nil
	return err
}

func addFilterElements(c *nftables.Conn, h *nftHandles, state FilterState) error {
	add := func(set *nftables.Set, elems []nftables.SetElement) error {
		if len(elems) == 0 {
			return nil
		}
		if err := c.SetAddElements(set, elems); err != nil {
			return fmt.Errorf("adding elements to set %s: %w", set.Name, err)
		}
		return nil
	}
	if err := add(h.managed6, ifnameElements(state.Managed6)); err != nil {
		return err
	}
	if err := add(h.srcOK6, pairElements(state.SrcOK6)); err != nil {
		return err
	}
	if err := add(h.blockedOut, ifnameElements(state.BlockedOut)); err != nil {
		return err
	}
	if err := add(h.dispatch, dispatchElements(state.Dispatch)); err != nil {
		return err
	}
	if err := add(h.managed4, ifnameElements(state.Managed4)); err != nil {
		return err
	}
	return add(h.srcOK4, pairElements(state.SrcOK4))
}

func ifnameElements(veths []string) []nftables.SetElement {
	elems := make([]nftables.SetElement, 0, len(veths))
	for _, veth := range veths {
		elems = append(elems, nftables.SetElement{Key: ifname(veth)})
	}
	return elems
}

func pairElements(pairs []VethAddr) []nftables.SetElement {
	elems := make([]nftables.SetElement, 0, len(pairs))
	for _, pair := range pairs {
		elems = append(elems, nftables.SetElement{Key: append(ifname(pair.Veth), pair.Addr.AsSlice()...)})
	}
	return elems
}

func dispatchElements(entries []DispatchElem) []nftables.SetElement {
	elems := make([]nftables.SetElement, 0, len(entries))
	for _, entry := range entries {
		elems = append(elems, nftables.SetElement{
			Key:         entry.Addr.AsSlice(),
			VerdictData: &expr.Verdict{Kind: expr.VerdictJump, Chain: entry.Chain},
		})
	}
	return elems
}

func dstChainHash(chain FilterChain) uint64 {
	hash := fnv.New64a()
	for _, rule := range chain.Rules {
		fmt.Fprintln(hash, rule.Key("ip6", chain.Name))
	}
	return hash.Sum64()
}

func (m *Manager) natSignatureLocked() uint64 {
	hash := fnv.New64a()
	for _, id := range m.sortedHostPortIDsLocked() {
		for _, rule := range m.hostPorts[id].rules {
			fmt.Fprintf(hash, "%d|%d|%d|%d|%s|%s|%t|%v|%v|%v\n",
				id, rule.Protocol, rule.HostPort, rule.TargetPort,
				rule.TargetV4, rule.TargetV6, rule.Filtered, rule.AllowV4, rule.AllowV6, rule.Dest)
		}
	}
	return hash.Sum64()
}

func (m *Manager) sortedHostPortIDsLocked() []int32 {
	ids := make([]int32, 0, len(m.hostPorts))
	for id := range m.hostPorts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (m *Manager) addNatRulesLocked(c *nftables.Conn, h *nftHandles) {
	for _, id := range m.sortedHostPortIDsLocked() {
		for _, rule := range m.hostPorts[id].rules {
			if rule.TargetV4.Is4() {
				addDNATRules(c, h.tbl4, h.pre4, h.out4, rule, rule.TargetV4, rule.AllowV4, unix.AF_INET)
			}
			if rule.TargetV6.Is6() {
				addDNATRules(c, h.tbl6, h.pre6, h.out6, rule, rule.TargetV6, rule.AllowV6, unix.AF_INET6)
			}
		}
	}
}

func filterExprs(rule FilterRule, is6 bool) []expr.Any {
	var exprs []expr.Any
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
			saddrPayload(is6, 2),
			&expr.Lookup{SourceRegister: 1, SetName: rule.SrcPairSet, Invert: rule.SrcPairInvert},
		)
	}
	if rule.Saddr.IsValid() {
		exprs = append(exprs, addrMatchExprs(rule.Saddr, true)...)
	}
	if rule.Daddr.IsValid() {
		exprs = append(exprs, addrMatchExprs(rule.Daddr, false)...)
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
	case FilterVerdictAccept:
		exprs = append(exprs, &expr.Verdict{Kind: expr.VerdictAccept})
	case FilterVerdictDrop:
		exprs = append(exprs, &expr.Verdict{Kind: expr.VerdictDrop})
	case FilterVerdictReturn:
		exprs = append(exprs, &expr.Verdict{Kind: expr.VerdictReturn})
	case FilterVerdictJump:
		exprs = append(exprs, &expr.Verdict{Kind: expr.VerdictJump, Chain: rule.JumpTarget})
	}
	return exprs
}

func saddrPayload(is6 bool, register uint32) *expr.Payload {
	if is6 {
		return &expr.Payload{DestRegister: register, Base: expr.PayloadBaseNetworkHeader, Offset: 8, Len: 16}
	}
	return &expr.Payload{DestRegister: register, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4}
}

func ifname(name string) []byte {
	b := make([]byte, 16)
	copy(b, name)
	return b
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

func addDNATRules(c *nftables.Conn, tbl *nftables.Table, pre, out *nftables.Chain, rule HostPortRule, target netip.Addr, allow []netip.Prefix, family int) {
	dests, restricted := rule.DestFor(family == unix.AF_INET6)
	if restricted && len(dests) == 0 {
		return
	}
	// One rule set per destination match: the fib local-address match when
	// the rule is unrestricted, otherwise one literal daddr match per prefix.
	matches := [][]expr.Any{fibLocalExprs()}
	if restricted {
		matches = matches[:0]
		for _, dest := range dests {
			matches = append(matches, addrMatchExprs(dest, false))
		}
	}
	tail := dnatExprs(rule.Protocol, rule.HostPort, target.AsSlice(), rule.TargetPort, family)
	for _, match := range matches {
		if rule.Filtered {
			for _, src := range allow {
				c.AddRule(&nftables.Rule{Table: tbl, Chain: pre, Exprs: concatExprs(addrMatchExprs(src, true), match, tail)})
			}
		} else {
			c.AddRule(&nftables.Rule{Table: tbl, Chain: pre, Exprs: concatExprs(match, tail)})
		}
		c.AddRule(&nftables.Rule{Table: tbl, Chain: out, Exprs: concatExprs(match, tail)})
	}
}

func concatExprs(parts ...[]expr.Any) []expr.Any {
	var out []expr.Any
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

func addrMatchExprs(prefix netip.Prefix, source bool) []expr.Any {
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

// fibLocalExprs: fib daddr type local
//
// The fib address-type match restricts DNAT to traffic addressed to the
// machine itself ("published on the machine's host interfaces"). Rules with a
// destination restriction use addrMatchExprs on the destination instead.
func fibLocalExprs() []expr.Any {
	return []expr.Any{
		&expr.Fib{Register: 1, ResultADDRTYPE: true, FlagDADDR: true},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(unix.RTN_LOCAL)},
	}
}

// dnatExprs: <proto> dport <hostPort> dnat to <target>:<targetPort>
//
// Preceded by a destination match (fibLocalExprs or addrMatchExprs). The rule
// is installed in prerouting for external traffic and output for host-local
// clients.
func dnatExprs(proto byte, hostPort uint16, target []byte, targetPort uint16, family int) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2}, // dport
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(hostPort)},
		&expr.Immediate{Register: 1, Data: target},
		&expr.Immediate{Register: 2, Data: binaryutil.BigEndian.PutUint16(targetPort)},
		&expr.NAT{Type: expr.NATTypeDestNAT, Family: uint32(family), RegAddrMin: 1, RegProtoMin: 2},
	}
}
