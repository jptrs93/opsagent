# Deployments

## Overview

OpenDeploy manages deployment configurations and deployment lifecycles. Users
create each deployment individually through typed protobuf API messages. The
system fetches available versions (git commits for Nix Docker builds or image
tags for container images) on demand, prepares images in containerd, and
supervises running containers with automatic crash recovery.

## Deployment config

Each deployment currently has exactly one workload. Public deployments use
`spec.container1Spec`, which contains one artifact `source`, its `runtime`
configuration, and workload-local `version` and `running` desired state.

A deployment is created by posting a `DeploymentCreateRequest` to
`POST /v1/deployment/create`:

```json
{
  "identity": {
    "name": "coflip_server",
    "spaceId": 1
  },
  "nodeId": 1,
  "spec": {
    "networking": {"mode": 1},
    "container1Spec": {
      "source": {
        "nixDockerBuild": {
          "repo": "github.com/org/repo",
          "flake": "nix/server/flake.nix"
        }
      },
      "runtime": {
        "user": "1000",
        "envVars": {
          "LOG_LEVEL": {"value": "info"},
          "DATABASE_URL": {"configId": 42},
          "DB_PASSWORD": {"secretId": 99}
        },
        "devShmSizeKb": 65536,
        "mounts": [{"hostPath": "/home/ubuntu/coflip-server/data", "containerPath": "/data", "permission": 1}]
      },
      "version": "0123456789abcdef0123456789abcdef01234567",
      "running": true
    }
  }
}
```

The spec of an existing deployment is updated by posting the typed `spec` field
in `POST /v1/deployment/update`. Its name and `nodeId` placement are fixed at
creation; its space can be changed through the update path.

`nodeId` is the required canonical placement and references `ClusterNode.id`.
Every stored deployment has a positive canonical `nodeId`. Deployment history
entries carry the deployment's current identity and node placement as display
metadata.

### Source variants

| Variant | Fields | Description |
|---|---|---|
| `nixDockerBuild` | `repo`, `flake`, optional `target` | Uses a full commit hash as the desired version. `flake` must be a safe repository-relative path whose basename is `flake.nix` and whose entry at that exact commit is a regular Git file. Before a running config is saved, the primary contacts the remote and verifies the exact repository-wide commit and flake entry. The preparer checks out the commit, rechecks the file, runs `nix build` without updating the lock file, and expects its selected output to be an executable OCI/Docker image stream such as `pkgs.dockerTools.streamLayeredImage`. An empty target builds the default output; a local selector such as `.#radkitRpaClientImage` selects a named flake output. The stream is imported into OpenDeploy's bundled containerd and returned as a local image ref. Must be paired with the `container` runner. |
| `remoteImage` | `image` | Pulls `image:version` (version is the workload's desired tag/digest) into containerd's content store and unpacks it. Phase 1 pulls anonymously — no registry credentials. |

`githubRelease` remains as an internal-only source for the `OPENDEPLOY`
self-deployment. Public create/update validation rejects it.

### Container runtime

| Variant | Fields | Description |
|---|---|---|
| `runtime` | `user`, `envVars`, `overrideCommand`, `overrideWorkingDir`, `defaultVolume`, `crossDeploymentMounts`, `mounts`, `assetMounts`, `devShmSizeKb`, `fileDescriptorLimit` | Runs the selected source as a container via containerd with OpenDeploy-supervised crash/backoff. Networking is controlled by `spec.networking`. `envVars` contains typed literal, pinned secret/config, asset, or address references. `defaultVolume` controls the per-deployment data volume. `crossDeploymentMounts` references another same-node deployment by ID; `mounts` is the raw host-path escape hatch. Mount permissions are explicit `READ_WRITE`, `READ_ONLY`, or, where supported, `READ_EXECUTE`. Upgrade strategy and readiness are fields on `container1Spec`. Linux only. |

`systemdSpec` remains an internal-only workload for the `OPENDEPLOY`
self-deployment. Public create/update validation rejects it, and public state
responses redact its runtime details.

### Networking

`spec.networking` controls container network mode and published ports.

- Create/update requests must set an explicit networking mode.
- `HOST` joins the host network namespace.
- `VIRTUAL` creates a per-run network namespace with stable inbound IPv6 address `I`, run-scoped preferred outbound IPv6 address `O`, and a machine-local IPv4 egress address. Both IPv6 addresses are preassigned; `I` is non-preferred and routed only to the current run, while `O` remains preferred and routed for that run's full lifetime.
- `portForwarding` publishes host-interface TCP or UDP ports to container ports through nftables DNAT and requires `VIRTUAL`, e.g. `{protocol: TCP, hostPort: 443, containerPort: 443}`.
- `ingress` currently accepts `TLS_PASSTHROUGH` routes in virtual mode. Each route has a hostname and `tlsPassthroughConfig: {hostPort, containerPort}`; `hostPort: 0` defaults to `443`. Routes are rendered into local netproxy state, which selects by TLS SNI and relays the original TCP stream to a READY backend without TLS termination. The primary node reserves `:443` for the Web UI until both share one listener.
- Virtual-mode deployments publish endpoint status for `.internal` DNS discovery through the per-machine netproxy deployment.
- An environment variable of type `Address` selects a same-node virtual deployment and stores its deployment and space IDs. The consuming container receives that target's stable inbound IPv6 address `I` when it starts; run-scoped `O` is never exposed through Address refs. A target cannot be deleted, moved to another space, or changed out of virtual networking while Address references remain. For a space move, remove the references, move the target, then add them back so they capture its new space ID.
- Workers reconcile cross-machine fixed tunnels and workload routes. Equivalent primary-node remote routing, unpublished candidate `O` distribution, policy, and public ingress remain incomplete.

### Config versioning

Each deployment's `DeploymentConfig.Version` is a per-deployment
monotonically increasing integer that bumps on any spec or desired-state
change. Every bump is persisted to `deployment_config_history` so the UI
can reconstruct the sequence of changes.

SQLite persists `DeploymentSpec` in both the current-config and config-history
rows. The selected workload's `version` and `running` fields are the only
authoritative desired state.

## Deployment state

Each deployment's runtime state is structured into sections owned by different components:

### Workload desired state

Set by user actions (deploy or stop). The selected `ContainerSpec` or
`SystemdSpec` contains the target `version` and `running` boolean. Audit fields
(`updated_at`, `updated_by`) and the config revision remain on the parent
`DeploymentConfig`.

Nix desired versions, when set, are full immutable commit hashes. Branch selection and the 25 most recent commits are discovery aids and are not persisted as source authority. Creating a running Nix deployment, starting one, changing its target commit, or changing its Nix source while it remains running performs synchronous remote commit and flake verification before persistence. Stopped Nix deployments still require structurally valid source fields but may omit the desired version and do not require remote accessibility until they transition to running.

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

Each deployment has an integer `id` (primary key) assigned when it is created via `POST /v1/deployment/create`. Human-readable metadata is stored as `DeploymentConfig.Identity`, and application identity is `{nodeId, spaceId, name}`. SQLite enforces that identity with a partial unique index over active deployments. All API requests, storage keys, and log file paths use the integer `id`.

Deleting a deployment releases its human-readable identity tuple but retains its ID, configuration history, status history, logs, volumes, and other ID-owned records. Creating a deployment later with the same space, node, and name creates a completely new and independent deployment with a fresh ID and version history. It does not restore, continue, or otherwise inherit the deleted deployment.

## Deployment status display

The status page shows one table row per deployment, sorted with
OPENDEPLOY last, then by space, name, node, and id. Deployments can also be
grouped by space. Each row displays:

- Deployment name, space, and node
- Status and version columns with one vertically stacked subcell for every non-final scheduled instance, oldest first. During rollover, this shows the old and replacement instances together. Versions use the pinned target until the runner reports its version; runner status badges are clickable to view run output.
- Restart count and last restart time for the newest scheduled instance
- Prepare status with link to prepare output (build/import/pull log)
- Deployment audit metadata plus History, Update, config, fork, and delete actions

## Deploy workflow

1. The user clicks "Update" on a table row. The overlay fetches available versions
   for the persisted source with one `POST /v1/deployment/versions` request —
   25 most recent commits for the selected Nix branch, or available image tags.
   For Nix updates, selecting a commit, changing branch, refreshing discovery,
   or starting a stopped deployment does not exact-validate an unchanged
   persisted repository and flake path in the frontend. Editing the repository
   or flake path still requires exact frontend preflight validation.
2. The user picks a version (and optionally edits the deployment spec) and submits.
3. The frontend calls `POST /v1/deployment/update` with the target version
   and, if the spec was edited, the new typed `spec`.
4. For an effective running Nix transition, the backend verifies the exact remote commit and regular `flake.nix` tree entry, then writes the spec with the selected workload's version and `running=true`, and bumps `DeploymentConfig.Version`. Verification failure writes nothing.
5. The operator's reconciliation loop picks up the change and starts a
   preparer.
6. The preparer clones/fetches, pulls, or imports the image, then writes
   `PreparerStatus.Status = READY`.
7. The operator creates a runner, which writes `RunnerStatus.Status =
   STARTING` then `RUNNING` with the PID.

For container deployments with `upgradeStrategy = ROLLOVER`, step 7 starts a candidate container before stopping the old one. The candidate receives `OPENDEPLOY_READINESS_SOCK_PATH=/run/opendeploy/readiness.sock`; once warmup is complete it writes `ready\n` to that Unix socket. With virtual networking, the candidate can bind the same container port immediately in its own network namespace. It already has preferred, routed `O` and non-preferred `I`; OpenDeploy promotes it by flipping the `I` host route and any `portForwarding` rules, then stops the old container. Promotion does not change its addresses or source preference, so it keeps using `O` for outbound traffic until that run ends. With host networking, rollover is cooperative: the candidate shares the host network namespace with the old container, so it must not bind conflicting host ports before signaling readiness. After readiness, it should wait until OpenDeploy stops the old container and the port becomes free, then bind and serve.

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
