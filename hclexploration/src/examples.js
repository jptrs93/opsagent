export const containerExample = `# A complete service deployment
deployment {
  node = node("worker_03")

  identity {
    name  = "payments-api"
    space = space("production")
  }

  container {
    source {
      container_image {
        image = "ghcr.io/acme/payments-api"
      }
    }

    process {
      user        = "1000:1000"
      command     = ["/app/server", "--listen", ":8080"]
      working_dir = "/app"
    }

    env_vars {
      LOG_LEVEL         = "info"
      DATABASE_URL      = config("database.url", { version = 5 })
      DATABASE_PASSWORD = secret("database.password")
      CACHE_ADDRESS     = address("production", "redis.cache")
      LICENSE_FILE      = asset("application.license")
    }

    mounts = [
      mount(default_volume(), "/var/lib/payments"),
      mount(asset("payments_settings"), "/etc/payments/settings.toml"),
      mount(asset("application.license"), "/etc/payments/license.key"),
      mount(deployment("production", "report.archive"), "/archive", {
        read_only = true
      }),
    ]

    resources {
      dev_shm_size_kb       = 65536
      file_descriptor_limit = 4096
    }

    upgrade {
      strategy                  = "rollover"
      readiness_timeout_seconds = 90
    }

    version = "2026.07.18"
  }

  network {
    mode = "virtual"

    ingress = [
      port_forward("tcp", 8080),
      port_forward("udp", 5353),
      tls_passthrough("payments.example.com", 8443),
      tls_passthrough("payments-admin.example.com", 9443, {
        host_port = 444
      }),
    ]
  }

  desired_running = true
}
`;

export const nixExample = `# Build an OCI image from a Nix flake output
deployment {
  node = node("worker_02")

  identity {
    name  = "report-worker"
    space = space("production")
  }

  container {
    source {
      nix_docker_build {
        repo   = "github.com/acme/reporting"
        flake  = "nix/worker/flake.nix"
        target = ".#reportWorkerImage"
      }
    }

    env_vars {
      QUEUE_NAME    = "reports"
      CACHE_ADDRESS = address("production", "redis.cache")
    }

    mounts = [
      mount(asset("worker_settings"), "/etc/report-worker/settings.toml"),
    ]

    version = "9e82f44"
  }

  network {
    mode = "host"
  }

  desired_running = true
}
`;

export const examples = {
    container: containerExample,
    nix: nixExample,
};
