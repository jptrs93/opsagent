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
	listErr      error
	replaceError error
}

func (f *fakeRouteOperations) RouteReplace(route *netlink.Route) error {
	f.replaced = append(f.replaced, *route)
	return f.replaceError
}

func (f *fakeRouteOperations) RouteListFiltered(family int, filter *netlink.Route, filterMask uint64) ([]netlink.Route, error) {
	return f.routes, f.listErr
}

func TestWorkloadRouteConstructors(t *testing.T) {
	prefix := GeneratePrefix()
	addr, err := prefix.InboundAddr(12, 34, 0)
	if err != nil {
		t.Fatal(err)
	}

	for name, route := range map[string]netlink.Route{
		"local":  localWorkloadRoute(addr, 10),
		"remote": remoteWorkloadRoute(netip.PrefixFrom(addr, 128), 11),
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

func TestTopologyValidationRequiresWireGuardTransport(t *testing.T) {
	prefix := GeneratePrefix()
	addr, err := prefix.InboundAddr(1, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	topology := Topology{
		Prefix:      prefix,
		LocalNodeID: 1,
		LocalWGPort: 51833,
		Peers: []Peer{{
			NodeID:   2,
			Endpoint: netip.MustParseAddr("192.0.2.2"),
			WGKey:    "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI=",
			WGPort:   51833,
		}},
		Routes: []RemoteRoute{{Prefix: netip.PrefixFrom(addr, 128), NodeID: 2}},
	}
	if err := validateTopology(topology); err != nil {
		t.Fatal(err)
	}
	keyless := topology
	keyless.Peers = []Peer{{NodeID: 2, Endpoint: topology.Peers[0].Endpoint, WGPort: 51833}}
	if err := validateTopology(keyless); err == nil {
		t.Fatal("keyless peer was accepted")
	}
	portless := topology
	portless.Peers = []Peer{{NodeID: 2, Endpoint: topology.Peers[0].Endpoint, WGKey: topology.Peers[0].WGKey}}
	if err := validateTopology(portless); err == nil {
		t.Fatal("peer without listen port was accepted")
	}
	noLocalPort := topology
	noLocalPort.LocalWGPort = 0
	if err := validateTopology(noLocalPort); err == nil {
		t.Fatal("topology without local listen port was accepted")
	}
	unroutedPeer := topology
	unroutedPeer.Routes = []RemoteRoute{{Prefix: netip.PrefixFrom(addr, 128), NodeID: 3}}
	if err := validateTopology(unroutedPeer); err == nil {
		t.Fatal("route referencing an unknown peer was accepted")
	}
}

func TestReconcileLocalWorkloadRouteInstallsOwnedRoute(t *testing.T) {
	prefix := GeneratePrefix()
	addr, err := prefix.InboundAddr(12, 34, 0)
	if err != nil {
		t.Fatal(err)
	}
	ops := &fakeRouteOperations{}
	if err := reconcileLocalWorkloadRouteWithOps(ops, prefix, addr, 10); err != nil {
		t.Fatal(err)
	}
	if len(ops.replaced) != 1 || ops.replaced[0].Protocol != routeProtocolOpenDeploy {
		t.Fatalf("replacement routes = %+v", ops.replaced)
	}
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

	ops := &fakeRouteOperations{replaceError: wantErr}
	if err := reconcileLocalWorkloadRouteWithOps(ops, prefix, addr, 10); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}
