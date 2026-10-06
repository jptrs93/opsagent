/// Rendered, node-local artifacts. Nothing in this module is appended to a
/// log: each value is a pure function of the core module's state plus the
/// host facts named on its fields, and is rebuilt from those inputs after any
/// loss. There is one NetState per node, written by that node's agent for its
/// own netproxy. Tags match the netstate.pb file on disk, which outlives an
/// agent upgrade.
import core "opendeploy"

type NetState {
    /// Node-local artifact counter. Netproxy ignores a file whose seq is not
    /// above the last one it applied.
    seq@1: i64
    /// Equals core.SystemConfig.network_ula_prefix.
    ula_prefix@2: [6]u8
    /// Equals core.Node.reported.identifier of the rendering node.
    node_identifier@3: string
    /// One entry per virtual-mode deployment in the cluster, from the cluster
    /// net map's DNS catalog. A service with no established ordinal keeps its
    /// entry with no endpoints so the DNS answer is authoritative.
    dns_services@4: []type DnsService {
        name@1: string
        environment@2: string
        endpoints@3: []type Endpoint {
            reserve @3, @4, @5
            ordinal@1: u32
            address@2: core.IPv6Address
        }
    }
    /// Host fact: the node's upstream DNS servers.
    upstream_resolvers@5: []string
    /// One route per (kind, hostname, host port or path prefix) of the
    /// virtual-mode deployments scheduled on this node. Backends are the
    /// established endpoints of the route's deployment on any node.
    ingress@6: []type NetIngress {
        hostname@1: string
        config@2: type TlsPassthroughNetIngress@1 {
            host_port@1: u32 [max = 65535, min = 1]
            backends@2: []IngressBackend
        } | type HttpsNetIngress@2 {
            path_prefix@1: string
            strip_prefix@2: bool
            backend_protocol@3: core.Deployment.DeploymentSpec.NetworkingConfig.Ingress.HttpsConfig.HttpBackendProtocol
            max_request_body_bytes@4: ?u64 [min = 1]
            flush_interval_ms@5: ?u32 [min = 1]
            cert_id@6: string
            backends@7: []IngressBackend
        }
    }
    /// From the primary's ACME state, forwarded on the cluster stream.
    acme_challenges@7: []type AcmeHttpChallenge {
        token@1: string
        key_authorization@2: string
    }
}

type IngressBackend {
    address@1: core.IPv6Address
    port@2: u32 [max = 65535, min = 1]
}

/// The certificate file beside NetState, with its own counter. Private keys
/// are resolved by netproxy from the secret store and never enter either file.
type CertBundle {
    seq@1: i64
    certs@2: []type CertBundleEntry {
        /// "acme:<hostname>" or "secret:<secret id>", matching
        /// HttpsNetIngress.cert_id.
        cert_id@1: string
        pem@2: string
    }
}

/// Cluster-wide placement and underlay map, rendered on the primary from
/// every member node, every live scheduled instance, and the policy table,
/// and sent whole on the cluster stream. Content is identical for every
/// node; only target_node_id differs. A node accepts a map only if its stamp
/// is above the one it holds, and acks it with NetMapStatus.
type ClusterNetMap {
    /// The node this copy was sent to.
    target_node_id@1: core.Node.id
    /// Equals core.SystemConfig.network_ula_prefix.
    ula_prefix@2: [6]u8
    /// Every member node with a WireGuard key, in node id order.
    nodes@3: []type ClusterNetMapNode {
        node_id@1: core.Node.id
        /// core.Node.reported.underlay_address.
        underlay_address@2: ?(core.IPv4Address@1 | core.IPv6Address@2)
        wg_public_key@3: string
        wg_listen_port@4: u32 [max = 65535, min = 1]
        /// The host DNAT set this node forwards to its netproxy, from the
        /// ingress listen selectors of every deployment. An absent address
        /// publishes on every local address.
        ingress_publish@5: []type IngressPublish {
            address@1: ?(core.IPv4Address@1 | core.IPv6Address@2)
            port@2: u32 [max = 65535, min = 1]
        }
    }
    /// One route per live placement: the /120 of each scheduled instance
    /// that wants to run, and the /100 of each ordinal at its serving node.
    routes@4: []type ClusterNetMapRoute {
        logical_prefix@1: core.IPv6Prefix
        hosting_node_id@2: core.Node.id
    }
    /// The primary's write seq at render time. Nodes report it back as
    /// persisted_seq and applied_seq; the rollover barrier waits on it.
    derived_from_seq@5: i64
    /// core.NetworkPolicy rows with each peer resolved to (space, deployment).
    policy_rules@6: []type NetPolicyRule {
        source@1: type NetPolicyPeer {
            space_id@1: core.Space.id
            /// Absent means the whole space.
            deployment_id@2: ?core.Deployment.id
        }
        destination@2: NetPolicyRule.NetPolicyPeer
        /// Empty means all ports and protocols.
        ports@3: []core.NetworkPolicy.NetPortMatch
    }
    /// One entry per virtual-mode deployment with a live placement, with the
    /// ordinals that are established: serving, or standby plus draining.
    dns_services@7: []type ClusterNetMapService {
        name@1: string
        space_id@2: core.Space.id
        deployment_id@3: core.Deployment.id
        ordinals@4: []u32
    }
    laws {
        one_host_per_prefix {
            assert all r in routes : (count o in routes where o.logical_prefix == r.logical_prefix) == 1
        }
    }
}
