# Proposed deployment HCL schema

This is an authoring format for `DeploymentCreateRequest` and the editable
parts of `DeploymentConfig`. Server-owned fields (`id`, config version, audit
metadata, and deletion state) are deliberately excluded.

```hcl
deployment {
  node = node("<name>") # required, resolves to node_id

  identity {
    name  = string # required
    space = space("<name>") # required, resolves to identity.space_id
  }

  # Repeatable to support pod-like multi-container deployments in the future.
  container {
    source {
      container_image {
        image = string
      }

      # Or:
      nix_docker_build {
        repo   = string
        flake  = string
        target = string # optional local selector beginning with .#
      }
    }

    process {
      user        = string
      command     = list(string)
      working_dir = string
    }

    env_vars {
      LITERAL_VALUE = string
      SECRET_VALUE  = secret("<name>"[, { version = number }])
      CONFIG_VALUE  = config("<name>"[, { version = number }])
      ASSET_VALUE   = asset("<name>"[, { version = number }])
      ADDRESS_VALUE = address("<space-name>", "<deployment-name>")
    }

    mounts = [
      mount(default_volume(), string),
      mount(asset("<asset-name>"[, { version = number }]), string),
      mount(asset("<asset-name>"[, { version = number }]), string, {
        executable = bool
      }),
      mount(deployment("<space-name>", "<deployment-name>"), string, {
        read_only = bool
      }),
    ]

    resources {
      dev_shm_size_kb       = number
      file_descriptor_limit = number
    }

    upgrade {
      strategy                  = string # "recreate" or "rollover"
      readiness_timeout_seconds = number
    }

    version = string
  }

  network {
    mode = "virtual" # or "host"

    ingress = [
      port_forward("tcp", container_port),
      port_forward("udp", container_port, {
        host_port = number # optional; defaults to container_port
      }),
      tls_passthrough("hostname", container_port),
      tls_passthrough("hostname", container_port, {
        host_port = number # optional; defaults to 443
      }),
    ]
  }

  desired_running = true # or false
}
```

Singleton concepts use unlabelled blocks. Mounts and ingress routes use lists
of composable expressions. The source block contains exactly one source
variant. `port_forward` and `tls_passthrough` remain distinct typed values even
though they map to different branches of the current protobuf networking
model.

Every symbolic resource is a typed function with a quoted name:
`space("production")`, `node("worker_03")`, `asset("payments.settings")`, and
`deployment("production", "report.archive")`. Environment variables use the
same reference functions. Deployment and address references explicitly name
their space, for example `address("production", "redis.cache")`, so they remain
globally addressable without nesting another reference function. Quoted names
keep dots and other punctuation unambiguous.

`address(...)` resolves the target deployment's stable inbound virtual address
`I`. Run-scoped preferred outbound addresses `O` belong to container-run
lifecycle and are not exposed in authored HCL.

Assets, secrets, and configs accept an optional `{ version = number }` argument.
Omitting it resolves the latest version in the selected space. Existing
deployment documents emit the options object so their immutable references
remain visibly pinned.

The editor resolves references against the deployment's space when saving and
emits the immutable ID expected by the protobuf API. Numeric IDs are not part
of the authored HCL format. The authoring model should retain both the symbolic
key and resolved ID so the HCL can round-trip even though the current API
stores only the ID.

The current protobuf has one deployment source, runner configuration, and
desired version. The HCL keeps those concerns directly under `container`. The
repeated `container` boundary intentionally anticipates a future pod-like
deployment model; until then, save-time validation can require exactly one
container.
