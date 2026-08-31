//go:build linux

package network

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"slices"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const (
	WGLinkName  = "odwg0"
	wgLinkAlias = "opendeploy:wg"
	// wgMTU leaves room for the WireGuard overhead on a normal 1500-byte
	// underlay: 80 bytes over IPv6, 60 over IPv4, so 1420 is safe for both
	// families.
	wgMTU = 1420
)

// ReconcileTopology converges the cross-node transport — one WireGuard device
// with a peer entry per remote node — plus remote workload routes, without
// touching locally attached workload routes or foreign links.
func (m *Manager) ReconcileTopology(topology Topology) error {
	if err := validateTopology(topology); err != nil {
		return err
	}
	if err := m.EnsureBase(); err != nil {
		return err
	}

	privateKey, hasPrivateKey := m.wgPrivateKeyValue()
	if !hasPrivateKey {
		return fmt.Errorf("no WireGuard private key loaded")
	}

	peers := make(map[int32]Peer, len(topology.Peers))
	for _, peer := range topology.Peers {
		peers[peer.NodeID] = peer
	}

	// The WireGuard device is reconciled before routes so route replacement
	// can point at it atomically.
	wgIndex, wgAudit, err := reconcileWGDevice(privateKey, topology.LocalWGPort, peers, topology.Routes)
	if err != nil {
		return err
	}

	desiredRoutes := make(map[netip.Prefix]int, len(topology.Routes))
	for _, route := range topology.Routes {
		desiredRoutes[route.Prefix] = wgIndex
	}
	if err := reconcileRemoteWorkloadRoutes(topology.Prefix, desiredRoutes, map[int]struct{}{wgIndex: {}}); err != nil {
		return err
	}

	m.mu.Lock()
	m.wgDesired = wgAudit
	m.mu.Unlock()
	return nil
}

// reconcileWGDevice ensures the managed WireGuard link exists and holds the
// full desired configuration: local key, listen port, and one peer per remote
// node whose allowed-ips are exactly the routed prefixes the map assigns to
// that node. Cryptokey routing then enforces node-level source attribution:
// the kernel drops decrypted packets whose source lies outside the sending
// node's routed prefixes.
func reconcileWGDevice(privateKey wgtypes.Key, listenPort uint16, peers map[int32]Peer, routes []RemoteRoute) (int, *WGAuditState, error) {
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
		allowedByNode[route.NodeID] = append(allowedByNode[route.NodeID], route.Prefix)
	}
	nodeIDs := slices.Sorted(maps.Keys(peers))
	peerConfigs := make([]wgtypes.PeerConfig, 0, len(peers))
	audit := &WGAuditState{PublicKey: privateKey.PublicKey().String(), ListenPort: listenPort}
	for _, nodeID := range nodeIDs {
		peer := peers[nodeID]
		publicKey, err := wgtypes.ParseKey(peer.WGKey)
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
		endpoint := &net.UDPAddr{IP: peer.Endpoint.AsSlice(), Port: int(peer.WGPort)}
		peerConfigs = append(peerConfigs, wgtypes.PeerConfig{
			PublicKey:         publicKey,
			Endpoint:          endpoint,
			ReplaceAllowedIPs: true,
			AllowedIPs:        allowedIPs,
		})
		audit.Peers = append(audit.Peers, WGAuditPeer{
			NodeID:     nodeID,
			PublicKey:  publicKey.String(),
			Endpoint:   netip.AddrPortFrom(peer.Endpoint, peer.WGPort),
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
		Peers:        peerConfigs,
	}); err != nil {
		return 0, nil, fmt.Errorf("configuring WireGuard device: %w", err)
	}
	return link.Attrs().Index, audit, nil
}

func validateTopology(topology Topology) error {
	if topology.Prefix.IsZero() || topology.LocalNodeID <= 0 {
		return fmt.Errorf("topology is missing cluster prefix or local node identity")
	}
	if topology.LocalWGPort == 0 {
		return fmt.Errorf("topology is missing the local WireGuard listen port")
	}
	peers := make(map[int32]Peer, len(topology.Peers))
	for _, peer := range topology.Peers {
		if peer.NodeID <= 0 || peer.NodeID == topology.LocalNodeID || !peer.Endpoint.IsValid() || peer.Endpoint.Zone() != "" {
			return fmt.Errorf("invalid peer for node %d", peer.NodeID)
		}
		if peer.WGKey == "" || peer.WGPort == 0 {
			return fmt.Errorf("node %d is missing WireGuard key or listen port", peer.NodeID)
		}
		if _, exists := peers[peer.NodeID]; exists {
			return fmt.Errorf("duplicate peer for node %d", peer.NodeID)
		}
		peers[peer.NodeID] = peer
	}
	seenRoutes := make(map[netip.Prefix]struct{}, len(topology.Routes))
	for _, route := range topology.Routes {
		if err := topology.Prefix.ValidateRoutedPrefix(route.Prefix); err != nil {
			return err
		}
		if _, exists := peers[route.NodeID]; !exists {
			return fmt.Errorf("route %s references missing peer node %d", route.Prefix, route.NodeID)
		}
		if _, exists := seenRoutes[route.Prefix]; exists {
			return fmt.Errorf("duplicate remote route %s", route.Prefix)
		}
		seenRoutes[route.Prefix] = struct{}{}
	}
	return nil
}

// reconcileRemoteWorkloadRoutes installs the map's routed prefixes over the
// WireGuard device and removes the ones it no longer names.
//
// Remote routes need no guard against locally attached workloads. Local
// workload routes are /128 and routed prefixes are never longer than /120, so
// longest-prefix match always prefers the local veth even while a map lags
// behind a placement that has just moved onto this node.
func reconcileRemoteWorkloadRoutes(prefix Prefix, desired map[netip.Prefix]int, ownedIndexes map[int]struct{}) error {
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
		if !isOwnedRemoteWorkloadRoute(routes[i], prefix, ownedIndexes) {
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
// its own transport links. It deliberately does not check the prefix length:
// ownership is established by the route protocol tag plus the link, and
// accepting any length lets reconciliation clean up routes left by an earlier
// layout.
func isOwnedRemoteWorkloadRoute(route netlink.Route, prefix Prefix, ownedIndexes map[int]struct{}) bool {
	destination, ok := netlinkRoutePrefix(route)
	if !ok || !protocolAndTableMatch(route, routeProtocolOpenDeploy) || route.Type != unix.RTN_UNICAST ||
		len(route.MultiPath) != 0 || len(route.Gw) != 0 || !prefix.CIDR().Contains(destination.Addr()) {
		return false
	}
	_, ok = ownedIndexes[route.LinkIndex]
	return ok
}
