export const fullExample = `deployment {
  name  = "payments-api"          # immutable once created
  space = space("prod")           # the space the deployment belongs to

  container {
    source {
      # exactly one of remote_image or nix_image_build
      remote_image {
        image = "ghcr.io/acme/payments:1.4.2"   # repository:tag or repository@sha256:...
      }
      # nix_image_build {
      #   repo    = "github.com/acme/payments"   # git repository
      #   flake   = "nix/flake.nix"              # path to flake.nix inside the repository
      #   target  = ".#image"                    # flake output; default: the flake's default output
      #   version = "3f2a9c1e0b7d4f6a8c2e1d3b5a7f9c0e2d4b6a8c"   # full 40-character commit sha
      # }
    }

    process {
      user        = "1000"                          # uid or user name the process runs as
      command     = ["/bin/payments", "serve"]      # replaces the image entrypoint
      working_dir = "/srv"                          # replaces the image working directory
    }

    env_vars = {
      "LOG_LEVEL"   = "info"                                 # literal
      "DB_PASSWORD" = secret("prod", "db/password")          # latest version of the secret
      "DB_URL"      = config("prod", "db/url", 3)            # pinned to version 3
      "RULES_FILE"  = asset("prod", "payments/rules.json")   # path of the asset inside the container
      "LEDGER_ADDR" = address("prod", "ledger")              # inbound address of another deployment
    }

    mounts = [
      mount(default_volume(), "/data"),                                            # persistent per-deployment volume
      mount(asset("prod", "payments/config.toml"), "/etc/payments/config.toml"),   # read-only file
      mount(deployment("prod", "ledger"), "/mnt/ledger", { read_only = true }),    # ledger's default volume
      mount(host_path("/srv/exports"), "/exports"),                                 # needs use_host_mounts
      mount(issued_tls(), "/etc/tls"),                                              # ca.crt, public.crt, private.key
    ]

    resources {
      dev_shm_size_kb       = 65536   # /dev/shm size in KiB; default: the runtime's
      file_descriptor_limit = 8192    # open files; default 2048
    }

    upgrade {
      strategy                  = "rollover"   # or "recreate" (default)
      readiness_timeout_seconds = 120          # default 600; see upgrade
    }
  }

  network {
    # mode = "virtual" is the default; "host" needs use_host_network and allows no ingress
    ingress {
      https {
        hostname       = "api.example.com"
        container_port = 8080
        path_prefix    = "/v1"        # default "/"
        strip_prefix   = false        # default false
        backend        = "http1"      # or "h2c"; default "http1"
        cert           = acme()       # default; or secret("prod", "tls/api")
        listen {
          node    = scheduled_node()  # default; or any_node(); node("name") must be the scheduled node today
          address = any_address()     # default; or ipv4(), ipv6(), "203.0.113.10", ["..."]
        }
      }
      tls_passthrough {
        hostname       = "db.example.com"
        container_port = 5432
        host_port      = 5433         # default 443
      }
      port_forward {
        protocol       = "udp"        # "tcp" or "udp"
        container_port = 9000
        host_port      = 9000         # default: container_port
        allow          = ["198.51.100.0/24"]   # default: everyone
      }
    }
  }

  scheduling {
    running = true                     # false keeps the config but runs nothing
    dedicated_nodes {
      nodes = [node("worker-1")]       # exactly one node; immutable once created
    }
  }
}
`;

export const minimalExample = `deployment {
  name  = "echo"
  space = space("prod")

  container {
    source {
      remote_image {
        image = "docker.io/library/nginx:1.27"
      }
    }
  }

  scheduling {
    running = true
    dedicated_nodes {
      nodes = [node("worker-1")]
    }
  }
}
`;

export const listenExamples = `listen {}                                       # the scheduled node, every address
listen { address = ipv4() }                     # the scheduled node, IPv4 addresses only
listen { node = any_node() }                    # every node that can reach the backend
listen {
  node    = node("edge-1")
  address = ["203.0.113.10", "2001:db8::10"]
}
`;

export const deploymentType = `type Deployment root {
    id: u64
    name: string
    space_id: Space.id
    spec: type DeploymentSpec {
        workload: type ContainerSpec {
            source: type NixImageBuild {
                repo: string
                flake: string
                target: string
            } | type RemoteImage {
                image: string
            }
            runtime: type ContainerRuntime {
                user: string
                env_vars: map<string, type LiteralEnv {
                    value: string
                } | type SecretEnv {
                    secret: SecretRef
                } | type ConfigEnv {
                    config: ConfigRef
                } | type AssetEnv {
                    key: string
                    asset: AssetRef
                } | type AddressEnv {
                    deployment_id: Deployment.id
                    space_id: Space.id
                }>
                override_command: []string
                override_working_dir: string
                default_volume: type DefaultVolumeMount {
                    container_path: string
                    disabled: bool
                }
                cross_deployment_mounts: []type CrossDeploymentMount {
                    deployment_id: Deployment.id
                    container_path: string
                    permission: FilePermission
                }
                mounts: []type HostMount {
                    host_path: string
                    container_path: string
                    permission: FilePermission
                }
                asset_mounts: []type AssetMount {
                    asset: AssetRef
                    container_path: string
                    permission: FilePermission
                }
                dev_shm_size_kb: ?i32
                file_descriptor_limit: ?i32
                issued_tls_mount: ?type IssuedTLSMount {
                    container_path: string
                    extra_names: []string
                    ca_only: bool
                }
            }
            version: string
            upgrade_strategy: enum ContainerUpgradeStrategy {
                Recreate
                Rollover
            }
            readiness_signal: ?type ContainerReadinessSignal {
                timeout_seconds: ?i32
            }
        }
        networking: type NetworkingConfig {
            mode: enum NetworkingMode {
                Virtual
                Host
            }
            port_forwarding: []type PortForward {
                protocol: enum PortForwardProtocol {
                    Tcp
                    Udp
                }
                host_port: i32
                container_port: i32
                ip_filter: []type IpFilter {
                    mode: enum IpFilterMode {
                        Allow
                        Deny
                    }
                    prefix: IPv4Prefix | IPv6Prefix
                }
            }
            ingress: []type Ingress {
                hostname: string
                listen: []type IngressListen {
                    node: ?(type AnyNode {
                    } | type SpecificNode {
                        node_id: Node.id
                    })
                    addresses: [](IPv4Prefix | IPv6Prefix)
                }
                config: type TlsPassthroughConfig {
                    host_port: ?i32
                    container_port: i32
                } | type HttpsConfig {
                    container_port: i32
                    path_prefix: string
                    strip_prefix: bool
                    backend_protocol: enum HttpBackendProtocol {
                        Http1
                        H2c
                    }
                    max_request_body_bytes: ?i64
                    flush_interval_ms: ?i32
                    cert_source: ?(type AcmeCertSource {
                        challenge: enum AcmeChallenge {
                            Http01
                        }
                    } | type SecretCertSource {
                        secret: SecretRef
                    })
                }
            }
        }
    }
    scheduling: type Scheduling {
        running: bool
        placement: type DedicatedNodesScheduling {
            nodes: []Node.id
        }
        restart_generation: u32
    }
}

enum FilePermission {
    ReadWrite
    ReadOnly
    ReadExecute
}

type IPv4Address {
    octets: [4]u8
}

type IPv6Address {
    octets: [16]u8
}

type IPv4Prefix {
    address: IPv4Address
    prefix_length: u8
}

type IPv6Prefix {
    address: IPv6Address
    prefix_length: u8
}

type SecretRef {
    secret_id: Secret.id
    version: u32
}

type ConfigRef {
    config_id: Config.id
    version: u32
}

type AssetRef {
    asset_id: Asset.id
    version: u32
}
`;
