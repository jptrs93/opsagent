# Deployments

## Overview

OpenDeploy manages deployment configurations and deployment lifecycles. Users
create each deployment individually through typed protobuf API messages. The
system fetches available versions (git commits for Nix Docker builds or image
tags for container images) on demand, prepares images in containerd, and
supervises running containers with automatic crash recovery.

## Deployment config

Each deployment has two explicit steps:

- **`prepare`** — produces an image in containerd. Pick exactly one public variant.
- **`runner`** — runs the image. Public deployments use only the `container` runner; omitted runner config means all-default container settings.

A deployment is created by posting a `DeploymentCreateRequest` to
`POST /v1/deployment/create`:

```json
{
  "configId": {
    "name": "coflip_server",
    "environment": "PROD",
    "machine": "192.168.1.100"
  },
  "spec": {
    "prepare": {
      "nixDockerBuild": {
        "repo": "github.com/org/repo",
        "flake": "nix/server/flake.nix"
      }
    },
    "runner": {
      "container": {
        "user": "1000",
        "envVars": {
          "LOG_LEVEL": {"value": "info"},
          "DATABASE_URL": {"configId": 42},
          "DB_PASSWORD": {"secretId": 99}
        },
        "devShmSizeKb": 65536,
        "mounts": [{"host": "/home/ubuntu/coflip-server/data", "container": "/data"}]
      }
    }
  }
}
```

The spec of an existing deployment is updated by posting the typed `spec` field
in `POST /v1/deployment/update`; the `name`,
`environment`, and `machine` identity fields are fixed at create time and
cannot be changed through this path.

### Prepare variants

| Variant | Fields | Description |
|---|---|---|
| `nixDockerBuild` | `repo`, `flake` | Clones the repo, checks out the desired version, verifies `flake` exists in that checked-out tree, runs `nix build` without updating the lock file, expects the default output to be an executable OCI/Docker image stream such as `pkgs.dockerTools.streamLayeredImage`, imports that stream into OpenDeploy's bundled containerd, and returns the local image ref. Must be paired with the `container` runner. |
| `containerImage` | `image` | Pulls `image:version` (version is the desired tag/digest) into containerd's content store and unpacks it. Phase 1 pulls anonymously — no registry credentials. Must be paired with the `container` runner. |

`githubRelease` remains as an internal-only source for the `OPENDEPLOY`
self-deployment. Public create/update validation rejects it.

### Runner variants

| Variant | Fields | Description |
|---|---|---|
| `container` | `user`, `envVars`, `command`, `workingDir`, `dataMountPath`, `disableDataVolume`, `mounts`, `assetMounts`, `upgradeStrategy`, `readinessSignal`, `devShmSizeKb`, `fileDescriptorLimit` | Runs the prepared image as a container via containerd with OpenDeploy-supervised crash/backoff. Networking is controlled by `spec.networking`: host mode joins the host network namespace; virtual mode creates a per-container network namespace. `envVars` is a map from env var name to an `EnvVarValue` with exactly one of literal `value`, pinned `secretId`, pinned `configId`, or asset ref. Every container gets a default per-deployment host data volume at `/var/lib/opendeploy-volumes/{deploymentID}/default`, bind-mounted at `/data` (override with `dataMountPath`, opt out with `disableDataVolume`). `mounts` bind existing absolute host paths from the target machine into absolute container paths, read/write by default or read-only with `readonly: true`. `assetMounts` bind OpenDeploy-managed asset files read-only; set `executable: true` for read+execute script mounts. `upgradeStrategy` defaults to `RECREATE`; `ROLLOVER` starts a candidate container, waits for its Unix-socket readiness signal, promotes it, then stops the old container. `devShmSizeKb` optionally resizes the container's `/dev/shm` tmpfs in KiB; `fileDescriptorLimit` optionally overrides `RLIMIT_NOFILE`, with OpenDeploy otherwise defaulting containers to `2048` for both soft and hard limits. A dedicated binary log consumer writes merged half-hourly stdout/stderr records to `{RunOutputDir}/{deploymentID}/{YYYYMMDD_HHMM}_{version}_{run}.logbin`; `version` is the deployment configuration version and `run` is its restart sequence. `user` maps to the in-container OS user. Requires the `containerImage` or `nixDockerBuild` prepare. Linux only. |

`systemd` remains as an internal-only runner for the `OPENDEPLOY`
self-deployment. Public create/update validation rejects it, and public state
responses redact that runner config to an empty `runner` object.

### Networking

`spec.networking` controls container network mode and published ports.

- New create/update requests must set an explicit networking mode. Legacy specs without networking are migrated to explicit `HOST` on startup.
- `HOST` joins the host network namespace.
- `VIRTUAL` creates a per-container network namespace with a derived IPv6 instance address and machine-local IPv4 egress address.
- `portForwarding` publishes host-interface TCP or UDP ports to container ports through nftables DNAT and requires `VIRTUAL`, e.g. `{protocol: TCP, hostPort: 443, containerPort: 443}`.
- Virtual-mode deployments publish endpoint status for `.internal` DNS discovery through the per-machine netproxy deployment.
- Cross-machine virtual networking, policy, and public ingress are not implemented yet.

### Config versioning

Each deployment's `DeploymentConfig.Version` is a per-deployment
monotonically increasing integer that bumps on any spec or desired-state
change. Every bump is persisted to `deployment_config_history` so the UI
can reconstruct the sequence of changes.

## Deployment state

Each deployment's runtime state is structured into sections owned by different components:

### DesiredState

Set by user actions (deploy or stop). Contains the target `version` (commit hash or release tag) and a `running` boolean. Audit fields (`updated_at`, `updated_by`) and the config `version` are on the parent `DeploymentConfig`, not on `DesiredState` itself.

### PreparerStatus

Driven by the preparer. Tracks prepare progress with status values:
`PREPARING`, `DOWNLOADING`, `READY`, `FAILED`. On success, contains the
resolved `artifact` (local image ref) and the `deployment_config_version`
from `DeploymentConfig.Version`.

### RunnerStatus

Driven by the runner. Tracks the running container task with `running_pid`,
`running_artifact`, `status` (`NO_DEPLOYMENT`, `RUNNING`, `STOPPED`, `STARTING`,
`CRASHED`), `deployment_config_version`, `number_of_restarts`, and `last_restart_at`.

## Deployment identification

Each deployment has an integer `id` (primary key) assigned when the deployment is first created via `POST /v1/deployment/create`. The `DeploymentIdentifier{environment, machine, name}` tuple is human-readable metadata stored on `DeploymentConfig.ConfigID`. All API requests, storage keys, and log file paths use the integer `id`.

## Deployment status display

The status page shows one card per deployment, sorted with
OPENDEPLOY last, then by environment, name, machine, and id. Each
card carries a per-environment tinted background and displays:

- Deployment name with history link
- Status badge (Running/Stopped/Starting/Crashed/No Deployment) — clickable to view run output
- Stop/Start buttons
- Two-column info panel: deployment info (deployed by, deployed at, version) and runtime info (restart count, last restart time)
- Prepare status with link to prepare output (build/import/pull log)
- "Update" button that opens an overlay for version selection and optional deployment spec edits

## Deploy workflow

1. The user clicks "Update" on a card. The overlay fetches available
   versions via `POST /v1/deployment/versions` or source validation — 25 most
   recent commits per Nix scope (branches), or available image tags.
2. The user picks a version (and optionally edits the deployment spec) and submits.
3. The frontend calls `POST /v1/deployment/update` with the target version
   and, if the spec was edited, the new typed `spec`.
4. The backend writes the new spec (if any), sets `DesiredState`
   (version, running=true), and bumps `DeploymentConfig.Version`.
5. The operator's reconciliation loop picks up the change and starts a
   preparer.
6. The preparer clones/fetches, pulls, or imports the image, then writes
   `PreparerStatus.Status = READY`.
7. The operator creates a runner, which writes `RunnerStatus.Status =
   STARTING` then `RUNNING` with the PID.

For container deployments with `upgradeStrategy = ROLLOVER`, step 7 starts a candidate container before stopping the old one. The candidate receives `OPENDEPLOY_READINESS_SOCK_PATH=/run/opendeploy/readiness.sock`; once warmup is complete it writes `ready\n` to that Unix socket. With virtual networking, the candidate can bind the same container port immediately in its own network namespace; OpenDeploy promotes it by flipping the stable-address host route and any `portForwarding` rules, then stops the old container. With host networking, rollover is cooperative: the candidate shares the host network namespace with the old container, so it must not bind conflicting host ports before signaling readiness. After readiness, it should wait until OpenDeploy stops the old container and the port becomes free, then bind and serve.

## Crash recovery

The container runner owns crash recovery directly: on task exit it writes
`RunnerStatus.Status = CRASHED`, sleeps for an exponentially increasing delay
(1s to 60s, doubling per consecutive crash), and respawns the same image.
`number_of_restarts` increments on each respawn and resets on new deployments.
If the container runs stably for 15+ seconds before crashing, the local crash
counter is reset.

## Deployment history

The history sidebar shows a chronological log of all deployment config and status changes. Config entries show the version number and what changed (version deployed, running toggled, deleted). Status entries show preparer and runner state transitions (diff-rendered against the previous entry so unchanged sections aren't repeated). All entries are fetched via `POST /v1/deployment/history` with the integer deployment ID. History is stored in `deployment_config_history` (PK `deployment_id, version`) and `deployment_status_history` (PK `deployment_id, status_seq_no`) — the composite primary keys already cover `deployment_id`-leading lookups.

## Empty state

When no deployments exist, the status page displays "No deployments configured. Create a deployment config first." Clicking "Add deployment" opens the typed deployment form.
