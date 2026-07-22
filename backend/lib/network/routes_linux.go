//go:build linux

package network

import (
	"errors"
	"fmt"
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	// Linux has no private route-protocol range. Value 200 is currently
	// unassigned, so deletion must also verify route shape and interface
	// ownership rather than trusting this value alone.
	routeProtocolOpenDeploy = netlink.RouteProtocol(200)
	// Protocol 99 is RTPROT_OPENR. Early OpenDeploy builds used it incorrectly;
	// it is accepted only for narrowly scoped migration cleanup.
	legacyRouteProtocolOpenDeploy = netlink.RouteProtocol(99)
)

type routeOperations interface {
	RouteReplace(route *netlink.Route) error
	RouteListFiltered(family int, filter *netlink.Route, filterMask uint64) ([]netlink.Route, error)
	RouteDel(route *netlink.Route) error
}

type systemRouteOperations struct{}

func (systemRouteOperations) RouteReplace(route *netlink.Route) error {
	return netlink.RouteReplace(route)
}

func (systemRouteOperations) RouteListFiltered(family int, filter *netlink.Route, filterMask uint64) ([]netlink.Route, error) {
	return netlink.RouteListFiltered(family, filter, filterMask)
}

func (systemRouteOperations) RouteDel(route *netlink.Route) error {
	return netlink.RouteDel(route)
}

func localWorkloadRoute(addr netip.Addr, linkIndex int) netlink.Route {
	return netlink.Route{
		LinkIndex: linkIndex,
		Dst:       netipPrefixToIPNet(netip.PrefixFrom(addr, 128)),
		Protocol:  routeProtocolOpenDeploy,
		Table:     unix.RT_TABLE_MAIN,
		Type:      unix.RTN_UNICAST,
	}
}

func remoteWorkloadRoute(addr netip.Addr, tunnelLinkIndex int) netlink.Route {
	return netlink.Route{
		LinkIndex: tunnelLinkIndex,
		Dst:       netipPrefixToIPNet(netip.PrefixFrom(addr, 128)),
		Protocol:  routeProtocolOpenDeploy,
		Table:     unix.RT_TABLE_MAIN,
		Type:      unix.RTN_UNICAST,
	}
}

func clusterFallbackRoute(prefix Prefix) netlink.Route {
	return netlink.Route{
		Dst:      netipPrefixToIPNet(prefix.CIDR()),
		Protocol: routeProtocolOpenDeploy,
		Table:    unix.RT_TABLE_MAIN,
		Type:     unix.RTN_UNREACHABLE,
	}
}

func reconcileLocalWorkloadRoute(prefix Prefix, addr netip.Addr, linkIndex int) error {
	return reconcileLocalWorkloadRouteWithOps(systemRouteOperations{}, prefix, addr, linkIndex)
}

func reconcileLocalWorkloadRouteWithOps(ops routeOperations, prefix Prefix, addr netip.Addr, linkIndex int) error {
	if !prefix.IsZero() && !prefix.CIDR().Contains(addr) {
		return fmt.Errorf("workload address %s is outside cluster prefix %s", addr, prefix)
	}
	route := localWorkloadRoute(addr, linkIndex)
	if err := ops.RouteReplace(&route); err != nil {
		return err
	}
	if prefix.IsZero() {
		return nil
	}
	return deleteMatchingProtocolRoutes(ops, legacyRouteProtocolOpenDeploy, func(candidate netlink.Route) bool {
		return isOwnedLocalWorkloadRoute(candidate, legacyRouteProtocolOpenDeploy, prefix, addr, linkIndex)
	})
}

func reconcileClusterFallbackRoute(prefix Prefix) error {
	return reconcileClusterFallbackRouteWithOps(systemRouteOperations{}, prefix)
}

func reconcileClusterFallbackRouteWithOps(ops routeOperations, prefix Prefix) error {
	route := clusterFallbackRoute(prefix)
	if err := ops.RouteReplace(&route); err != nil {
		return err
	}
	return deleteMatchingProtocolRoutes(ops, legacyRouteProtocolOpenDeploy, func(candidate netlink.Route) bool {
		return isOwnedClusterFallbackRoute(candidate, legacyRouteProtocolOpenDeploy, prefix)
	})
}

func listProtocolRoutes(ops routeOperations, protocol netlink.RouteProtocol) ([]netlink.Route, error) {
	return ops.RouteListFiltered(netlink.FAMILY_V6, &netlink.Route{
		Protocol: protocol,
		Table:    unix.RT_TABLE_MAIN,
	}, netlink.RT_FILTER_PROTOCOL|netlink.RT_FILTER_TABLE)
}

func deleteMatchingProtocolRoutes(ops routeOperations, protocol netlink.RouteProtocol, matches func(netlink.Route) bool) error {
	routes, err := listProtocolRoutes(ops, protocol)
	if err != nil {
		return fmt.Errorf("listing protocol %d routes: %w", protocol, err)
	}
	for i := range routes {
		if !matches(routes[i]) {
			continue
		}
		// Delete the complete object returned by netlink. Constructing a partial
		// delete request could select a different route after a concurrent change.
		if err := ops.RouteDel(&routes[i]); err != nil && !errors.Is(err, unix.ESRCH) {
			return fmt.Errorf("deleting legacy route %s: %w", routes[i].Dst, err)
		}
	}
	return nil
}

func isOwnedLocalWorkloadRoute(route netlink.Route, protocol netlink.RouteProtocol, prefix Prefix, addr netip.Addr, linkIndex int) bool {
	destination, ok := netlinkRoutePrefix(route)
	return ok &&
		protocolAndTableMatch(route, protocol) &&
		route.Type == unix.RTN_UNICAST &&
		route.LinkIndex == linkIndex && linkIndex > 0 &&
		len(route.MultiPath) == 0 && len(route.Gw) == 0 &&
		destination.Bits() == 128 && destination.Addr() == addr &&
		prefix.CIDR().Contains(destination.Addr())
}

func isOwnedClusterFallbackRoute(route netlink.Route, protocol netlink.RouteProtocol, prefix Prefix) bool {
	destination, ok := netlinkRoutePrefix(route)
	return ok &&
		protocolAndTableMatch(route, protocol) &&
		route.Type == unix.RTN_UNREACHABLE &&
		len(route.MultiPath) == 0 && len(route.Gw) == 0 &&
		destination == prefix.CIDR()
}

func protocolAndTableMatch(route netlink.Route, protocol netlink.RouteProtocol) bool {
	return route.Protocol == protocol && route.Table == unix.RT_TABLE_MAIN
}

func netlinkRoutePrefix(route netlink.Route) (netip.Prefix, bool) {
	if route.Dst == nil {
		return netip.Prefix{}, false
	}
	ones, bits := route.Dst.Mask.Size()
	if ones < 0 {
		return netip.Prefix{}, false
	}
	addr, ok := netip.AddrFromSlice(route.Dst.IP)
	if !ok || !addr.Is6() || bits != addr.BitLen() {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(addr, ones).Masked(), true
}
