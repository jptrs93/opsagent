package network

import (
	"net/netip"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"golang.org/x/sys/unix"
)

func TestReconcileNetproxyHostPortsUsesRenderedIngress(t *testing.T) {
	m := New(GeneratePrefix(), 42)
	m.netproxyIngressPorts = map[uint16]struct{}{443: {}, 8443: {}}
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
	if entry.owner != "netproxy-run" || len(entry.rules) != 2 {
		t.Fatalf("entry = %+v, want netproxy owner with two rules", entry)
	}
	for i, port := range []uint16{443, 8443} {
		rule := entry.rules[i]
		if rule.Protocol != unix.IPPROTO_TCP || rule.HostPort != port || rule.TargetPort != port || rule.TargetV6 != m.current[42].InboundAddr || rule.TargetV4 != m.current[42].V4 {
			t.Fatalf("rule[%d] = %+v, want TCP forwarding for %d", i, rule, port)
		}
	}
}

func TestSetNetproxyIngressFiltersUnsupportedRoutes(t *testing.T) {
	ports := netproxyIngressPorts([]*apigen.NetIngress{
		{Kind: apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH, TlsPassthrough: &apigen.TlsPassthroughNetIngress{HostPort: 8443}},
		{Kind: apigen.IngressKind_INGRESS_KIND_UNSPECIFIED, TlsPassthrough: &apigen.TlsPassthroughNetIngress{HostPort: 9443}},
		{Kind: apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH, TlsPassthrough: &apigen.TlsPassthroughNetIngress{}},
	})
	if len(ports) != 1 {
		t.Fatalf("ports = %+v, want only 8443", ports)
	}
}
