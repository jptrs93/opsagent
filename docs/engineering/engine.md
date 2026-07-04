# Deployment engine

## Overview

The engine package (`backend/engine/`) orchestrates deployments through an operator-per-deployment model. Each deployment gets a `DeploymentOperator` reconciliation loop that reacts to config/status changes, starts a preparer, and then starts or replaces a runner when the prepared image is ready.

Key files:

- `backend/engine/operator.go` — `DeploymentOperator` reconciliation loop.
- `backend/engine/preparer/preparer.go` — `Preparer` interface plus `StartPrepare`/`ReAttach` dispatch.
- `backend/engine/preparer/nixdockerbuild.go` — builds a Nix flake that streams an OCI/Docker image and imports it into containerd.
- `backend/engine/preparer/containerimage.go` — pulls a container image into containerd.
- `backend/engine/preparer/ghrelease.go` — internal-only GitHub release downloader for the `OPENDEPLOY` self-deployment.
- `backend/engine/preparer/nixbuild.go` — shared Git/Nix command helper used by `NixDockerBuilder`; not a public Nix-store executable preparer.
- `backend/engine/preparer/gitmanager.go` — Git repo/branch/commit helper used by validation, version discovery, and Nix Docker builds.
- `backend/engine/ctrd/` — containerd client wrapper behind Linux build tags.
- `backend/engine/runner/runner.go` — `Runner` interface and factories.
- `backend/engine/runner/container.go` — public containerd runner.
- `backend/engine/runner/systemd.go` — internal-only runner for the `OPENDEPLOY` self-deployment.

## Data Model

Public deployment configs have two steps:

- `prepare` produces a containerd image ref. Public variants are `nixDockerBuild` and `containerImage`.
- `runner` runs the image. Public deployments use only `runner.container`; omitted runner config means all-default container settings.

Internal exceptions:

- `prepare.githubRelease` is retained for the OpenDeploy self-deployment.
- `runner.systemd` is retained for the OpenDeploy self-deployment.
- Public create/update validation rejects both internal branches, and public state/history responses redact `runner.systemd` to an empty `runner` object.

`PreparerStatus.Artifact` is the resolved runtime artifact. For public deployments this is always a local containerd image ref. For the internal system deployment it is the downloaded OpenDeploy binary path consumed by the internal systemd runner.

## Operator

`DeploymentOperator` is deliberately small: it decides which prepared artifact should be running and delegates lifecycle to the current preparer/runner.

Decision flow:

- `config.Deleted` — cancel preparer, stop runner, unsubscribe.
- `!config.DesiredState.Running` — stop runner.
- `config.Version > currentPreparer.Version()` — cancel old prepare and start a new one.
- `preparerReady(status, config.Version) && config.Version > currentRunner.Version()` — stop old runner and create a new one.

`Stop()` is synchronous. The operator waits until the runner has stopped and written terminal status before moving on.

Container deployments can opt into `runner.container.upgradeStrategy = ROLLOVER`. In that mode the operator starts an unpublished candidate runner for the prepared config version, waits for its readiness signal, then promotes it and stops the old runner. If the candidate exits, times out, or fails before readiness, the operator stops the candidate and keeps the old runner active. The default/unspecified strategy is `RECREATE`, which preserves the stop-then-start behavior above.

## Preparers

`NixDockerBuilder` asks `GitManager` to prepare a local checkout of the configured Git repo at `DesiredState.Version`, verifies the configured `flake.nix` path exists in that checked-out tree, runs `nix build --no-update-lock-file --no-link --print-out-paths -L` in the configured flake directory, executes the resulting image stream, and pipes it into `ctrd.Client.Import`. The imported image is tagged as `opendeploy.local/nix-docker-build/{deploymentID}:{version}`. Validation and version discovery use Git-native remote/ref operations plus a bare partial metadata cache under the data directory, avoiding GitHub API dependency for Nix/Git sources.

`ContainerImagePuller` pulls `prepare.containerImage.image` plus the desired tag/digest into containerd and unpacks it. Pulls are anonymous in the current phase.

`GithubReleaseDownloader` is internal-only. It downloads OpenDeploy release assets for `OPENDEPLOY`; it is not exposed as a public deployment source.

Prepare output is written to `{PrepareOutputDir}/{deploymentID}/{version}.log`.

## Runners

`Runner` remains a small interface so internal and future runner variants can coexist:

```go
type Runner interface {
    Stop()
    Version() int32
}
```

Public deployments run through `containerRunner`. It creates one deterministic containerd container per deployment config version (`opendeploy-{deploymentID}-v{version}`), starts a task with host networking, wires stdout/stderr through the internal split binary log consumer, writes indexed source files under `{RunOutputDir}/{deploymentID}/{version}/{run}/{stdoutN,stderrN}.logbin`, waits for task exit, and respawns with exponential backoff on crashes. Split source files end with a rotate marker when the consumer moves to the next indexed file, or an end marker on graceful shutdown. The log collector follows rotate markers and compacts source files into processed hourly files under `{RunOutputDir}/{deploymentID}/processed/` for API reads.

Run-log reads go through `engine/logreader`. It identifies all candidate run directories for a deployment, turns each run into a chronological `LogLine` stream, and merges those streams by timestamp for historical searches. It only scans existing files and does not tail active writes.

Container runner behavior:

- Environment variables are stored as typed `EnvVarValue` entries. Exactly one of literal `value`, `secretId`, `configId`, or asset ref is set; secret/config IDs are prepared by the preparer and resolved at start time.
- Default data volume is created under `{dataDir}-volumes/{deploymentID}/default` and mounted at `/data`, unless disabled or overridden with `dataMountPath`.
- Additional host mounts and OpenDeploy-managed asset mounts are translated to containerd bind mounts.
- `devShmSizeKb` optionally resizes the container's default `/dev/shm` tmpfs from containerd's default 64 MiB using a KiB value.
- `fileDescriptorLimit` optionally overrides the OCI `RLIMIT_NOFILE`; when unset, OpenDeploy sets both soft and hard limits to `2048`.
- Rollover candidates get a per-run Unix socket directory mounted at `/run/opendeploy` and `OPENDEPLOY_READINESS_SOCK_PATH=/run/opendeploy/readiness.sock`. The app signals readiness by writing `ready\n` to that socket after warmup. OpenDeploy then stops the old runner; with host networking, the app should wait for its required port to become bindable before starting its server.
- Reattach uses `ctrd.LoadTask` by deterministic id; if no running task exists, the runner starts fresh.
- Stop sends SIGTERM, waits up to 3 seconds, sends SIGKILL if needed, then deletes the task/container/snapshot.

`systemdRunner` is internal-only for the OpenDeploy self-deployment. It symlinks the downloaded binary to the configured bin path, writes `STARTING`, and asks systemd to restart the unit. The old process does not poll systemd after requesting restart; the restarted process marks itself `RUNNING` when the operator reattaches. Public API validation rejects systemd runner config.

## Containerd Runtime

OpenDeploy runs its own bundled containerd provisioned by the installer as `opendeploy-containerd.service`. Runtime binaries/config live under `/var/lib/opendeploy/runtime/`; containerd root state lives under `/var/lib/opendeploy-containerd/`; transient state lives under `/run/opendeploy-containerd/`; the socket is `/run/opendeploy/containerd.sock`.

The `ctrd` package stubs out on non-Linux so the backend builds on macOS/dev. Container prepares/runs fail at runtime on unsupported platforms with a clear containerd/Linux error.

## Backoff

Container crash backoff is 1 second to 60 seconds, doubling per consecutive crash. If a container runs for at least 15 seconds before crashing, the local crash count resets.

## Storage Failure Policy

All DB calls go through `Must*` variants when failure is an internal invariant violation. The process is expected to run under a supervisor that restarts it; startup rebuilds in-memory state from the database.

Rules for new code:

- Writes use `Must*`.
- Reads where the key is an internal invariant use `Must*`.
- Reads driven by user input where "not found" is expected use non-`Must*` and translate to `ApiErr`.
