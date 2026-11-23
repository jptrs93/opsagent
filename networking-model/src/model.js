export const layers = {
  all: {
    eyebrow: "THE BIG PICTURE",
    title: "One network. Every node.",
    description:
      "A private IPv6 network connects workloads across the cluster. WireGuard carries cross-node traffic; ingress is the way in from the public world.",
    color: "#c4cbd6",
    label: "All layers",
  },
  underlay: {
    eyebrow: "01 / TRANSPORT",
    title: "Connected underneath.",
    description:
      "WireGuard links connect the nodes and carry the private network. Every cross-node packet is encrypted and authenticated by the transport.",
    color: "#a798f5",
    label: "Underlay",
    tag: "WireGuard",
    facts: [
      "One WireGuard device per node",
      "A peer for each remote node",
      "Encrypted across node boundaries",
    ],
  },
  ipv6: {
    eyebrow: "02 / PRIVATE NETWORK",
    title: "IPv6, end to end.",
    description:
      "Workloads share one logical IPv6 network. Between nodes, their packets travel inside WireGuard; the private addresses stay the same.",
    color: "#70dfbd",
    label: "Virtual IPv6",
    tag: "Cluster network",
    facts: [
      "One private ULA /48 for the cluster",
      "Native routing between workloads",
      "Carried inside the WireGuard transport",
    ],
  },
  ingress: {
    eyebrow: "03 / PUBLIC INGRESS",
    title: "A deliberate way in.",
    description:
      "In this HTTPS example, public traffic reaches a node’s ingress proxy. The proxy connects to a workload over private IPv6, even when it lives on another node.",
    color: "#f4b77c",
    label: "Public ingress",
    tag: "HTTPS example",
    facts: [
      "Public connection arrives at node 01",
      "The ingress proxy selects a backend",
      "Private IPv6 reaches the remote workload",
    ],
  },
};

export const nodes = [
  {
    id: "node-01",
    name: "Node 01",
    service: "Ingress + web",
    detail:
      "This node accepts the illustrated public HTTPS connection. Its ingress proxy can reach a backend on any node through the private network.",
  },
  {
    id: "node-02",
    name: "Node 02",
    service: "API workload",
    detail:
      "The remote backend in this example. Private IPv6 traffic arrives inside WireGuard, then the kernel routes the decrypted packet to the workload.",
  },
  {
    id: "node-03",
    name: "Node 03",
    service: "Worker workload",
    detail:
      "A third peer on the same logical network. It has direct WireGuard connectivity to both other nodes and participates in the private IPv6 network.",
  },
];
