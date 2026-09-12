package network

import (
	"net/netip"
	"slices"
	"testing"

	"golang.org/x/sys/unix"
)

func testAttachment(t *testing.T, p Prefix, veth string, spaceID, deploymentID, scheduledInstanceID int32) *ContainerNet {
	t.Helper()
	inbound, err := p.InboundAddr(spaceID, deploymentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	outbound, err := p.OutboundAddr(spaceID, deploymentID, 0, scheduledInstanceID, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, contV4, err := V4Pair(deploymentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return &ContainerNet{
		ContainerID:  veth,
		DeploymentID: deploymentID,
		HostVeth:     veth,
		InboundAddr:  inbound,
		OutboundAddr: outbound,
		V4:           contV4,
	}
}

func chainByName(t *testing.T, chains []FilterChain, name string) FilterChain {
	t.Helper()
	for _, chain := range chains {
		if chain.Name == name {
			return chain
		}
	}
	t.Fatalf("chain %s not rendered", name)
	return FilterChain{}
}

func ruleKeys(family, chain string, rules []FilterRule) []string {
	keys := make([]string, 0, len(rules))
	for _, rule := range rules {
		keys = append(keys, rule.Key(family, chain))
	}
	return keys
}

func dispatchKeys(entries []DispatchElem) []string {
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, DispatchElementKey(entry.Addr, entry.Chain))
	}
	return keys
}

func TestStaticFilterRuleKeys(t *testing.T) {
	want6 := []string{
		"ip6 forward ct established,related accept",
		"ip6 forward oifname @blocked_out counter drop",
		"ip6 forward iifname @managed iifname . saddr != @src_ok counter drop",
		"ip6 forward iifname @build jump build_egress",
		"ip6 forward daddr vmap @dst_dispatch",
	}
	if got := ruleKeys("ip6", "forward", StaticFilterRules6()); !slices.Equal(got, want6) {
		t.Fatalf("v6 static rules = %q, want %q", got, want6)
	}
	want4 := []string{
		"ip forward ct established,related accept",
		"ip forward iifname @managed daddr 10.201.0.0/16 counter drop",
		"ip forward iifname @managed iifname . saddr != @src_ok counter drop",
		"ip forward iifname @build jump build_egress",
	}
	if got := ruleKeys("ip", "forward", StaticFilterRules4()); !slices.Equal(got, want4) {
		t.Fatalf("v4 static rules = %q, want %q", got, want4)
	}
	wantIn6 := []string{
		"ip6 input iifname @build 58 accept",
		"ip6 input iifname @build counter drop",
	}
	if got := ruleKeys("ip6", "input", StaticInputRules6()); !slices.Equal(got, wantIn6) {
		t.Fatalf("v6 input rules = %q, want %q", got, wantIn6)
	}
	wantIn4 := []string{"ip input iifname @build counter drop"}
	if got := ruleKeys("ip", "input", StaticInputRules4()); !slices.Equal(got, wantIn4) {
		t.Fatalf("v4 input rules = %q, want %q", got, wantIn4)
	}
}

func testBuildAttachment(t *testing.T, m *Manager, veth string, buildID string) *ContainerNet {
	t.Helper()
	spec, err := m.BuildNetSpec(buildID)
	if err != nil {
		t.Fatal(err)
	}
	_, contV4, err := V4Pair(BuildDeploymentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return &ContainerNet{
		ContainerID:  spec.ContainerID,
		DeploymentID: spec.DeploymentID,
		HostVeth:     veth,
		InboundAddr:  spec.InboundAddr,
		OutboundAddr: spec.OutboundAddr,
		V4:           contV4,
	}
}

func TestBuildNetSpecUsesReservedIdentity(t *testing.T) {
	p := mustPrefix(t, []byte{0xfd, 0xab, 0xcd, 0xef, 0x01, 0x23})
	m := New(p, 3)
	first, err := m.BuildNetSpec("abc123")
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.BuildNetSpec("def456")
	if err != nil {
		t.Fatal(err)
	}
	if first.DeploymentID != BuildDeploymentID || first.ContainerID != "opendeploy-16777215-abc123" {
		t.Fatalf("spec identity = %d/%s", first.DeploymentID, first.ContainerID)
	}
	if err := validateContainerAddressIdentity(p, BuildDeploymentID, first.InboundAddr, first.OutboundAddr); err != nil {
		t.Fatal(err)
	}
	decoded, err := p.ParseAddr(first.OutboundAddr)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.SpaceID != SystemSpaceID || decoded.DeploymentID != BuildDeploymentID {
		t.Fatalf("outbound identity = %+v", decoded)
	}
	if first.InboundAddr != second.InboundAddr || first.OutboundAddr == second.OutboundAddr {
		t.Fatalf("builds must share the inbound address and differ in outbound: %v/%v %v/%v", first.InboundAddr, second.InboundAddr, first.OutboundAddr, second.OutboundAddr)
	}
	if !IsHostVethName(hostVethName(BuildDeploymentID, 1)) || len(hostVethName(BuildDeploymentID, 1)) > 15 {
		t.Fatalf("build veth name %q is invalid", hostVethName(BuildDeploymentID, 1))
	}
	if _, err := New(Prefix{}, 0).BuildNetSpec("x"); err == nil {
		t.Fatal("expected an error without a cluster prefix")
	}
}

func TestRenderFilterStateBuildAttachment(t *testing.T) {
	p := mustPrefix(t, []byte{0xfd, 0xab, 0xcd, 0xef, 0x01, 0x23})
	m := New(p, 3)
	build := testBuildAttachment(t, m, "od16777215s0", "b1")
	workload := testAttachment(t, p, "od7s0", 5, 7, 12)
	peers := []netip.Addr{netip.MustParseAddr("192.168.104.11"), netip.MustParseAddr("2001:db8::11"), netip.MustParseAddr("192.168.104.11")}
	state := RenderFilterState(p, true, 3, []*ContainerNet{workload, build}, nil, peers)

	if !slices.Equal(state.Build6, []string{"od16777215s0"}) || !slices.Equal(state.Build4, []string{"od16777215s0"}) {
		t.Fatalf("build sets wrong: v6=%v v4=%v", state.Build6, state.Build4)
	}
	if !slices.Contains(state.BlockedOut, "od16777215s0") {
		t.Fatalf("build veth must be in blocked_out: %v", state.BlockedOut)
	}
	if !slices.Contains(state.Managed6, "od16777215s0") || !slices.Contains(state.Managed4, "od16777215s0") {
		t.Fatalf("build veth must be managed: v6=%v v4=%v", state.Managed6, state.Managed4)
	}
	if !slices.Contains(state.SrcOK6, VethAddr{Veth: "od16777215s0", Addr: build.OutboundAddr}) {
		t.Fatalf("build outbound address missing from src_ok6: %v", state.SrcOK6)
	}
	for _, elem := range state.Dispatch {
		if elem.Addr == build.InboundAddr || elem.Addr == build.OutboundAddr {
			t.Fatalf("build addresses must not dispatch: %v", state.Dispatch)
		}
	}
	for _, chain := range state.DstChains {
		if chain.Name == dstChainName(BuildDeploymentID) {
			t.Fatal("build identity must not render a destination chain")
		}
	}
	dns, err := p.InboundAddr(SystemSpaceID, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	want6 := []string{
		"ip6 build_egress daddr " + dns.String() + "/128 udp dport 53 accept",
		"ip6 build_egress daddr " + dns.String() + "/128 tcp dport 53 accept",
		"ip6 build_egress daddr fdab:cdef:123::/48 counter drop",
		"ip6 build_egress daddr fe80::/10 counter drop",
		"ip6 build_egress daddr 2001:db8::11/128 counter drop",
	}
	if got := ruleKeys("ip6", state.BuildEgress6.Name, state.BuildEgress6.Rules); !slices.Equal(got, want6) {
		t.Fatalf("v6 egress rules = %q, want %q", got, want6)
	}
	want4 := []string{
		"ip build_egress daddr 169.254.0.0/16 counter drop",
		"ip build_egress daddr 127.0.0.0/8 counter drop",
		"ip build_egress daddr 192.168.104.11/32 counter drop",
	}
	if got := ruleKeys("ip", state.BuildEgress4.Name, state.BuildEgress4.Rules); !slices.Equal(got, want4) {
		t.Fatalf("v4 egress rules = %q, want %q", got, want4)
	}
	keys := state.ElementKeys()
	if keys[BuildElementKey("ip6", "od16777215s0")] != 1 || keys[BuildElementKey("ip", "od16777215s0")] != 1 {
		t.Fatalf("build element keys missing: %v", keys)
	}
	ruleKeySet := state.RuleKeys()
	for _, want := range append(want6, want4...) {
		if ruleKeySet[want] != 1 {
			t.Fatalf("rule key %q missing from %v", want, ruleKeySet)
		}
	}
}

func TestRenderFilterStateEgressWithoutPrefix(t *testing.T) {
	state := RenderFilterState(Prefix{}, false, 0, nil, nil, []netip.Addr{netip.MustParseAddr("10.0.0.9")})
	if len(state.BuildEgress6.Rules) != 1 || state.BuildEgress6.Rules[0].Daddr != netip.MustParsePrefix("fe80::/10") {
		t.Fatalf("v6 egress without prefix = %v", state.BuildEgress6.Rules)
	}
	if got := ruleKeys("ip", "build_egress", state.BuildEgress4.Rules); got[len(got)-1] != "ip build_egress daddr 10.0.0.9/32 counter drop" {
		t.Fatalf("v4 egress = %q", got)
	}
}

func TestRenderFilterStateUndecodableIdentityFailsClosed(t *testing.T) {
	p := mustPrefix(t, []byte{0xfd, 0xab, 0xcd, 0xef, 0x01, 0x23})
	broken := testAttachment(t, p, "od7s0", 5, 7, 12)
	broken.InboundAddr = netip.MustParseAddr("2001:db8::1")
	state := RenderFilterState(p, true, 0, []*ContainerNet{broken}, nil, nil)

	if !slices.Equal(state.Managed6, []string{"od7s0"}) {
		t.Fatalf("managed6 = %v, want [od7s0]", state.Managed6)
	}
	if !slices.Equal(state.BlockedOut, []string{"od7s0"}) {
		t.Fatalf("blocked_out = %v, want [od7s0]", state.BlockedOut)
	}
	if len(state.SrcOK6) != 0 || len(state.Dispatch) != 0 || len(state.DstChains) != 0 {
		t.Fatalf("undecodable attachment must render no pairs, dispatch, or chains: %+v", state)
	}
	if !slices.Equal(state.Managed4, []string{"od7s0"}) {
		t.Fatalf("managed4 = %v, want [od7s0]", state.Managed4)
	}
}

func TestRenderFilterStateEmptyWithoutAttachments(t *testing.T) {
	p := mustPrefix(t, []byte{0xfd, 0xab, 0xcd, 0xef, 0x01, 0x23})
	state := RenderFilterState(p, true, 0, nil, nil, nil)
	if len(state.ElementKeys()) != 0 || len(state.DstChains) != 0 {
		t.Fatalf("state for empty attachment set is not empty: %+v", state)
	}
}

func TestRenderFilterStateDefaultBoundary(t *testing.T) {
	p := mustPrefix(t, []byte{0xfd, 0xab, 0xcd, 0xef, 0x01, 0x23})
	cn := testAttachment(t, p, "od7s0", 5, 7, 12)
	state := RenderFilterState(p, true, 0, []*ContainerNet{cn}, nil, nil)

	if !slices.Equal(state.Managed6, []string{"od7s0"}) || !slices.Equal(state.Managed4, []string{"od7s0"}) {
		t.Fatalf("managed sets wrong: v6=%v v4=%v", state.Managed6, state.Managed4)
	}
	wantSrcOK6 := []VethAddr{
		{Veth: "od7s0", Addr: cn.InboundAddr},
		{Veth: "od7s0", Addr: cn.OutboundAddr},
	}
	if !slices.Equal(state.SrcOK6, wantSrcOK6) {
		t.Fatalf("src_ok6 = %v, want %v", state.SrcOK6, wantSrcOK6)
	}
	if !slices.Equal(state.SrcOK4, []VethAddr{{Veth: "od7s0", Addr: cn.V4}}) {
		t.Fatalf("src_ok4 = %v", state.SrcOK4)
	}
	wantDispatch := []string{
		DispatchElementKey(cn.InboundAddr, "wl_dst_7"),
		DispatchElementKey(cn.OutboundAddr, "wl_dst_7"),
	}
	slices.Sort(wantDispatch)
	got := dispatchKeys(state.Dispatch)
	slices.Sort(got)
	if !slices.Equal(got, wantDispatch) {
		t.Fatalf("dispatch = %q, want %q", got, wantDispatch)
	}

	dst := chainByName(t, state.DstChains, "wl_dst_7")
	wantDst := []string{
		"ip6 wl_dst_7 saddr fdab:cdef:123:5::/64 accept",
		"ip6 wl_dst_7 saddr fdab:cdef:123::/64 accept",
		"ip6 wl_dst_7 saddr fdab:cdef:123::/48 counter drop",
	}
	if got := ruleKeys("ip6", dst.Name, dst.Rules); !slices.Equal(got, wantDst) {
		t.Fatalf("dst chain rules = %q, want %q", got, wantDst)
	}
}

func TestRenderFilterStateNetproxyAndSystemSpace(t *testing.T) {
	p := mustPrefix(t, []byte{0xfd, 0xab, 0xcd, 0xef, 0x01, 0x23})
	cn := testAttachment(t, p, "od3s0", SystemSpaceID, 3, 9)
	state := RenderFilterState(p, true, 3, []*ContainerNet{cn}, nil, nil)

	dst := chainByName(t, state.DstChains, "wl_dst_3")
	want := []string{
		"ip6 wl_dst_3 udp dport 53 accept",
		"ip6 wl_dst_3 tcp dport 53 accept",
		"ip6 wl_dst_3 saddr fdab:cdef:123::/64 accept",
		"ip6 wl_dst_3 saddr fdab:cdef:123::/48 counter drop",
	}
	if got := ruleKeys("ip6", dst.Name, dst.Rules); !slices.Equal(got, want) {
		t.Fatalf("netproxy dst chain rules = %q, want %q", got, want)
	}
}

func TestRenderFilterStateGlobalSpaceAcceptsAllClusterSources(t *testing.T) {
	p := mustPrefix(t, []byte{0xfd, 0xab, 0xcd, 0xef, 0x01, 0x23})
	cn := testAttachment(t, p, "od9s0", GlobalSpaceID, 9, 4)
	state := RenderFilterState(p, true, 0, []*ContainerNet{cn}, nil, nil)

	dst := chainByName(t, state.DstChains, "wl_dst_9")
	want := []string{"ip6 wl_dst_9 saddr fdab:cdef:123::/48 accept"}
	if got := ruleKeys("ip6", dst.Name, dst.Rules); !slices.Equal(got, want) {
		t.Fatalf("global-space dst chain rules = %q, want %q", got, want)
	}
}

func TestRenderFilterStateRolloverSharesDispatchAndChain(t *testing.T) {
	p := mustPrefix(t, []byte{0xfd, 0xab, 0xcd, 0xef, 0x01, 0x23})
	current := testAttachment(t, p, "od7s0", 5, 7, 12)
	candidate := testAttachment(t, p, "od7s1", 5, 7, 13)
	if current.InboundAddr != candidate.InboundAddr {
		t.Fatal("rollover attachments must share the inbound address")
	}
	if current.OutboundAddr == candidate.OutboundAddr {
		t.Fatal("rollover attachments must have distinct outbound addresses")
	}
	state := RenderFilterState(p, true, 0, []*ContainerNet{candidate, current}, nil, nil)

	if !slices.Equal(state.Managed6, []string{"od7s0", "od7s1"}) {
		t.Fatalf("managed6 = %v", state.Managed6)
	}
	wantSrcOK := []VethAddr{
		{Veth: "od7s0", Addr: current.InboundAddr},
		{Veth: "od7s0", Addr: current.OutboundAddr},
		{Veth: "od7s1", Addr: candidate.InboundAddr},
		{Veth: "od7s1", Addr: candidate.OutboundAddr},
	}
	if !slices.Equal(state.SrcOK6, wantSrcOK) {
		t.Fatalf("src_ok6 = %v, want %v", state.SrcOK6, wantSrcOK)
	}
	if len(state.Dispatch) != 3 {
		t.Fatalf("dispatch entries = %v, want 3 (shared inbound deduplicated)", dispatchKeys(state.Dispatch))
	}
	for _, entry := range state.Dispatch {
		if entry.Chain != "wl_dst_7" {
			t.Fatalf("dispatch entry %v does not target wl_dst_7", entry)
		}
	}
	dstChains := 0
	for _, chain := range state.DstChains {
		if chain.Name == "wl_dst_7" {
			dstChains++
		}
	}
	if dstChains != 1 {
		t.Fatalf("rendered %d wl_dst_7 chains, want 1", dstChains)
	}
}

func TestRenderFilterStateOverrideRules(t *testing.T) {
	p := mustPrefix(t, []byte{0xfd, 0xab, 0xcd, 0xef, 0x01, 0x23})
	cn := testAttachment(t, p, "od7s0", 5, 7, 12)
	other := testAttachment(t, p, "od8s0", 6, 8, 14)
	rules := []PolicyRule{
		{Source: PolicyPeer{SpaceID: 6, DeploymentID: 8}, Destination: PolicyPeer{SpaceID: 5, DeploymentID: 7},
			Ports: []PortMatch{{Protocol: unix.IPPROTO_TCP, Port: 8080}, {Protocol: unix.IPPROTO_UDP, Port: 4000, PortEnd: 4100}}},
		{Source: PolicyPeer{SpaceID: 6}, Destination: PolicyPeer{SpaceID: 5}},
		{Source: PolicyPeer{SpaceID: 6}, Destination: PolicyPeer{SpaceID: 9}},
	}
	state := RenderFilterState(p, true, 0, []*ContainerNet{cn, other}, rules, nil)

	dst := chainByName(t, state.DstChains, "wl_dst_7")
	want := []string{
		"ip6 wl_dst_7 saddr fdab:cdef:123:5::/64 accept",
		"ip6 wl_dst_7 saddr fdab:cdef:123::/64 accept",
		"ip6 wl_dst_7 saddr fdab:cdef:123:6:0:800::/88 tcp dport 8080 accept",
		"ip6 wl_dst_7 saddr fdab:cdef:123:6:0:800::/88 udp dport 4000-4100 accept",
		"ip6 wl_dst_7 saddr fdab:cdef:123:6::/64 accept",
		"ip6 wl_dst_7 saddr fdab:cdef:123::/48 counter drop",
	}
	if got := ruleKeys("ip6", dst.Name, dst.Rules); !slices.Equal(got, want) {
		t.Fatalf("override dst chain rules = %q, want %q", got, want)
	}

	otherDst := chainByName(t, state.DstChains, "wl_dst_8")
	for _, key := range ruleKeys("ip6", otherDst.Name, otherDst.Rules) {
		if key == "ip6 wl_dst_8 saddr fdab:cdef:123:6::/64 accept" {
			continue
		}
		if slices.Contains([]string{"ip6 wl_dst_8 saddr fdab:cdef:123::/64 accept", "ip6 wl_dst_8 saddr fdab:cdef:123::/48 counter drop"}, key) {
			continue
		}
		t.Fatalf("unexpected rule on unrelated destination chain: %s", key)
	}
}

func TestRenderFilterStateWithoutPrefixKeepsV4Boundary(t *testing.T) {
	p := mustPrefix(t, []byte{0xfd, 0xab, 0xcd, 0xef, 0x01, 0x23})
	cn := testAttachment(t, p, "od7s0", 5, 7, 12)
	state := RenderFilterState(Prefix{}, false, 0, []*ContainerNet{cn}, nil, nil)
	if len(state.Managed6) != 0 || len(state.SrcOK6) != 0 || len(state.Dispatch) != 0 || len(state.DstChains) != 0 {
		t.Fatalf("v6 state rendered without prefix: %+v", state)
	}
	if !slices.Equal(state.Managed4, []string{"od7s0"}) || len(state.SrcOK4) != 1 {
		t.Fatalf("v4 elements missing without prefix: %+v", state)
	}
}

func TestRenderFilterStateAttachmentWithoutV4HasNoSrcOK4(t *testing.T) {
	p := mustPrefix(t, []byte{0xfd, 0xab, 0xcd, 0xef, 0x01, 0x23})
	cn := testAttachment(t, p, "od7s0", 5, 7, 12)
	cn.V4 = netip.Addr{}
	state := RenderFilterState(p, true, 0, []*ContainerNet{cn}, nil, nil)
	if !slices.Equal(state.Managed4, []string{"od7s0"}) {
		t.Fatalf("managed4 = %v", state.Managed4)
	}
	if len(state.SrcOK4) != 0 {
		t.Fatalf("src_ok4 = %v, want empty so all v4 from the veth drops", state.SrcOK4)
	}
}

func TestFilterStateElementKeys(t *testing.T) {
	p := mustPrefix(t, []byte{0xfd, 0xab, 0xcd, 0xef, 0x01, 0x23})
	cn := testAttachment(t, p, "od7s0", 5, 7, 12)
	state := RenderFilterState(p, true, 0, []*ContainerNet{cn}, nil, nil)
	keys := state.ElementKeys()
	for _, want := range []string{
		"ip6 set managed od7s0",
		"ip6 set src_ok od7s0 . " + cn.InboundAddr.String(),
		"ip6 set src_ok od7s0 . " + cn.OutboundAddr.String(),
		"ip6 map dst_dispatch " + cn.InboundAddr.String() + " : jump wl_dst_7",
		"ip6 map dst_dispatch " + cn.OutboundAddr.String() + " : jump wl_dst_7",
		"ip set managed od7s0",
		"ip set src_ok od7s0 . " + cn.V4.String(),
	} {
		if keys[want] != 1 {
			t.Fatalf("element key %q missing from %v", want, keys)
		}
	}
	if len(keys) != 7 {
		t.Fatalf("element keys = %d, want 7: %v", len(keys), keys)
	}
}

func TestFilterStateRuleKeysIncludeStatics(t *testing.T) {
	keys := FilterState{}.RuleKeys()
	if keys["ip6 forward daddr vmap @dst_dispatch"] != 1 {
		t.Fatalf("static vmap rule missing: %v", keys)
	}
	if keys["ip forward iifname @managed daddr 10.201.0.0/16 counter drop"] != 1 {
		t.Fatalf("static v4 boundary rule missing: %v", keys)
	}
	if keys["ip6 input iifname @build counter drop"] != 1 {
		t.Fatalf("static input rule missing: %v", keys)
	}
	if len(keys) != len(StaticFilterRules6())+len(StaticFilterRules4())+len(StaticInputRules6())+len(StaticInputRules4()) {
		t.Fatalf("empty state must render only static rules: %v", keys)
	}
}

func TestFilterRuleKeyPortForms(t *testing.T) {
	single := FilterRule{Saddr: netip.MustParsePrefix("fd00::/48"), Protocol: unix.IPPROTO_TCP, Port: 443, Verdict: FilterVerdictAccept}
	if got, want := single.Key("ip6", "c"), "ip6 c saddr fd00::/48 tcp dport 443 accept"; got != want {
		t.Fatalf("key = %q, want %q", got, want)
	}
	ranged := FilterRule{Protocol: unix.IPPROTO_UDP, Port: 100, PortEnd: 200, Counter: true, Verdict: FilterVerdictDrop}
	if got, want := ranged.Key("ip", "c"), "ip c udp dport 100-200 counter drop"; got != want {
		t.Fatalf("key = %q, want %q", got, want)
	}
}
