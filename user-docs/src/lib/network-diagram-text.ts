export type DetailText = { title: string; body: string[] };
export type NodeText = { id: string; name: string; host: string; workloads: string[] };

export const legend = [
  { key: 'underlay', label: 'Underlay network', swatch: 'underlay' },
  { key: 'wg', label: 'WireGuard tunnel', swatch: 'wg' },
  { key: 'virtual', label: 'Virtual private network', swatch: 'virtual' },
  { key: 'flow:ew', label: 'East-west traffic', swatch: 'ew' },
  { key: 'flow:ingress', label: 'Public ingress', swatch: 'ingress' },
  { key: 'flow:egress', label: 'Egress (masqueraded)', swatch: 'egress' },
];

export function buildDetails(nodes: NodeText[]): Record<string, DetailText> {
  const details: Record<string, DetailText> = {
    _: {
      title: 'Networking at a glance',
      body: [
        'One private IPv6 network spans every node, carried over WireGuard on whatever underlay the nodes already have.',
        'Hover or click an item to explore it.',
      ],
    },
    underlay: {
      title: 'Underlay network',
      body: [
        'The network the nodes already have: LAN, VPC, or internet.',
        'OpenDeploy only needs one UDP port open between nodes.',
      ],
    },
    wg: {
      title: 'WireGuard tunnels',
      body: [
        'One device per node, one peer per remote node. Keys never leave the node.',
        'A peer may only send from the prefixes routed to it, so nodes cannot spoof each other.',
      ],
    },
    virtual: {
      title: 'Virtual private network',
      body: [
        'One ULA /48 per cluster: a /64 per space, a /88 per deployment, a /100 per instance.',
        'Addresses are derived from identity. Nothing is allocated or looked up.',
      ],
    },
  };
  for (const n of nodes) {
    details[n.id] = {
      title: n.name,
      body: [
        `Host address ${n.host} on the underlay, with a WireGuard peer for each other node.`,
        `Runs ${n.workloads.join(' and ')} in isolated network namespaces on the virtual network.`,
      ],
    };
  }

  return details;
}
