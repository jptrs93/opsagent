package network

import (
	"fmt"
	"net/netip"

	"github.com/jptrs93/opsagent/backend/apigen"
	"golang.org/x/sys/unix"
)

// ApplyClusterNetMap converts a targeted cluster map into kernel intent and
// hands it to the given sinks: remote topology first, then policy rules, so a
// rule referencing a newly routed peer is never enforced against routes that
// do not exist yet, then the node's ingress publish set. Only remote paths are
// applied — local workload routes are installed by the container lifecycle and
// take precedence if map state lags.
func ApplyClusterNetMap(clusterMap *apigen.ClusterNetMap, nodeID int32, prefix Prefix, reconcile func(Topology) error, setPolicyRules func([]PolicyRule) error, setNetproxyPublish func([]IngressPublish) error) error {
	topology, err := TopologyFromClusterNetMap(clusterMap, nodeID, prefix)
	if err != nil {
		return err
	}
	publish, err := NetproxyPublishFromClusterNetMap(clusterMap, nodeID)
	if err != nil {
		return err
	}
	if err := reconcile(topology); err != nil {
		return err
	}
	if err := setPolicyRules(PolicyRulesFromNetMap(clusterMap.PolicyRules)); err != nil {
		return err
	}
	return setNetproxyPublish(publish)
}

// NetproxyPublishFromClusterNetMap extracts the local node's ingress publish
// set. An empty address is the wildcard (every local address).
func NetproxyPublishFromClusterNetMap(clusterMap *apigen.ClusterNetMap, nodeID int32) ([]IngressPublish, error) {
	if clusterMap == nil {
		return nil, fmt.Errorf("network map is nil")
	}
	var out []IngressPublish
	for _, node := range clusterMap.Nodes {
		if node == nil || node.NodeID != nodeID {
			continue
		}
		for _, entry := range node.IngressPublish {
			if entry == nil {
				continue
			}
			if entry.Port < 1 || entry.Port > 65535 {
				return nil, fmt.Errorf("network map ingress publish has invalid port %d", entry.Port)
			}
			publish := IngressPublish{Port: uint16(entry.Port)}
			if entry.Address != "" {
				addr, err := netip.ParseAddr(entry.Address)
				if err != nil || addr.Zone() != "" {
					return nil, fmt.Errorf("network map ingress publish has invalid address %q", entry.Address)
				}
				publish.Address = addr.Unmap()
			}
			out = append(out, publish)
		}
	}
	return out, nil
}

func TopologyFromClusterNetMap(clusterMap *apigen.ClusterNetMap, nodeID int32, prefix Prefix) (Topology, error) {
	if clusterMap == nil || nodeID <= 0 || prefix.IsZero() {
		return Topology{}, fmt.Errorf("network map topology is missing map, prefix, or local node")
	}
	type nodeTransport struct {
		underlay netip.Addr
		wgKey    string
		wgPort   uint16
	}
	underlays := make(map[int32]nodeTransport, len(clusterMap.Nodes))
	localWGPort := uint16(0)
	for _, node := range clusterMap.Nodes {
		if node == nil || node.NodeID <= 0 {
			return Topology{}, fmt.Errorf("network map topology has invalid node")
		}
		if node.WgPublicKey == "" {
			return Topology{}, fmt.Errorf("network map topology has no WireGuard key for node %d", node.NodeID)
		}
		if node.WgListenPort < 1 || node.WgListenPort > 65535 {
			return Topology{}, fmt.Errorf("network map topology has invalid WireGuard listen port for node %d", node.NodeID)
		}
		if node.NodeID == nodeID {
			localWGPort = uint16(node.WgListenPort)
		}
		if node.UnderlayAddress == "" {
			continue
		}
		addr, err := netip.ParseAddr(node.UnderlayAddress)
		if err != nil || addr.Zone() != "" {
			return Topology{}, fmt.Errorf("network map topology has invalid underlay for node %d", node.NodeID)
		}
		underlays[node.NodeID] = nodeTransport{underlay: addr.Unmap(), wgKey: node.WgPublicKey, wgPort: uint16(node.WgListenPort)}
	}
	if localWGPort == 0 {
		return Topology{}, fmt.Errorf("network map topology is missing local node %d", nodeID)
	}

	topology := Topology{Prefix: prefix, LocalNodeID: nodeID, LocalWGPort: localWGPort}
	remoteHosts := make(map[int32]struct{})
	for _, route := range clusterMap.Routes {
		if route == nil || route.HostingNodeID == nodeID {
			continue
		}
		destination, err := netip.ParsePrefix(route.LogicalPrefix)
		if err != nil {
			return Topology{}, fmt.Errorf("parsing logical route prefix %q: %w", route.LogicalPrefix, err)
		}
		topology.Routes = append(topology.Routes, RemoteRoute{Prefix: destination, NodeID: route.HostingNodeID})
		remoteHosts[route.HostingNodeID] = struct{}{}
	}
	if len(remoteHosts) == 0 {
		return topology, nil
	}
	local, ok := underlays[nodeID]
	if !ok {
		return Topology{}, fmt.Errorf("local node %d has no underlay address", nodeID)
	}
	for remoteNodeID := range remoteHosts {
		remote, ok := underlays[remoteNodeID]
		if !ok {
			return Topology{}, fmt.Errorf("remote node %d has no underlay address", remoteNodeID)
		}
		if local.underlay.BitLen() != remote.underlay.BitLen() {
			return Topology{}, fmt.Errorf("remote node %d underlay family differs from local node", remoteNodeID)
		}
		topology.Peers = append(topology.Peers, Peer{
			NodeID:   remoteNodeID,
			Endpoint: remote.underlay,
			WGKey:    remote.wgKey,
			WGPort:   remote.wgPort,
		})
	}
	return topology, nil
}

func PolicyRulesFromNetMap(rules []*apigen.NetPolicyRule) []PolicyRule {
	out := make([]PolicyRule, 0, len(rules))
	for _, rule := range rules {
		converted, err := policyRuleFromWire(rule)
		if err != nil {
			continue
		}
		out = append(out, converted)
	}
	return out
}

func policyRuleFromWire(rule *apigen.NetPolicyRule) (PolicyRule, error) {
	if rule == nil {
		return PolicyRule{}, fmt.Errorf("policy rule is nil")
	}
	source, err := policyPeerFromWire(rule.Source)
	if err != nil {
		return PolicyRule{}, fmt.Errorf("policy rule source: %w", err)
	}
	destination, err := policyPeerFromWire(rule.Destination)
	if err != nil {
		return PolicyRule{}, fmt.Errorf("policy rule destination: %w", err)
	}
	converted := PolicyRule{Source: source, Destination: destination}
	for _, port := range rule.Ports {
		match, err := portMatchFromWire(port)
		if err != nil {
			return PolicyRule{}, err
		}
		converted.Ports = append(converted.Ports, match)
	}
	return converted, nil
}

func policyPeerFromWire(peer *apigen.NetPolicyPeer) (PolicyPeer, error) {
	if peer == nil {
		return PolicyPeer{}, fmt.Errorf("peer is nil")
	}
	if peer.SpaceID < 0 || peer.SpaceID > MaxSpaceID {
		return PolicyPeer{}, fmt.Errorf("space id %d is outside 0..%d", peer.SpaceID, MaxSpaceID)
	}
	if peer.DeploymentID < 0 || peer.DeploymentID > MaxDeploymentID {
		return PolicyPeer{}, fmt.Errorf("deployment id %d is outside 0..%d", peer.DeploymentID, MaxDeploymentID)
	}
	return PolicyPeer{SpaceID: peer.SpaceID, DeploymentID: peer.DeploymentID}, nil
}

func portMatchFromWire(port *apigen.NetPortMatch) (PortMatch, error) {
	if port == nil {
		return PortMatch{}, fmt.Errorf("port match is nil")
	}
	var protocol uint8
	switch port.Protocol {
	case apigen.NetProtocol_NET_PROTOCOL_TCP:
		protocol = unix.IPPROTO_TCP
	case apigen.NetProtocol_NET_PROTOCOL_UDP:
		protocol = unix.IPPROTO_UDP
	default:
		return PortMatch{}, fmt.Errorf("port match has invalid protocol %d", port.Protocol)
	}
	if port.Port < 1 || port.Port > 65535 {
		return PortMatch{}, fmt.Errorf("port %d is outside 1..65535", port.Port)
	}
	if port.PortEnd != 0 && (port.PortEnd < port.Port || port.PortEnd > 65535) {
		return PortMatch{}, fmt.Errorf("port range end %d is invalid for start %d", port.PortEnd, port.Port)
	}
	return PortMatch{Protocol: protocol, Port: uint16(port.Port), PortEnd: uint16(port.PortEnd)}, nil
}

func ValidateNetMapPolicyRule(rule *apigen.NetPolicyRule) error {
	_, err := policyRuleFromWire(rule)
	return err
}

func ValidateNetPortMatch(port *apigen.NetPortMatch) error {
	_, err := portMatchFromWire(port)
	return err
}
