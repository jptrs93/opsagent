//go:build linux

package network

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

type fakeRouteOperations struct {
	routes       []netlink.Route
	replaced     []netlink.Route
	deleted      []netlink.Route
	listFamily   int
	listFilter   netlink.Route
	listMask     uint64
	listErr      error
	deleteErr    error
	replaceError error
}

func (f *fakeRouteOperations) RouteReplace(route *netlink.Route) error {
	f.replaced = append(f.replaced, *route)
	return f.replaceError
}

func (f *fakeRouteOperations) RouteListFiltered(family int, filter *netlink.Route, filterMask uint64) ([]netlink.Route, error) {
	f.listFamily = family
	f.listFilter = *filter
	f.listMask = filterMask
	return f.routes, f.listErr
}

func (f *fakeRouteOperations) RouteDel(route *netlink.Route) error {
	f.deleted = append(f.deleted, *route)
	return f.deleteErr
}

func TestWorkloadRouteConstructors(t *testing.T) {
	prefix := GeneratePrefix()
	addr, err := prefix.InboundAddr(12, 34, 0)
	if err != nil {
		t.Fatal(err)
	}

	for name, route := range map[string]netlink.Route{
		"local":  localWorkloadRoute(addr, 10),
		"remote": remoteWorkloadRoute(addr, 11),
	} {
		t.Run(name, func(t *testing.T) {
			destination, ok := netlinkRoutePrefix(route)
			if !ok || destination != netip.PrefixFrom(addr, 128) {
				t.Fatalf("destination = %v, want %s/128", destination, addr)
			}
			if route.Protocol != routeProtocolOpenDeploy || route.Table != unix.RT_TABLE_MAIN || route.Type != unix.RTN_UNICAST {
				t.Fatalf("unexpected route ownership: %+v", route)
			}
		})
	}
}

func TestClusterFallbackRouteConstructor(t *testing.T) {
	prefix := GeneratePrefix()
	route := clusterFallbackRoute(prefix)
	destination, ok := netlinkRoutePrefix(route)
	if !ok || destination != prefix.CIDR() {
		t.Fatalf("destination = %v, want %v", destination, prefix.CIDR())
	}
	if route.Protocol != routeProtocolOpenDeploy || route.Table != unix.RT_TABLE_MAIN || route.Type != unix.RTN_UNREACHABLE {
		t.Fatalf("unexpected fallback ownership: %+v", route)
	}
}

func TestTunnelIdentityAndTopologyValidation(t *testing.T) {
	if got := tunnelName(12345); got != "odt9ix" {
		t.Fatalf("tunnel name = %q, want odt9ix", got)
	}
	if got := tunnelAlias(12345); got != "opendeploy:tunnel:12345" {
		t.Fatalf("tunnel alias = %q", got)
	}
	if nodeID, ok := ownedTunnelNodeID(&netlink.Sittun{LinkAttrs: netlink.LinkAttrs{Name: tunnelName(2), Alias: tunnelAlias(2)}}); !ok || nodeID != 2 {
		t.Fatalf("owned SIT tunnel = (%d, %t), want (2, true)", nodeID, ok)
	}
	if _, ok := ownedTunnelNodeID(&netlink.Sittun{LinkAttrs: netlink.LinkAttrs{Name: tunnelName(2)}}); ok {
		t.Fatal("unaliased tunnel was accepted as owned")
	}
	prefix := GeneratePrefix()
	addr, err := prefix.InboundAddr(1, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	topology := Topology{
		Prefix:      prefix,
		LocalNodeID: 1,
		Tunnels: []Tunnel{{
			NodeID: 2,
			Local:  netip.MustParseAddr("192.0.2.1"),
			Remote: netip.MustParseAddr("192.0.2.2"),
		}},
		Routes: []RemoteRoute{{Addr: addr, NodeID: 2}},
	}
	if err := validateTopology(topology); err != nil {
		t.Fatal(err)
	}
	topology.Tunnels[0].Remote = netip.MustParseAddr("2001:db8::2")
	if err := validateTopology(topology); err == nil {
		t.Fatal("mixed tunnel families were accepted")
	}
}

func TestReconcileLocalWorkloadRouteDeletesOnlyExactLegacyRoute(t *testing.T) {
	prefix := GeneratePrefix()
	addr, err := prefix.InboundAddr(12, 34, 0)
	if err != nil {
		t.Fatal(err)
	}
	otherAddr, err := prefix.InboundAddr(12, 35, 0)
	if err != nil {
		t.Fatal(err)
	}
	exact := localWorkloadRoute(addr, 10)
	exact.Protocol = legacyRouteProtocolOpenDeploy
	wrongProtocol := exact
	wrongProtocol.Protocol = routeProtocolOpenDeploy
	wrongTable := exact
	wrongTable.Table = 100
	wrongType := exact
	wrongType.Type = unix.RTN_UNREACHABLE
	wrongLink := exact
	wrongLink.LinkIndex = 11
	wrongDestination := exact
	wrongDestination.Dst = netipPrefixToIPNet(netip.PrefixFrom(otherAddr, 128))

	ops := &fakeRouteOperations{routes: []netlink.Route{
		exact, wrongProtocol, wrongTable, wrongType, wrongLink, wrongDestination,
	}}
	if err := reconcileLocalWorkloadRouteWithOps(ops, prefix, addr, 10); err != nil {
		t.Fatal(err)
	}
	if len(ops.replaced) != 1 || ops.replaced[0].Protocol != routeProtocolOpenDeploy {
		t.Fatalf("replacement routes = %+v", ops.replaced)
	}
	if len(ops.deleted) != 1 || !ops.deleted[0].Equal(exact) {
		t.Fatalf("deleted routes = %+v, want exact legacy route", ops.deleted)
	}
	assertLegacyRouteListFilter(t, ops)
}

func TestReconcileClusterFallbackDeletesOnlyExactLegacyRoute(t *testing.T) {
	prefix := GeneratePrefix()
	exact := clusterFallbackRoute(prefix)
	exact.Protocol = legacyRouteProtocolOpenDeploy
	// Linux reports IPv6 unreachable routes with the loopback ifindex even
	// when the route was installed without an explicit output interface.
	exact.LinkIndex = 1
	wrongProtocol := exact
	wrongProtocol.Protocol = routeProtocolOpenDeploy
	wrongType := exact
	wrongType.Type = unix.RTN_UNICAST

	ops := &fakeRouteOperations{routes: []netlink.Route{exact, wrongProtocol, wrongType}}
	if err := reconcileClusterFallbackRouteWithOps(ops, prefix); err != nil {
		t.Fatal(err)
	}
	if len(ops.deleted) != 1 || !ops.deleted[0].Equal(exact) {
		t.Fatalf("deleted routes = %+v, want exact legacy fallback", ops.deleted)
	}
	assertLegacyRouteListFilter(t, ops)
}

func TestReconcileLocalWorkloadRouteRejectsAddressOutsideCluster(t *testing.T) {
	prefix := GeneratePrefix()
	otherPrefix := GeneratePrefix()
	addr, err := otherPrefix.InboundAddr(12, 34, 0)
	if err != nil {
		t.Fatal(err)
	}
	ops := &fakeRouteOperations{}
	if err := reconcileLocalWorkloadRouteWithOps(ops, prefix, addr, 10); err == nil {
		t.Fatal("out-of-prefix address was accepted")
	}
	if len(ops.replaced) != 0 {
		t.Fatalf("out-of-prefix route was installed: %+v", ops.replaced)
	}
}

func TestRouteReconciliationPropagatesOperationErrors(t *testing.T) {
	prefix := GeneratePrefix()
	addr, err := prefix.InboundAddr(12, 34, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("netlink failed")

	t.Run("replace", func(t *testing.T) {
		ops := &fakeRouteOperations{replaceError: wantErr}
		if err := reconcileLocalWorkloadRouteWithOps(ops, prefix, addr, 10); !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
	})
	t.Run("list", func(t *testing.T) {
		ops := &fakeRouteOperations{listErr: wantErr}
		if err := reconcileLocalWorkloadRouteWithOps(ops, prefix, addr, 10); !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
	})
	t.Run("delete", func(t *testing.T) {
		legacy := localWorkloadRoute(addr, 10)
		legacy.Protocol = legacyRouteProtocolOpenDeploy
		ops := &fakeRouteOperations{routes: []netlink.Route{legacy}, deleteErr: wantErr}
		if err := reconcileLocalWorkloadRouteWithOps(ops, prefix, addr, 10); !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
	})
	t.Run("already deleted", func(t *testing.T) {
		legacy := localWorkloadRoute(addr, 10)
		legacy.Protocol = legacyRouteProtocolOpenDeploy
		ops := &fakeRouteOperations{routes: []netlink.Route{legacy}, deleteErr: unix.ESRCH}
		if err := reconcileLocalWorkloadRouteWithOps(ops, prefix, addr, 10); err != nil {
			t.Fatalf("error = %v, want successful convergence", err)
		}
	})
}

func assertLegacyRouteListFilter(t *testing.T, ops *fakeRouteOperations) {
	t.Helper()
	if ops.listFamily != netlink.FAMILY_V6 ||
		ops.listFilter.Protocol != legacyRouteProtocolOpenDeploy ||
		ops.listFilter.Table != unix.RT_TABLE_MAIN ||
		ops.listMask != netlink.RT_FILTER_PROTOCOL|netlink.RT_FILTER_TABLE {
		t.Fatalf("unexpected route list filter: family=%d filter=%+v mask=%d", ops.listFamily, ops.listFilter, ops.listMask)
	}
}
