//go:build linux

package network

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	tunnelAliasPrefix = "opendeploy:tunnel:"
	tunnelMTU         = 1420
)

// ReconcileTopology converges fixed protocol-41 tunnels and remote workload
// routes without touching locally attached workload routes or foreign links.
func (m *Manager) ReconcileTopology(topology Topology) error {
	if err := validateTopology(topology); err != nil {
		return err
	}
	if err := m.EnsureBase(); err != nil {
		return err
	}

	desiredTunnels := make(map[int32]Tunnel, len(topology.Tunnels))
	for _, tunnel := range topology.Tunnels {
		desiredTunnels[tunnel.NodeID] = tunnel
	}
	owned, err := listOwnedTunnels()
	if err != nil {
		return err
	}
	for nodeID, desired := range desiredTunnels {
		if current, ok := owned[nodeID]; ok && !tunnelMatches(current, desired) {
			if err := netlink.LinkDel(current); err != nil && !errors.Is(err, unix.ESRCH) {
				return fmt.Errorf("replacing tunnel for node %d: %w", nodeID, err)
			}
			delete(owned, nodeID)
		}
		if _, ok := owned[nodeID]; !ok {
			link, err := addTunnel(desired)
			if err != nil {
				return fmt.Errorf("creating tunnel for node %d: %w", nodeID, err)
			}
			owned[nodeID] = link
		}
		if err := netlink.LinkSetMTU(owned[nodeID], tunnelMTU); err != nil {
			return fmt.Errorf("setting tunnel MTU for node %d: %w", nodeID, err)
		}
		if err := netlink.LinkSetUp(owned[nodeID]); err != nil {
			return fmt.Errorf("bringing tunnel for node %d up: %w", nodeID, err)
		}
	}

	desiredRoutes := make(map[netip.Addr]int, len(topology.Routes))
	for _, route := range topology.Routes {
		link, ok := owned[route.NodeID]
		if !ok {
			return fmt.Errorf("route %s has no tunnel for node %d", route.Addr, route.NodeID)
		}
		desiredRoutes[route.Addr] = link.Attrs().Index
	}
	if err := reconcileRemoteWorkloadRoutes(topology.Prefix, desiredRoutes, ownedTunnelIndexes(owned)); err != nil {
		return err
	}

	// Routes have been removed before their unused tunnel is deleted.
	used := make(map[int32]struct{}, len(topology.Routes))
	for _, route := range topology.Routes {
		used[route.NodeID] = struct{}{}
	}
	for nodeID, link := range owned {
		if _, wanted := desiredTunnels[nodeID]; wanted {
			continue
		}
		if _, referenced := used[nodeID]; referenced {
			continue
		}
		if err := netlink.LinkDel(link); err != nil && !errors.Is(err, unix.ESRCH) {
			return fmt.Errorf("deleting stale tunnel for node %d: %w", nodeID, err)
		}
	}
	return nil
}

func validateTopology(topology Topology) error {
	if topology.Prefix.IsZero() || topology.LocalNodeID <= 0 {
		return fmt.Errorf("topology is missing cluster prefix or local node identity")
	}
	tunnels := make(map[int32]Tunnel, len(topology.Tunnels))
	for _, tunnel := range topology.Tunnels {
		if tunnel.NodeID <= 0 || tunnel.NodeID == topology.LocalNodeID || !tunnel.Local.IsValid() || !tunnel.Remote.IsValid() ||
			tunnel.Local.Zone() != "" || tunnel.Remote.Zone() != "" || tunnel.Local.BitLen() != tunnel.Remote.BitLen() {
			return fmt.Errorf("invalid tunnel for node %d", tunnel.NodeID)
		}
		if _, exists := tunnels[tunnel.NodeID]; exists {
			return fmt.Errorf("duplicate tunnel for node %d", tunnel.NodeID)
		}
		tunnels[tunnel.NodeID] = tunnel
	}
	seenRoutes := make(map[netip.Addr]struct{}, len(topology.Routes))
	for _, route := range topology.Routes {
		if err := topology.Prefix.ValidateRoutedAddr(route.Addr); err != nil {
			return err
		}
		if _, exists := tunnels[route.NodeID]; !exists {
			return fmt.Errorf("route %s references missing tunnel node %d", route.Addr, route.NodeID)
		}
		if _, exists := seenRoutes[route.Addr]; exists {
			return fmt.Errorf("duplicate remote route %s", route.Addr)
		}
		seenRoutes[route.Addr] = struct{}{}
	}
	return nil
}

func tunnelName(nodeID int32) string { return "odt" + strconv.FormatInt(int64(nodeID), 36) }

func tunnelAlias(nodeID int32) string {
	return tunnelAliasPrefix + strconv.FormatInt(int64(nodeID), 10)
}

func addTunnel(tunnel Tunnel) (netlink.Link, error) {
	attrs := netlink.LinkAttrs{Name: tunnelName(tunnel.NodeID), Alias: tunnelAlias(tunnel.NodeID), MTU: tunnelMTU}
	var link netlink.Link
	if tunnel.Local.Is4() {
		link = &netlink.Sittun{
			LinkAttrs: attrs,
			Local:     net.IP(tunnel.Local.AsSlice()),
			Remote:    net.IP(tunnel.Remote.AsSlice()),
			Proto:     unix.IPPROTO_IPV6,
			PMtuDisc:  1,
		}
	} else {
		link = &netlink.Ip6tnl{
			LinkAttrs: attrs,
			Local:     net.IP(tunnel.Local.AsSlice()),
			Remote:    net.IP(tunnel.Remote.AsSlice()),
			Proto:     unix.IPPROTO_IPV6,
		}
	}
	if err := netlink.LinkAdd(link); err != nil {
		return nil, err
	}
	created, err := netlink.LinkByName(attrs.Name)
	if err != nil {
		return nil, err
	}
	// IFLA_IFALIAS is not applied by LinkAdd for SIT/IP6TNL links on all
	// kernels. Set it explicitly because the alias is our ownership boundary.
	if err := netlink.LinkSetAlias(created, attrs.Alias); err != nil {
		_ = netlink.LinkDel(created)
		return nil, err
	}
	return created, nil
}

func listOwnedTunnels() (map[int32]netlink.Link, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}
	owned := make(map[int32]netlink.Link)
	for _, link := range links {
		nodeID, ok := ownedTunnelNodeID(link)
		if !ok {
			continue
		}
		if _, duplicate := owned[nodeID]; duplicate {
			return nil, fmt.Errorf("duplicate owned tunnel for node %d", nodeID)
		}
		owned[nodeID] = link
	}
	return owned, nil
}

func ownedTunnelNodeID(link netlink.Link) (int32, bool) {
	if link == nil {
		return 0, false
	}
	switch link.(type) {
	case *netlink.Sittun, *netlink.Ip6tnl:
	default:
		return 0, false
	}
	attrs := link.Attrs()
	if attrs == nil || !strings.HasPrefix(attrs.Alias, tunnelAliasPrefix) {
		return 0, false
	}
	raw := strings.TrimPrefix(attrs.Alias, tunnelAliasPrefix)
	nodeID, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || nodeID <= 0 || attrs.Name != tunnelName(int32(nodeID)) {
		return 0, false
	}
	return int32(nodeID), true
}

func tunnelMatches(link netlink.Link, desired Tunnel) bool {
	if nodeID, ok := ownedTunnelNodeID(link); !ok || nodeID != desired.NodeID {
		return false
	}
	switch current := link.(type) {
	case *netlink.Sittun:
		return desired.Local.Is4() && current.Local.Equal(net.IP(desired.Local.AsSlice())) && current.Remote.Equal(net.IP(desired.Remote.AsSlice())) && current.Proto == unix.IPPROTO_IPV6
	case *netlink.Ip6tnl:
		return desired.Local.Is6() && current.Local.Equal(net.IP(desired.Local.AsSlice())) && current.Remote.Equal(net.IP(desired.Remote.AsSlice())) && current.Proto == unix.IPPROTO_IPV6
	default:
		return false
	}
}

func ownedTunnelIndexes(tunnels map[int32]netlink.Link) map[int]struct{} {
	indexes := make(map[int]struct{}, len(tunnels))
	for _, tunnel := range tunnels {
		indexes[tunnel.Attrs().Index] = struct{}{}
	}
	return indexes
}

func reconcileRemoteWorkloadRoutes(prefix Prefix, desired map[netip.Addr]int, tunnelIndexes map[int]struct{}) error {
	routes, err := listProtocolRoutes(systemRouteOperations{}, routeProtocolOpenDeploy)
	if err != nil {
		return fmt.Errorf("listing OpenDeploy routes: %w", err)
	}
	for addr, linkIndex := range desired {
		if routeIsLocalWorkload(routes, addr) {
			continue
		}
		route := remoteWorkloadRoute(addr, linkIndex)
		if err := netlink.RouteReplace(&route); err != nil {
			return fmt.Errorf("installing remote route %s: %w", addr, err)
		}
	}
	for i := range routes {
		if !isOwnedRemoteWorkloadRoute(routes[i], prefix, tunnelIndexes) {
			continue
		}
		addr, _ := netlinkRoutePrefix(routes[i])
		if _, wanted := desired[addr.Addr()]; wanted {
			continue
		}
		if err := netlink.RouteDel(&routes[i]); err != nil && !errors.Is(err, unix.ESRCH) {
			return fmt.Errorf("deleting stale remote route %s: %w", addr, err)
		}
	}
	return nil
}

func isOwnedRemoteWorkloadRoute(route netlink.Route, prefix Prefix, tunnelIndexes map[int]struct{}) bool {
	destination, ok := netlinkRoutePrefix(route)
	if !ok || !protocolAndTableMatch(route, routeProtocolOpenDeploy) || route.Type != unix.RTN_UNICAST ||
		len(route.MultiPath) != 0 || len(route.Gw) != 0 || destination.Bits() != 128 || !prefix.CIDR().Contains(destination.Addr()) {
		return false
	}
	_, ok = tunnelIndexes[route.LinkIndex]
	return ok
}

func routeIsLocalWorkload(routes []netlink.Route, addr netip.Addr) bool {
	for _, route := range routes {
		destination, ok := netlinkRoutePrefix(route)
		if !ok || destination.Bits() != 128 || destination.Addr() != addr || route.Type != unix.RTN_UNICAST || route.LinkIndex <= 0 {
			continue
		}
		link, err := netlink.LinkByIndex(route.LinkIndex)
		if err != nil || link.Type() != "veth" || !isWorkloadVethName(link.Attrs().Name) {
			continue
		}
		return true
	}
	return false
}

func isWorkloadVethName(name string) bool {
	if !strings.HasPrefix(name, "od") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(name, "od"), "s")
	if len(parts) != 2 || (parts[1] != "0" && parts[1] != "1") {
		return false
	}
	_, err := strconv.ParseInt(parts[0], 10, 32)
	return err == nil
}
