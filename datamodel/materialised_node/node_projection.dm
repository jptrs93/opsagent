import core "opendeploy"

type NodeProjection mat {
    seq@1: i64
    instances@2: []NodeInstance
    net_map@3: ?ClusterNetMap
    acme@4: ?NodeAcmeState
}

type NodeInstance {
    instance@1: core.ScheduledInstance
    config@2: core.Deployment
    status_watermark@3: ?i64
}

type NodeAcmeState {
    cert_bindings@1: []type AcmeCertBinding {
        hostname@1: string
        secret@2: core.SecretRef
    }
    challenges@2: []type AcmeHttpChallenge {
        hostname@1: string
        token@2: string
        key_authorization@3: string
    }
}

type ClusterNetMap {
    target_node_id@1: core.Node.id
    ula_prefix@2: [6]u8
    nodes@3: []type ClusterNetMapNode {
        node_id@1: core.Node.id
        underlay_address@2: ?(core.IPv4Address@1 | core.IPv6Address@2)
        wg_public_key@3: string
        wg_listen_port@4: u32 [max = 65535, min = 1]
        ingress_publish@5: []type IngressPublish {
            address@1: ?(core.IPv4Address@1 | core.IPv6Address@2)
            port@2: u32 [max = 65535, min = 1]
        }
    }
    routes@4: []type ClusterNetMapRoute {
        logical_prefix@1: core.IPv6Prefix
        hosting_node_id@2: core.Node.id
    }
    derived_from_seq@5: i64
    policy_rules@6: []type NetPolicyRule {
        source@1: type NetPolicyPeer {
            space_id@1: core.Space.id
            deployment_id@2: ?core.Deployment.id
        }
        destination@2: NetPolicyRule.NetPolicyPeer
        ports@3: []core.NetworkPolicy.NetPortMatch
    }
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
