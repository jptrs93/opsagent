import core "opendeploy"

type NetState {
    seq@1: i64
    ula_prefix@2: [6]u8
    node_identifier@3: string
    dns_services@4: []type DnsService {
        name@1: string
        environment@2: string
        endpoints@3: []type Endpoint {
            reserve @3, @4, @5
            ordinal@1: u32
            address@2: core.IPv6Address
        }
    }
    upstream_resolvers@5: []string
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
    acme_challenges@7: []type AcmeHttpChallenge {
        token@1: string
        key_authorization@2: string
    }
}

type IngressBackend {
    address@1: core.IPv6Address
    port@2: u32 [max = 65535, min = 1]
}

type CertBundle {
    seq@1: i64
    certs@2: []type CertBundleEntry {
        cert_id@1: string
        pem@2: string
    }
}
