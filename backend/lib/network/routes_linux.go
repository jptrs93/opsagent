//go:build linux

package network

import (
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
)

// AuditRouteProtocol exposes the manager's route protocol value so the
// netaudit package can select exactly the routes this manager owns.
func AuditRouteProtocol() netlink.RouteProtocol { return routeProtocolOpenDeploy }

type routeOperations interface {
	RouteReplace(route *netlink.Route) error
	RouteListFiltered(family int, filter *netlink.Route, filterMask uint64) ([]netlink.Route, error)
}

type systemRouteOperations struct{}

func (systemRouteOperations) RouteReplace(route *netlink.Route) error {
	return netlink.RouteReplace(route)
}

func (systemRouteOperations) RouteListFiltered(family int, filter *netlink.Route, filterMask uint64) ([]netlink.Route, error) {
	return netlink.RouteListFiltered(family, filter, filterMask)
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

func remoteWorkloadRoute(prefix netip.Prefix, tunnelLinkIndex int) netlink.Route {
	return netlink.Route{
		LinkIndex: tunnelLinkIndex,
		Dst:       netipPrefixToIPNet(prefix),
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
	if !prefix.IsZero() {
		if err := prefix.ValidateRoutedAddr(addr); err != nil {
			return fmt.Errorf("invalid workload address: %w", err)
		}
	}
	route := localWorkloadRoute(addr, linkIndex)
	return ops.RouteReplace(&route)
}

func reconcileClusterFallbackRoute(prefix Prefix) error {
	return reconcileClusterFallbackRouteWithOps(systemRouteOperations{}, prefix)
}

func reconcileClusterFallbackRouteWithOps(ops routeOperations, prefix Prefix) error {
	route := clusterFallbackRoute(prefix)
	return ops.RouteReplace(&route)
}

func listProtocolRoutes(ops routeOperations, protocol netlink.RouteProtocol) ([]netlink.Route, error) {
	return ops.RouteListFiltered(netlink.FAMILY_V6, &netlink.Route{
		Protocol: protocol,
		Table:    unix.RT_TABLE_MAIN,
	}, netlink.RT_FILTER_PROTOCOL|netlink.RT_FILTER_TABLE)
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
