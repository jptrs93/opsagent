package network

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	SystemSpaceID int32  = 0
	GlobalSpaceID int32  = 1
	DNSPort       uint16 = 53
)

const (
	NftSetManaged     = "managed"
	NftSetSrcOK       = "src_ok"
	NftSetBlockedOut  = "blocked_out"
	NftMapDstDispatch = "dst_dispatch"
)

type PolicyPeer struct {
	SpaceID      int32
	DeploymentID int32
}

type PortMatch struct {
	Protocol uint8
	Port     uint16
	PortEnd  uint16
}

type PolicyRule struct {
	Source      PolicyPeer
	Destination PolicyPeer
	Ports       []PortMatch
}

type FilterVerdict string

const (
	FilterVerdictAccept FilterVerdict = "accept"
	FilterVerdictDrop   FilterVerdict = "drop"
	FilterVerdictReturn FilterVerdict = "return"
	FilterVerdictJump   FilterVerdict = "jump"
)

type FilterRule struct {
	CtEstablished bool
	IifName       string
	OifName       string
	IifSet        string
	OifSet        string
	SrcPairSet    string
	SrcPairInvert bool
	DaddrVmap     string
	Saddr         netip.Prefix
	Daddr         netip.Prefix
	Protocol      uint8
	Port          uint16
	PortEnd       uint16
	Counter       bool
	Verdict       FilterVerdict
	JumpTarget    string
}

type FilterChain struct {
	Name  string
	Rules []FilterRule
}

type VethAddr struct {
	Veth string
	Addr netip.Addr
}

type DispatchElem struct {
	Addr  netip.Addr
	Chain string
}

type FilterState struct {
	Managed6   []string
	SrcOK6     []VethAddr
	BlockedOut []string
	Dispatch   []DispatchElem
	DstChains  []FilterChain
	Managed4   []string
	SrcOK4     []VethAddr
}

func dstChainName(deploymentID int32) string {
	return "wl_dst_" + strconv.Itoa(int(deploymentID))
}

func StaticFilterRules6() []FilterRule {
	return []FilterRule{
		{CtEstablished: true, Verdict: FilterVerdictAccept},
		{OifSet: NftSetBlockedOut, Counter: true, Verdict: FilterVerdictDrop},
		{IifSet: NftSetManaged, SrcPairSet: NftSetSrcOK, SrcPairInvert: true, Counter: true, Verdict: FilterVerdictDrop},
		{DaddrVmap: NftMapDstDispatch},
	}
}

func StaticFilterRules4() []FilterRule {
	return []FilterRule{
		{CtEstablished: true, Verdict: FilterVerdictAccept},
		{IifSet: NftSetManaged, Daddr: V4CIDR, Counter: true, Verdict: FilterVerdictDrop},
		{IifSet: NftSetManaged, SrcPairSet: NftSetSrcOK, SrcPairInvert: true, Counter: true, Verdict: FilterVerdictDrop},
	}
}

func RenderFilterState(prefix Prefix, hasPrefix bool, netproxyDeploymentID int32, nets []*ContainerNet, rules []PolicyRule) FilterState {
	attachments := make([]*ContainerNet, 0, len(nets))
	for _, cn := range nets {
		if cn != nil && cn.HostVeth != "" {
			attachments = append(attachments, cn)
		}
	}
	slices.SortFunc(attachments, func(a, b *ContainerNet) int { return strings.Compare(a.HostVeth, b.HostVeth) })

	var state FilterState
	for _, cn := range attachments {
		state.Managed4 = append(state.Managed4, cn.HostVeth)
		if cn.V4.Is4() {
			state.SrcOK4 = append(state.SrcOK4, VethAddr{Veth: cn.HostVeth, Addr: cn.V4})
		}
	}

	if !hasPrefix {
		return state
	}

	dispatch := map[netip.Addr]string{}
	spaceByDeployment := map[int32]int32{}
	for _, cn := range attachments {
		state.Managed6 = append(state.Managed6, cn.HostVeth)
		decoded, err := prefix.ParseAddr(cn.InboundAddr)
		if err != nil {
			state.BlockedOut = append(state.BlockedOut, cn.HostVeth)
			continue
		}
		state.SrcOK6 = append(state.SrcOK6,
			VethAddr{Veth: cn.HostVeth, Addr: cn.InboundAddr},
			VethAddr{Veth: cn.HostVeth, Addr: cn.OutboundAddr},
		)
		chain := dstChainName(cn.DeploymentID)
		dispatch[cn.InboundAddr] = chain
		dispatch[cn.OutboundAddr] = chain
		spaceByDeployment[cn.DeploymentID] = decoded.SpaceID
	}

	addrs := make([]netip.Addr, 0, len(dispatch))
	for addr := range dispatch {
		addrs = append(addrs, addr)
	}
	slices.SortFunc(addrs, func(a, b netip.Addr) int { return a.Compare(b) })
	for _, addr := range addrs {
		state.Dispatch = append(state.Dispatch, DispatchElem{Addr: addr, Chain: dispatch[addr]})
	}

	deployments := make([]int32, 0, len(spaceByDeployment))
	for id := range spaceByDeployment {
		deployments = append(deployments, id)
	}
	slices.Sort(deployments)
	for _, deploymentID := range deployments {
		spaceID := spaceByDeployment[deploymentID]
		chain := FilterChain{Name: dstChainName(deploymentID)}
		if deploymentID == netproxyDeploymentID {
			chain.Rules = append(chain.Rules,
				FilterRule{Protocol: unix.IPPROTO_UDP, Port: DNSPort, Verdict: FilterVerdictAccept},
				FilterRule{Protocol: unix.IPPROTO_TCP, Port: DNSPort, Verdict: FilterVerdictAccept},
			)
		}
		if spaceID == GlobalSpaceID {
			chain.Rules = append(chain.Rules, FilterRule{Saddr: prefix.CIDR(), Verdict: FilterVerdictAccept})
			state.DstChains = append(state.DstChains, chain)
			continue
		}
		if sameSpace, err := prefix.SpaceCIDR(spaceID); err == nil {
			chain.Rules = append(chain.Rules, FilterRule{Saddr: sameSpace, Verdict: FilterVerdictAccept})
		}
		if spaceID != SystemSpaceID {
			if system, err := prefix.SpaceCIDR(SystemSpaceID); err == nil {
				chain.Rules = append(chain.Rules, FilterRule{Saddr: system, Verdict: FilterVerdictAccept})
			}
		}
		chain.Rules = append(chain.Rules, overrideFilterRules(prefix, deploymentID, spaceID, rules)...)
		chain.Rules = append(chain.Rules, FilterRule{Saddr: prefix.CIDR(), Counter: true, Verdict: FilterVerdictDrop})
		state.DstChains = append(state.DstChains, chain)
	}
	return state
}

func overrideFilterRules(prefix Prefix, deploymentID, spaceID int32, rules []PolicyRule) []FilterRule {
	var out []FilterRule
	for _, rule := range rules {
		if rule.Destination.SpaceID != spaceID {
			continue
		}
		if rule.Destination.DeploymentID != 0 && rule.Destination.DeploymentID != deploymentID {
			continue
		}
		var source netip.Prefix
		var err error
		if rule.Source.DeploymentID == 0 {
			source, err = prefix.SpaceCIDR(rule.Source.SpaceID)
		} else {
			source, err = prefix.DeploymentCIDR(rule.Source.SpaceID, rule.Source.DeploymentID)
		}
		if err != nil {
			continue
		}
		if len(rule.Ports) == 0 {
			out = append(out, FilterRule{Saddr: source, Verdict: FilterVerdictAccept})
			continue
		}
		for _, port := range rule.Ports {
			out = append(out, FilterRule{Saddr: source, Protocol: port.Protocol, Port: port.Port, PortEnd: port.PortEnd, Verdict: FilterVerdictAccept})
		}
	}
	slices.SortFunc(out, func(a, b FilterRule) int {
		if c := strings.Compare(a.Saddr.String(), b.Saddr.String()); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Protocol, b.Protocol); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Port, b.Port); c != 0 {
			return c
		}
		return cmp.Compare(a.PortEnd, b.PortEnd)
	})
	return slices.CompactFunc(out, func(a, b FilterRule) bool { return a == b })
}

func filterProtoName(proto uint8) string {
	switch proto {
	case unix.IPPROTO_TCP:
		return "tcp"
	case unix.IPPROTO_UDP:
		return "udp"
	}
	return strconv.Itoa(int(proto))
}

func (r FilterRule) Key(family, chain string) string {
	parts := []string{family, chain}
	if r.CtEstablished {
		parts = append(parts, "ct established,related")
	}
	if r.IifName != "" {
		parts = append(parts, "iifname "+r.IifName)
	}
	if r.IifSet != "" {
		parts = append(parts, "iifname @"+r.IifSet)
	}
	if r.OifName != "" {
		parts = append(parts, "oifname "+r.OifName)
	}
	if r.OifSet != "" {
		parts = append(parts, "oifname @"+r.OifSet)
	}
	if r.SrcPairSet != "" {
		if r.SrcPairInvert {
			parts = append(parts, "iifname . saddr != @"+r.SrcPairSet)
		} else {
			parts = append(parts, "iifname . saddr @"+r.SrcPairSet)
		}
	}
	if r.Saddr.IsValid() {
		parts = append(parts, "saddr "+r.Saddr.String())
	}
	if r.Daddr.IsValid() {
		parts = append(parts, "daddr "+r.Daddr.String())
	}
	if r.DaddrVmap != "" {
		parts = append(parts, "daddr vmap @"+r.DaddrVmap)
	}
	if r.Protocol != 0 {
		if r.Port != 0 {
			if r.PortEnd != 0 {
				parts = append(parts, fmt.Sprintf("%s dport %d-%d", filterProtoName(r.Protocol), r.Port, r.PortEnd))
			} else {
				parts = append(parts, fmt.Sprintf("%s dport %d", filterProtoName(r.Protocol), r.Port))
			}
		} else {
			parts = append(parts, filterProtoName(r.Protocol))
		}
	}
	if r.Counter {
		parts = append(parts, "counter")
	}
	switch {
	case r.Verdict == FilterVerdictJump:
		parts = append(parts, "jump "+r.JumpTarget)
	case r.Verdict != "":
		parts = append(parts, string(r.Verdict))
	}
	return strings.Join(parts, " ")
}

func ManagedElementKey(family, veth string) string {
	return family + " set " + NftSetManaged + " " + veth
}

func SrcOKElementKey(family, veth string, addr netip.Addr) string {
	return family + " set " + NftSetSrcOK + " " + veth + " . " + addr.String()
}

func BlockedOutElementKey(veth string) string {
	return "ip6 set " + NftSetBlockedOut + " " + veth
}

func DispatchElementKey(addr netip.Addr, chain string) string {
	return "ip6 map " + NftMapDstDispatch + " " + addr.String() + " : jump " + chain
}

func (s FilterState) RuleKeys() map[string]int {
	keys := map[string]int{}
	for _, rule := range StaticFilterRules6() {
		keys[rule.Key("ip6", "forward")]++
	}
	for _, rule := range StaticFilterRules4() {
		keys[rule.Key("ip", "forward")]++
	}
	for _, chain := range s.DstChains {
		for _, rule := range chain.Rules {
			keys[rule.Key("ip6", chain.Name)]++
		}
	}
	return keys
}

func (s FilterState) ElementKeys() map[string]int {
	keys := map[string]int{}
	for _, veth := range s.Managed6 {
		keys[ManagedElementKey("ip6", veth)]++
	}
	for _, pair := range s.SrcOK6 {
		keys[SrcOKElementKey("ip6", pair.Veth, pair.Addr)]++
	}
	for _, veth := range s.BlockedOut {
		keys[BlockedOutElementKey(veth)]++
	}
	for _, elem := range s.Dispatch {
		keys[DispatchElementKey(elem.Addr, elem.Chain)]++
	}
	for _, veth := range s.Managed4 {
		keys[ManagedElementKey("ip", veth)]++
	}
	for _, pair := range s.SrcOK4 {
		keys[SrcOKElementKey("ip", pair.Veth, pair.Addr)]++
	}
	return keys
}
