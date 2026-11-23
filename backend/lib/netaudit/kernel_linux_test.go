//go:build linux

package netaudit

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

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

	for _, filtered := range []bool{false, true} {
		rule := network.HostPortRule{
			Protocol:     unix.IPPROTO_UDP,
			HostPort:     5353,
			TargetPort:   53,
			TargetV6:     cn.InboundAddr,
			TargetV4:     cn.V4,
			SkipLoopback: true,
		}
		if filtered {
			rule.Filtered = true
			rule.AllowV4 = []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}
			rule.AllowV6 = []netip.Prefix{netip.MustParsePrefix("2001:db8::/32")}
		}
		if err := m.ApplyHostPorts(7, cn.ContainerID, []network.HostPortRule{rule}); err != nil {
			t.Fatalf("ApplyHostPorts skipping loopback (filtered=%v): %v", filtered, err)
		}
		requireInSync(t, m, fmt.Sprintf("after loopback-skipping host ports (filtered=%v)", filtered))
	}

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

func TestForwardLeavesHostLoopbackAlone(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root and a linux kernel with nf_tables")
	}
	prefix, err := network.ParsePrefix([]byte{0xfd, 0x11, 0x22, 0x33, 0x44, 0x66})
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
	inbound, err := prefix.InboundAddr(5, 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	outbound, err := prefix.OutboundAddr(5, 8, 0, 12, 1)
	if err != nil {
		t.Fatal(err)
	}
	cn, err := m.SetupContainerNet(network.ContainerNetSpec{ContainerID: "opendeploy-8-loopbacktest", DeploymentID: 8, InboundAddr: inbound, OutboundAddr: outbound})
	if err != nil {
		t.Fatalf("SetupContainerNet: %v", err)
	}
	t.Cleanup(func() { m.TeardownContainerNet(cn) })
	m.SetCurrentNet(8, cn)
	if err := m.Activate(cn); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	for _, listen := range []string{"127.0.0.1:0", "[::1]:0"} {
		listener, err := net.Listen("tcp", listen)
		if err != nil {
			t.Logf("no listener on %s: %v", listen, err)
			continue
		}
		defer listener.Close()
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				io.WriteString(conn, "host service")
				conn.Close()
			}
		}()
		port := uint16(listener.Addr().(*net.TCPAddr).Port)
		reach := func() error {
			conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
			if err != nil {
				return err
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(time.Second))
			body, err := io.ReadAll(conn)
			if err != nil {
				return err
			}
			if string(body) != "host service" {
				return fmt.Errorf("answered by something else: %q", body)
			}
			return nil
		}
		rule := network.HostPortRule{Protocol: unix.IPPROTO_TCP, HostPort: port, TargetPort: 80, TargetV6: cn.InboundAddr, TargetV4: cn.V4}
		if err := m.ApplyHostPorts(8, cn.ContainerID, []network.HostPortRule{rule}); err != nil {
			t.Fatalf("ApplyHostPorts: %v", err)
		}
		t.Logf("%s with a forward that matches loopback: %v", listen, reach())
		rule.SkipLoopback = true
		if err := m.ApplyHostPorts(8, cn.ContainerID, []network.HostPortRule{rule}); err != nil {
			t.Fatalf("ApplyHostPorts skipping loopback: %v", err)
		}
		if err := reach(); err != nil {
			t.Fatalf("%s is not reachable beside a forward on its port: %v", listen, err)
		}
	}
}
