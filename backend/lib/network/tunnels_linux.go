//go:build linux

package network

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const (
	tunnelAliasPrefix = "opendeploy:tunnel:"
	tunnelMTU         = 1420
	WGLinkName        = "odwg0"
	wgLinkAlias       = "opendeploy:wg"
	// wgMTU matches tunnelMTU: WireGuard over an IPv6 underlay costs 80 bytes
	// (60 over IPv4), so 1420 is safe for both families and keeps the inner
	// path MTU identical across the transport migration.
	wgMTU = 1420
)

// ReconcileTopology converges the cross-node transport — one WireGuard device
// with a peer entry per keyed pair, fixed protocol-41 tunnels for pairs where
// either side lacks a key — plus remote workload routes, without touching
// locally attached workload routes or foreign links. Both ends derive the same
// pairwise transport rule from the map, so the two directions of a pair always
// agree.
func (m *Manager) ReconcileTopology(topology Topology) error {
	if err := validateTopology(topology); err != nil {
		return err
	}
	if err := m.EnsureBase(); err != nil {
		return err
	}

	privateKey, hasPrivateKey := m.wgPrivateKeyValue()
	wgActive := topology.LocalWGCapable && hasPrivateKey && topology.LocalWGPort > 0

	wgPairs := make(map[int32]Tunnel)
	tunnelPairs := make(map[int32]Tunnel)
	for _, pair := range topology.Tunnels {
		if wgActive && pair.RemoteWGKey != "" {
			wgPairs[pair.NodeID] = pair
		} else {
			tunnelPairs[pair.NodeID] = pair
		}
	}

	// The WireGuard device is reconciled before routes so route replacement
	// can point at it atomically; ip6tnl teardown happens after routes so a
	// pair migrating to WireGuard never has a routed prefix without a link.
	wgIndex := 0
	var wgAudit *WGAuditState
	if wgActive {
		index, audit, err := reconcileWGDevice(privateKey, topology.LocalWGPort, wgPairs, topology.Routes)
		if err != nil {
			return err
		}
		wgIndex = index
		wgAudit = audit
	} else if err := deleteWGDevice(); err != nil {
		return err
	}

	owned, err := listOwnedTunnels()
	if err != nil {
		return err
	}
	for nodeID, desired := range tunnelPairs {
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

	desiredRoutes := make(map[netip.Prefix]int, len(topology.Routes))
	for _, route := range topology.Routes {
		if _, viaWG := wgPairs[route.NodeID]; viaWG {
			desiredRoutes[route.Prefix] = wgIndex
			continue
		}
		link, ok := owned[route.NodeID]
		if !ok {
			return fmt.Errorf("route %s has no transport for node %d", route.Prefix, route.NodeID)
		}
		desiredRoutes[route.Prefix] = link.Attrs().Index
	}
	ownedIndexes := ownedTunnelIndexes(owned)
	if wgIndex != 0 {
		ownedIndexes[wgIndex] = struct{}{}
	}
	if err := reconcileRemoteWorkloadRoutes(topology.Prefix, desiredRoutes, ownedIndexes); err != nil {
		return err
	}

	// Routes have been moved or removed before their unused tunnel is deleted.
	for nodeID, link := range owned {
		if _, wanted := tunnelPairs[nodeID]; wanted {
			continue
		}
		if err := netlink.LinkDel(link); err != nil && !errors.Is(err, unix.ESRCH) {
			return fmt.Errorf("deleting stale tunnel for node %d: %w", nodeID, err)
		}
	}

	m.mu.Lock()
	m.wgDesired = wgAudit
	m.mu.Unlock()
	return nil
}

// reconcileWGDevice ensures the managed WireGuard link exists and holds the
// full desired configuration: local key, listen port, and one peer per keyed
// pair whose allowed-ips are exactly the routed prefixes the map assigns to
// that node. Cryptokey routing then enforces node-level source attribution:
// the kernel drops decrypted packets whose source lies outside the sending
// node's routed prefixes.
func reconcileWGDevice(privateKey wgtypes.Key, listenPort uint16, wgPairs map[int32]Tunnel, routes []RemoteRoute) (int, *WGAuditState, error) {
	link, err := netlink.LinkByName(WGLinkName)
	if _, missing := err.(netlink.LinkNotFoundError); missing {
		attrs := netlink.NewLinkAttrs()
		attrs.Name = WGLinkName
		attrs.Alias = wgLinkAlias
		attrs.MTU = wgMTU
		if err := netlink.LinkAdd(&netlink.Wireguard{LinkAttrs: attrs}); err != nil {
			return 0, nil, fmt.Errorf("creating WireGuard device: %w", err)
		}
		link, err = netlink.LinkByName(WGLinkName)
	}
	if err != nil {
		return 0, nil, fmt.Errorf("resolving WireGuard device: %w", err)
	}
	if _, ok := link.(*netlink.Wireguard); !ok {
		return 0, nil, fmt.Errorf("link %s exists but is not a WireGuard device", WGLinkName)
	}
	if link.Attrs().Alias != wgLinkAlias {
		if err := netlink.LinkSetAlias(link, wgLinkAlias); err != nil {
			return 0, nil, fmt.Errorf("setting WireGuard device alias: %w", err)
		}
	}
	if err := netlink.LinkSetMTU(link, wgMTU); err != nil {
		return 0, nil, fmt.Errorf("setting WireGuard device MTU: %w", err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return 0, nil, fmt.Errorf("bringing WireGuard device up: %w", err)
	}

	allowedByNode := make(map[int32][]netip.Prefix)
	for _, route := range routes {
		if _, ok := wgPairs[route.NodeID]; ok {
			allowedByNode[route.NodeID] = append(allowedByNode[route.NodeID], route.Prefix)
		}
	}
	nodeIDs := slices.Sorted(maps.Keys(wgPairs))
	peers := make([]wgtypes.PeerConfig, 0, len(wgPairs))
	audit := &WGAuditState{PublicKey: privateKey.PublicKey().String(), ListenPort: listenPort}
	for _, nodeID := range nodeIDs {
		pair := wgPairs[nodeID]
		publicKey, err := wgtypes.ParseKey(pair.RemoteWGKey)
		if err != nil {
			return 0, nil, fmt.Errorf("parsing WireGuard key for node %d: %w", nodeID, err)
		}
		prefixes := allowedByNode[nodeID]
		slices.SortFunc(prefixes, func(a, b netip.Prefix) int { return strings.Compare(a.String(), b.String()) })
		allowedIPs := make([]net.IPNet, 0, len(prefixes))
		for _, prefix := range prefixes {
			allowedIPs = append(allowedIPs, net.IPNet{
				IP:   prefix.Addr().AsSlice(),
				Mask: net.CIDRMask(prefix.Bits(), prefix.Addr().BitLen()),
			})
		}
		endpoint := &net.UDPAddr{IP: pair.Remote.AsSlice(), Port: int(pair.RemoteWGPort)}
		peers = append(peers, wgtypes.PeerConfig{
			PublicKey:         publicKey,
			Endpoint:          endpoint,
			ReplaceAllowedIPs: true,
			AllowedIPs:        allowedIPs,
		})
		audit.Peers = append(audit.Peers, WGAuditPeer{
			NodeID:     nodeID,
			PublicKey:  publicKey.String(),
			Endpoint:   netip.AddrPortFrom(pair.Remote, pair.RemoteWGPort),
			AllowedIPs: prefixes,
		})
	}

	client, err := wgctrl.New()
	if err != nil {
		return 0, nil, fmt.Errorf("opening WireGuard control socket: %w", err)
	}
	defer client.Close()
	port := int(listenPort)
	if err := client.ConfigureDevice(WGLinkName, wgtypes.Config{
		PrivateKey:   &privateKey,
		ListenPort:   &port,
		ReplacePeers: true,
		Peers:        peers,
	}); err != nil {
		return 0, nil, fmt.Errorf("configuring WireGuard device: %w", err)
	}
	return link.Attrs().Index, audit, nil
}

// deleteWGDevice removes the managed WireGuard link when the local node has no
// active WireGuard identity, tolerating its absence.
func deleteWGDevice() error {
	link, err := netlink.LinkByName(WGLinkName)
	if _, missing := err.(netlink.LinkNotFoundError); missing {
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolving WireGuard device: %w", err)
	}
	if _, ok := link.(*netlink.Wireguard); !ok {
		return fmt.Errorf("link %s exists but is not a WireGuard device", WGLinkName)
	}
	if err := netlink.LinkDel(link); err != nil && !errors.Is(err, unix.ESRCH) {
		return fmt.Errorf("deleting WireGuard device: %w", err)
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
		if tunnel.RemoteWGKey != "" && tunnel.RemoteWGPort == 0 {
			return fmt.Errorf("node %d has WireGuard key but no listen port", tunnel.NodeID)
		}
		if _, exists := tunnels[tunnel.NodeID]; exists {
			return fmt.Errorf("duplicate tunnel for node %d", tunnel.NodeID)
		}
		tunnels[tunnel.NodeID] = tunnel
	}
	seenRoutes := make(map[netip.Prefix]struct{}, len(topology.Routes))
	for _, route := range topology.Routes {
		if err := topology.Prefix.ValidateRoutedPrefix(route.Prefix); err != nil {
			return err
		}
		if _, exists := tunnels[route.NodeID]; !exists {
			return fmt.Errorf("route %s references missing tunnel node %d", route.Prefix, route.NodeID)
		}
		if _, exists := seenRoutes[route.Prefix]; exists {
			return fmt.Errorf("duplicate remote route %s", route.Prefix)
		}
		seenRoutes[route.Prefix] = struct{}{}
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

// reconcileRemoteWorkloadRoutes installs the map's routed prefixes over tunnels
// and removes the ones it no longer names.
//
// Remote routes need no guard against locally attached workloads. Local
// workload routes are /128 and routed prefixes are never longer than /120, so
// longest-prefix match always prefers the local veth even while a map lags
// behind a placement that has just moved onto this node.
func reconcileRemoteWorkloadRoutes(prefix Prefix, desired map[netip.Prefix]int, tunnelIndexes map[int]struct{}) error {
	routes, err := listProtocolRoutes(systemRouteOperations{}, routeProtocolOpenDeploy)
	if err != nil {
		return fmt.Errorf("listing OpenDeploy routes: %w", err)
	}
	for destination, linkIndex := range desired {
		route := remoteWorkloadRoute(destination, linkIndex)
		if err := netlink.RouteReplace(&route); err != nil {
			return fmt.Errorf("installing remote route %s: %w", destination, err)
		}
	}
	for i := range routes {
		if !isOwnedRemoteWorkloadRoute(routes[i], prefix, tunnelIndexes) {
			continue
		}
		destination, _ := netlinkRoutePrefix(routes[i])
		if _, wanted := desired[destination]; wanted {
			continue
		}
		if err := netlink.RouteDel(&routes[i]); err != nil && !errors.Is(err, unix.ESRCH) {
			return fmt.Errorf("deleting stale remote route %s: %w", destination, err)
		}
	}
	return nil
}

// isOwnedRemoteWorkloadRoute matches anything this agent installed over one of
// its own tunnels. It deliberately does not check the prefix length: ownership
// is established by the route protocol tag plus the tunnel link, and accepting
// any length lets reconciliation clean up routes left by an earlier layout.
func isOwnedRemoteWorkloadRoute(route netlink.Route, prefix Prefix, tunnelIndexes map[int]struct{}) bool {
	destination, ok := netlinkRoutePrefix(route)
	if !ok || !protocolAndTableMatch(route, routeProtocolOpenDeploy) || route.Type != unix.RTN_UNICAST ||
		len(route.MultiPath) != 0 || len(route.Gw) != 0 || !prefix.CIDR().Contains(destination.Addr()) {
		return false
	}
	_, ok = tunnelIndexes[route.LinkIndex]
	return ok
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
