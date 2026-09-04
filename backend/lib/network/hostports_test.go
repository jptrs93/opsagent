package network

import (
	"net/netip"
	"testing"

	"golang.org/x/sys/unix"
)

func TestReconcileNetproxyHostPortsGroupsPublishSetByPort(t *testing.T) {
	m := New(GeneratePrefix(), 42)
	m.netproxyPublish = []IngressPublish{
		{Address: netip.MustParseAddr("203.0.113.10"), Port: 443},
		{Address: netip.MustParseAddr("2001:db8::10"), Port: 443},
		{Address: netip.MustParseAddr("203.0.113.10"), Port: 80},
		{Port: 8443},
		{Address: netip.MustParseAddr("203.0.113.11"), Port: 8443},
	}
	m.reconcileNetproxyHostPortsLocked()
	if _, ok := m.hostPorts[42]; ok {
		t.Fatal("netproxy host ports were recorded before its network was recovered")
	}

	m.current[42] = &ContainerNet{
		ContainerID:  "netproxy-run",
		InboundAddr:  netip.MustParseAddr("fd00::42"),
		OutboundAddr: netip.MustParseAddr("fd00::99"),
		V4:           netip.MustParseAddr("100.64.0.2"),
	}
	m.reconcileNetproxyHostPortsLocked()

	entry, ok := m.hostPorts[42]
	if !ok {
		t.Fatal("netproxy host ports were not recorded")
	}
	if entry.owner != "netproxy-run" || len(entry.rules) != 3 {
		t.Fatalf("entry = %+v, want netproxy owner with three rules", entry)
	}
	for i, port := range []uint16{80, 443, 8443} {
		rule := entry.rules[i]
		if rule.Protocol != unix.IPPROTO_TCP || rule.HostPort != port || rule.TargetPort != port || rule.TargetV6 != m.current[42].InboundAddr || rule.TargetV4 != m.current[42].V4 {
			t.Fatalf("rule[%d] = %+v, want TCP forwarding for %d", i, rule, port)
		}
	}
	if got := entry.rules[0].Dest; len(got) != 1 || got[0] != netip.MustParsePrefix("203.0.113.10/32") {
		t.Fatalf("port 80 dest = %v, want the single IPv4 literal", got)
	}
	if got := entry.rules[1].Dest; len(got) != 2 || got[0] != netip.MustParsePrefix("203.0.113.10/32") || got[1] != netip.MustParsePrefix("2001:db8::10/128") {
		t.Fatalf("port 443 dest = %v, want both literals sorted", got)
	}
	// A wildcard entry widens the whole port; the literal beside it is moot.
	if got := entry.rules[2].Dest; len(got) != 0 {
		t.Fatalf("port 8443 dest = %v, want unrestricted", got)
	}
	v6, restricted := entry.rules[0].DestFor(true)
	if !restricted || len(v6) != 0 {
		t.Fatalf("port 80 IPv6 dests = %v restricted=%v, want restricted with none", v6, restricted)
	}
}

func TestSetNetproxyPublishClearsRulesWhenEmpty(t *testing.T) {
	m := New(GeneratePrefix(), 42)
	m.current[42] = &ContainerNet{ContainerID: "netproxy-run", InboundAddr: netip.MustParseAddr("fd00::42"), V4: netip.MustParseAddr("100.64.0.2")}
	if err := m.SetNetproxyPublish([]IngressPublish{{Port: 443}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.hostPorts[42]; !ok {
		t.Fatal("publish set was not applied")
	}
	if err := m.SetNetproxyPublish(nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.hostPorts[42]; ok {
		t.Fatal("an empty publish set must remove the netproxy rules")
	}
}
