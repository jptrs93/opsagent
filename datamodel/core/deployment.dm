type Deployment@1 root {
    id@1: u64 [key]
    name@2: string [min_length = 1]
    space_id@3: Space.id
    spec@4: type DeploymentSpec {
        workload@1: type ContainerSpec@1 {
            source@1: type NixImageBuild@1 {
                repo@1: string
                flake@2: string
                target@3: string
            } | type RemoteImage@2 {
                image@1: string
            }
            runtime@2: type ContainerRuntime {
                user@1: string
                env_vars@2: map<string, type LiteralEnv@1 {
                    value@1: string
                } | type SecretEnv@2 {
                    secret@1: SecretRef
                } | type ConfigEnv@3 {
                    config@1: ConfigRef
                } | type AssetEnv@4 {
                    key@1: string
                    asset@2: AssetRef
                } | type AddressEnv@5 {
                    deployment_id@1: Deployment.id
                    space_id@2: Space.id
                }>
                override_command@3: []string
                override_working_dir@4: string
                default_volume@5: type DefaultVolumeMount {
                    container_path@1: string
                    disabled@2: bool
                }
                cross_deployment_mounts@6: []type CrossDeploymentMount {
                    deployment_id@1: Deployment.id
                    container_path@2: string
                    permission@3: FilePermission
                }
                mounts@7: []type HostMount {
                    host_path@1: string
                    container_path@2: string
                    permission@3: FilePermission
                }
                asset_mounts@8: []type AssetMount {
                    asset@1: AssetRef
                    container_path@2: string
                    permission@3: FilePermission
                }
                dev_shm_size_kb@9: ?u32 [min = 1]
                file_descriptor_limit@10: ?u32 [min = 1]
                issued_tls_mount@11: ?type IssuedTLSMount {
                    container_path@1: string
                    extra_names@2: []string
                    ca_only@3: bool
                }
            }
            version@3: string
            upgrade_strategy@4: enum ContainerUpgradeStrategy {
                Recreate@1
                Rollover@2
            }
            readiness_signal@5: ?type ContainerReadinessSignal {
                timeout_seconds@1: ?u32 [min = 1]
            }
            laws {
                rollover_needs_readiness {
                    assert upgrade_strategy != Rollover || readiness_signal is present
                }
            }
        }
        networking@2: type NetworkingConfig {
            mode@1: enum NetworkingMode {
                Virtual@1
                Host@2
            }
            port_forwarding@2: []type PortForward {
                protocol@1: enum PortForwardProtocol {
                    Tcp@1
                    Udp@2
                }
                host_port@2: u32 [max = 65535, min = 1]
                container_port@3: u32 [max = 65535, min = 1]
                ip_filter@4: []type IpFilter {
                    mode@1: enum IpFilterMode {
                        Allow@1
                        Deny@2
                    }
                    prefix@2: IPv4Prefix@1 | IPv6Prefix@2
                }
            }
            ingress@3: []type Ingress {
                hostname@1: string
                listen@2: []type IngressListen {
                    node@1: ?(type AnyNode@1 {
                    } | type SpecificNode@2 {
                        node_id@1: Node.id
                    })
                    addresses@2: [](IPv4Prefix@1 | IPv6Prefix@2)
                }
                config@3: type TlsPassthroughConfig@1 {
                    host_port@1: ?u32 [max = 65535, min = 1]
                    container_port@2: u32 [max = 65535, min = 1]
                } | type HttpsConfig@2 {
                    container_port@1: u32 [max = 65535, min = 1]
                    path_prefix@2: string
                    strip_prefix@3: bool
                    backend_protocol@4: enum HttpBackendProtocol {
                        H2c@1
                        Http1@2
                    }
                    max_request_body_bytes@5: ?u64 [min = 1]
                    flush_interval_ms@6: ?u32 [min = 1]
                    cert_source@7: ?(type AcmeCertSource@1 {
                        challenge@1: enum AcmeChallenge {
                            Http01@1
                        }
                    } | type SecretCertSource@2 {
                        secret@1: SecretRef
                    })
                }
            }
            laws {
                port_forwarding_needs_virtual {
                    forbid mode == Host && !port_forwarding.is_empty()
                }
                ingress_needs_virtual {
                    forbid mode == Host && !ingress.is_empty()
                }
            }
        }
    }
    scheduling@5: type Scheduling {
        running@1: bool
        placement@2: type DedicatedNodesScheduling@1 {
            nodes@1: []Node.id [max_items = 1, min_items = 1, mutable = false]
        }
        restart_generation@3: u32
    }
    laws {
        placement_nodes_have_space_access where scheduling.placement is DedicatedNodesScheduling p {
            assert all n in p.nodes : space_id in n->operator.allowed_spaces
        }
        placement_nodes_are_members where scheduling.placement is DedicatedNodesScheduling p {
            assert all n in p.nodes : n->status in [MemberNormal, MemberUnhealthy, MemberDraining, MemberMissing]
        }
        address_refs_resolve where spec.workload is ContainerSpec c {
            assert none e in c.runtime.env_vars : e is AddressEnv a && (a.deployment_id == id || a.deployment_id->spec.networking.mode != Virtual || a.space_id != a.deployment_id->space_id)
        }
        cross_deployment_mounts_resolve where spec.workload is ContainerSpec c && scheduling.placement is DedicatedNodesScheduling p {
            assert all m in c.runtime.cross_deployment_mounts : m.deployment_id != id && m.deployment_id->scheduling.placement is DedicatedNodesScheduling q && q.nodes.first() == p.nodes.first() && m.deployment_id->spec.workload is ContainerSpec s && !s.runtime.default_volume.disabled
        }
    }
}

laws deployment_and_scheduling {
    deployment_identity (a: Deployment, b: Deployment, space: Space) where a.space_id == space.id && b.space_id == space.id && a.id != b.id && a.name == b.name {
        forbid a.scheduling.placement is DedicatedNodesScheduling pa && b.scheduling.placement is DedicatedNodesScheduling pb && pa.nodes.first() == pb.nodes.first()
    }
}
