//go:build linux

package netaudit

import (
	"errors"
	"net/netip"
	"os"
	"testing"

	"github.com/jptrs93/opsagent/backend/lib/network"
	"golang.org/x/sys/unix"
)

func requireInSync(t *testing.T, m *network.Manager, step string) {
	t.Helper()
	diff, err := collectAndCompare(m)
	if err != nil {
		t.Fatalf("%s: reading kernel state: %v", step, err)
	}
	if !diff.InSync() {
		t.Fatalf("%s: kernel diverged from desired: %+v", step, diff)
	}
}

func TestKernelRoundTrip(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root and a linux kernel with nf_tables")
	}
	prefix, err := network.ParsePrefix([]byte{0xfd, 0x11, 0x22, 0x33, 0x44, 0x55})
	if err != nil {
		t.Fatal(err)
	}
	m := network.New(prefix, 0)
	if err := m.EnsureBase(); err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EROFS) {
			t.Skipf("kernel networking not permitted: %v", err)
		}
		t.Fatalf("EnsureBase: %v", err)
	}
	requireInSync(t, m, "after EnsureBase")

	inbound, err := prefix.InboundAddr(5, 7, 0)
	if err != nil {
		t.Fatal(err)
	}
	outbound, err := prefix.OutboundAddr(5, 7, 0, 12, 1)
	if err != nil {
		t.Fatal(err)
	}
	cn, err := m.SetupContainerNet(network.ContainerNetSpec{
		ContainerID:  "opendeploy-7-kerneltest",
		DeploymentID: 7,
		InboundAddr:  inbound,
		OutboundAddr: outbound,
	})
	if err != nil {
		t.Fatalf("SetupContainerNet: %v", err)
	}
	t.Cleanup(func() { m.TeardownContainerNet(cn) })
	requireInSync(t, m, "after container setup")

	m.SetCurrentNet(7, cn)
	if err := m.Activate(cn); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	requireInSync(t, m, "after activation")

	if err := m.ApplyHostPorts(7, cn.ContainerID, []network.HostPortRule{{
		Protocol:   unix.IPPROTO_TCP,
		HostPort:   8443,
		TargetPort: 443,
		TargetV6:   cn.InboundAddr,
		TargetV4:   cn.V4,
	}}); err != nil {
		t.Fatalf("ApplyHostPorts: %v", err)
	}
	requireInSync(t, m, "after host ports")

	if err := m.ApplyHostPorts(7, cn.ContainerID, []network.HostPortRule{{
		Protocol:   unix.IPPROTO_TCP,
		HostPort:   8443,
		TargetPort: 443,
		TargetV6:   cn.InboundAddr,
		TargetV4:   cn.V4,
		Filtered:   true,
		AllowV4:    []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")},
		AllowV6:    []netip.Prefix{netip.MustParsePrefix("2001:db8::/32")},
	}}); err != nil {
		t.Fatalf("ApplyHostPorts filtered: %v", err)
	}
	requireInSync(t, m, "after filtered host ports")

	if err := m.SetPolicyRules([]network.PolicyRule{{
		Source:      network.PolicyPeer{SpaceID: 6},
		Destination: network.PolicyPeer{SpaceID: 5},
		Ports:       []network.PortMatch{{Protocol: unix.IPPROTO_TCP, Port: 8080}},
	}}); err != nil {
		t.Fatalf("SetPolicyRules: %v", err)
	}
	requireInSync(t, m, "after policy rules")

	if err := m.ClearHostPorts(7, cn.ContainerID); err != nil {
		t.Fatalf("ClearHostPorts: %v", err)
	}
	requireInSync(t, m, "after clearing host ports")

	m.SetCurrentNet(7, nil)
	m.TeardownContainerNet(cn)
	requireInSync(t, m, "after teardown")
}
