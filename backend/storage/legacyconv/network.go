package legacyconv

import (
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/apigenold"
)

// NetworkPolicy keeps allow policies; the old deny action has no case in the
// new model and is refused.
func NetworkPolicy(old *apigenold.NetworkPolicy) (*apigen.NetworkPolicy, error) {
	c := &conv{}
	if old == nil {
		c.refuse("NetworkPolicy", "", "nil payload")
		return checked("NetworkPolicy", (*apigen.NetworkPolicy)(nil), c.err)
	}
	if old.Action == apigenold.NetworkPolicyAction_NETWORK_POLICY_ACTION_DENY {
		c.refuse("NetworkPolicy", "action", "deny has no case in the new model")
	}
	out := &apigen.NetworkPolicy{
		ID:          c.id("NetworkPolicy", "id", int64(old.ID)),
		Action:      apigen.NetworkPolicyAction(old.Action),
		Source:      c.policyPeer("NetworkPolicy", "source", old.Source),
		Destination: c.policyPeer("NetworkPolicy", "destination", old.Destination),
		Ports:       c.portMatches("NetworkPolicy", old.Ports),
	}
	return checked("NetworkPolicy", out, c.err)
}

func (c *conv) policyPeer(typ, field string, old *apigenold.NetworkPolicyPeerRef) apigen.NetworkPolicyPeer {
	if old == nil {
		c.refuse(typ, field, "unset peer")
		return apigen.NetworkPolicyPeer{}
	}
	var target apigen.NetworkPolicyPeerTargetValueOneof
	switch old.Kind {
	case apigenold.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE:
		target.Space = &apigen.SpacePeer{SpaceID: c.id(typ, field, int64(old.ID))}
	case apigenold.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_DEPLOYMENT:
		target.Deployment = &apigen.DeploymentPeer{DeploymentID: c.id(typ, field, int64(old.ID))}
	default:
		c.refuse(typ, field+".kind", "unsupported value %d", old.Kind)
	}
	return apigen.NetworkPolicyPeer{Target: apigen.NetworkPolicyPeerTarget{Value: target}}
}

func (c *conv) portMatches(typ string, old []*apigenold.NetPortMatch) []apigen.NetPortMatch {
	var out []apigen.NetPortMatch
	for _, p := range old {
		if p == nil {
			c.refuse(typ, "ports", "nil entry")
			continue
		}
		end := p.PortEnd
		if end == 0 {
			end = p.Port
		}
		out = append(out, apigen.NetPortMatch{
			Protocol: apigen.NetProtocol(p.Protocol),
			Range:    apigen.PortRange{Start: c.u32(typ, "ports.port", int64(p.Port)), End: c.u32(typ, "ports.port_end", int64(end))},
		})
	}
	return out
}

func ClusterNetworkInfo(old []byte) (*apigen.ClusterNetworkInfo, error) {
	decoded, err := apigenold.DecodeClusterNetworkInfo(old)
	if err != nil {
		return nil, fmt.Errorf("ClusterNetworkInfo: decode: %w", err)
	}
	return checked("ClusterNetworkInfo", &apigen.ClusterNetworkInfo{UlaPrefix: decoded.UlaPrefix}, nil)
}

func ClusterNetMap(old []byte) (*apigen.ClusterNetMap, error) {
	decoded, err := apigenold.DecodeClusterNetMap(old)
	if err != nil {
		return nil, fmt.Errorf("ClusterNetMap: decode: %w", err)
	}
	c := &conv{}
	out := &apigen.ClusterNetMap{
		TargetNodeID:   c.id("ClusterNetMap", "target_node_id", int64(decoded.TargetNodeID)),
		UlaPrefix:      decoded.UlaPrefix,
		DerivedFromSeq: decoded.DerivedFromSeq,
	}
	for _, n := range decoded.Nodes {
		if n == nil {
			c.refuse("ClusterNetMap", "nodes", "nil entry")
			continue
		}
		node := apigen.ClusterNetMapNode{
			NodeID:          c.id("ClusterNetMapNode", "node_id", int64(n.NodeID)),
			UnderlayAddress: n.UnderlayAddress,
			WgPublicKey:     n.WgPublicKey,
			WgListenPort:    c.u32("ClusterNetMapNode", "wg_listen_port", int64(n.WgListenPort)),
		}
		for _, p := range n.IngressPublish {
			if p == nil {
				c.refuse("ClusterNetMapNode", "ingress_publish", "nil entry")
				continue
			}
			node.IngressPublish = append(node.IngressPublish, apigen.IngressPublish{Address: p.Address, Port: c.u32("IngressPublish", "port", int64(p.Port))})
		}
		out.Nodes = append(out.Nodes, node)
	}
	for _, r := range decoded.Routes {
		if r == nil {
			c.refuse("ClusterNetMap", "routes", "nil entry")
			continue
		}
		out.Routes = append(out.Routes, apigen.ClusterNetMapRoute{LogicalPrefix: r.LogicalPrefix, HostingNodeID: c.id("ClusterNetMapRoute", "hosting_node_id", int64(r.HostingNodeID))})
	}
	for _, r := range decoded.PolicyRules {
		if r == nil {
			c.refuse("ClusterNetMap", "policy_rules", "nil entry")
			continue
		}
		out.PolicyRules = append(out.PolicyRules, apigen.NetPolicyRule{
			Source:      c.netPolicyPeer("source", r.Source),
			Destination: c.netPolicyPeer("destination", r.Destination),
			Ports:       c.portMatches("NetPolicyRule", r.Ports),
		})
	}
	for _, s := range decoded.DnsServices {
		if s == nil {
			c.refuse("ClusterNetMap", "dns_services", "nil entry")
			continue
		}
		svc := apigen.ClusterNetMapService{
			Name:         s.Name,
			SpaceID:      c.id("ClusterNetMapService", "space_id", int64(s.SpaceID)),
			DeploymentID: c.id("ClusterNetMapService", "deployment_id", int64(s.DeploymentID)),
		}
		for _, o := range s.Ordinals {
			if o == nil {
				c.refuse("ClusterNetMapService", "ordinals", "nil entry")
				continue
			}
			svc.Ordinals = append(svc.Ordinals, apigen.ClusterNetMapServiceOrdinal{Ordinal: c.u32("ClusterNetMapServiceOrdinal", "ordinal", int64(o.Ordinal))})
		}
		out.DnsServices = append(out.DnsServices, svc)
	}
	return checked("ClusterNetMap", out, c.err)
}

func (c *conv) netPolicyPeer(field string, old *apigenold.NetPolicyPeer) apigen.NetPolicyPeer {
	if old == nil {
		c.refuse("NetPolicyRule", field, "unset peer")
		return apigen.NetPolicyPeer{}
	}
	return apigen.NetPolicyPeer{
		SpaceID:      c.id("NetPolicyPeer", field+".space_id", int64(old.SpaceID)),
		DeploymentID: c.id("NetPolicyPeer", field+".deployment_id", int64(old.DeploymentID)),
	}
}
