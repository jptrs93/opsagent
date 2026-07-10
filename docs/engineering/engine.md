# Deployment engine

## Overview

The engine package (`backend/lib/engine/`) orchestrates deployments through an operator-per-deployment model. Each deployment gets a `DeploymentOperator` reconciliation loop that reacts to config/status changes, starts a preparer, and then starts or replaces a runner when the prepared image is ready.

Key files:

- `backend/lib/engine/operator.go` — `DeploymentOperator` reconciliation loop and preparer dispatch.
- `backend/app/primary/runtime.go` — primary dependency graph and operator startup.
- `backend/lib/engine/preparer/preparer.go` — in-flight handle lifecycle and shared preparation helpers.
- `backend/lib/engine/preparer/nixdocker/` — builds a Nix flake that streams an OCI/Docker image and imports it into containerd.
- `backend/lib/engine/preparer/containerimage/` — pulls an ordinary registry image into containerd.
- `backend/lib/engine/preparer/githubrelease/` — internal-only OpenDeploy executable release preparation.
- `backend/lib/engine/preparer/githubreleaseimage/` — builds the internal `opendeploy-net` image from an OpenDeploy release binary.
- `backend/lib/repo/git/` — Git repo/branch/commit and checkout service used by validation, version discovery, and Nix Docker builds.
- `backend/lib/repo/github/` — authenticated GitHub release and asset download client.
- `backend/lib/engine/ctrd/` — containerd client wrapper behind Linux build tags.
- `backend/lib/engine/runner/runner.go` — `Runner` interface and factories.
- `backend/lib/engine/runner/container.go` — public containerd runner.
- `backend/lib/engine/runner/systemd.go` — internal-only runner for the OpenDeploy self-deployment.
- `backend/lib/network/` — machine-local virtual networking, host routes, nftables egress NAT, and host-port DNAT.

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

`DeploymentOperator` owns concrete references to the four artifact preparers, the shared containerd client and runtime-input service, and the runner reconciliation loop. It explicitly passes the same containerd and runtime-input instances to every container runner it creates or reattaches. A single private switch selects the GitHub release, Nix Docker, ordinary container image, or internal GitHub release image preparer. Primary constructs and starts its operator in `app/primary/runtime.go`; secondary does so in `app/secondary/secondary.go`. The dependency graphs are independent, and preparation and runner construction do not use package-level implementation globals.

`preparer.Handle` is the concrete cancellation/completion handle for the complete preparation operation. The operator owns its goroutine and all status transitions: it first ensures all runtime inputs are available, then calls the selected artifact preparer synchronously, and publishes the terminal status only after both stages complete. Concrete artifact preparers only perform strategy-specific artifact work. Nix builds are serialized across deployments to avoid Nix-store contention; image pulls and the two internal release preparations are allowed to run independently.

Decision flow:

- `config.Deleted` — cancel preparer, stop runner, unsubscribe.
- `!config.DesiredState.Running` — stop runner.
- `config.Version > currentPreparer.Version()` — cancel old prepare and start a new one.
- `preparerReady(status, config.Version) && config.Version > currentRunner.Version()` — stop old runner and create a new one.

`Stop()` is synchronous. The operator waits until the runner has stopped and written terminal status before moving on.

Container deployments can opt into `runner.container.upgradeStrategy = ROLLOVER`. The operator starts an unpublished candidate runner for the prepared config version, waits for its readiness signal, promotes it, and stops the old runner. If the candidate exits, times out, or fails before readiness, the operator stops the candidate and keeps the old runner active. The default/unspecified strategy is `RECREATE`, which preserves the stop-then-start behavior above. Virtual networking can promote without host-port bind contention; host networking requires the workload to defer binding conflicting host ports until after readiness.

## Preparers

`nixdocker.Preparer` asks `repo/git.Manager` to prepare a local checkout of the configured Git repo at `DesiredState.Version`, verifies the configured `flake.nix` path exists in that checked-out tree, runs `nix build --no-update-lock-file --no-link --print-out-paths -L` in the configured flake directory, executes the resulting image stream, and pipes it into `ctrd.Client.Import`. The imported image is tagged as `opendeploy.local/nix-docker-build/{deploymentID}:{version}`. Validation and version discovery share the same Git manager and its bare partial metadata cache under the data directory.

`containerimage.Preparer` pulls `prepare.containerImage.image` plus the desired tag/digest into containerd and unpacks it. Pulls are anonymous in the current phase.

`githubrelease.Preparer` is internal-only. It downloads the executable used by the OpenDeploy self-deployment. `githubreleaseimage.Preparer` independently downloads the architecture-specific OpenDeploy binary and packages it into the internal `opendeploy-net` OCI image. Both consume the shared `repo/github.Client`; neither concrete preparer depends on the other.

Asset, secret, and config preparation dependencies are grouped in one instance-owned `runtimeinputs.RuntimeInputs`. It owns the validated in-memory secret and config caches as well as the providers that populate them. The operator uses this service before artifact preparation and explicitly passes the same instance to container runners for typed env-ref expansion at spawn and respawn. Every deployment strategy passes through this common stage before artifact preparation. On successful process-start reattachment, the operator also rechecks runtime inputs before reusing an existing artifact.

Prepare output is written to `{PrepareOutputDir}/{deploymentID}/{version}.log`.
OpenDeploy-generated entries use `==> message` and failures use `==> ERROR: message`; subprocess output is streamed unchanged between those entries.

## Runners

`Runner` remains a small interface so internal and future runner variants can coexist:

```go
type Runner interface {
    Stop()
    Version() int32
}
```

Public deployments run through `containerRunner`. It creates one deterministic containerd container per deployment config version (`opendeploy-{deploymentID}-v{version}`), wires stdout/stderr through the internal split binary log consumer, writes indexed source files under `{RunOutputDir}/{deploymentID}/{version}/{run}/{stdoutN,stderrN}.logbin`, waits for task exit, and respawns with exponential backoff on crashes. Host-network deployments join the host network namespace. Virtual-network deployments get a pre-created Linux netns from `backend/lib/network`, and the containerd OCI spec joins that namespace. Split source files end with a rotate marker when the consumer moves to the next indexed file, or an end marker on graceful shutdown. The log collector follows rotate markers and compacts source files into processed hourly files under `{RunOutputDir}/{deploymentID}/processed/` for API reads.

Run-log reads go through `engine/logreader`. It identifies all candidate run directories for a deployment, turns each run into a chronological `LogLine` stream, and merges those streams by timestamp for historical searches. It only scans existing files and does not tail active writes.

Container runner behavior:

- Environment variables are stored as typed `EnvVarValue` entries. Exactly one of literal `value`, `secretId`, `configId`, or asset ref is set; secret/config IDs are prepared by the preparer and resolved at start time.
- Default data volume is created under `{dataDir}-volumes/{deploymentID}/default` and mounted at `/data`, unless disabled or overridden with `dataMountPath`.
- Additional host mounts and OpenDeploy-managed asset mounts are translated to containerd bind mounts.
- `devShmSizeKb` optionally resizes the container's default `/dev/shm` tmpfs from containerd's default 64 MiB using a KiB value.
- `fileDescriptorLimit` optionally overrides the OCI `RLIMIT_NOFILE`; when unset, OpenDeploy sets both soft and hard limits to `2048`.
- Virtual networking creates a netns/veth pair, a stable IPv6 instance address, a machine-local IPv4 egress address, a host `/128` route, and optional nftables `portForwarding` DNAT rules.
- Rollover candidates get a per-run Unix socket directory mounted at `/run/opendeploy` and `OPENDEPLOY_READINESS_SOCK_PATH=/run/opendeploy/readiness.sock`. The app signals readiness by writing `ready\n` to that socket after warmup. In virtual mode, OpenDeploy promotes the candidate by replacing the stable-address host route and `portForwarding` rules, then stops the old runner. In host mode, rollover is cooperative: the candidate shares the host network namespace, so it must defer binding conflicting host ports until after readiness and then wait for the old runner to stop.
- Reattach uses `ctrd.LoadTask` by deterministic id; if no running task exists, the runner starts fresh.
- Stop sends SIGTERM, waits up to 3 seconds, sends SIGKILL if needed, then deletes the task/container/snapshot.

## Networking

The current networking implementation is machine-local. See `docs/engineering/networking.md` for details.

The runner only consumes networking primitives. It derives the stable instance address from the cluster ULA prefix and deployment id, asks the network manager to prepare state, passes the netns path to containerd, writes a generated `resolv.conf` when netproxy DNS is known, and writes endpoint status back to storage.

Cross-machine routing, WireGuard, ingress, and policy enforcement are outside the current runner path.

`systemdRunner` is internal-only for the OpenDeploy self-deployment. It symlinks the downloaded binary to the configured bin path, writes `STARTING`, and asks systemd to restart the unit. The old process does not poll systemd after requesting restart; the restarted process marks itself `RUNNING` when the operator reattaches. Public API validation rejects systemd runner config.

## Containerd Runtime

OpenDeploy runs its own bundled containerd provisioned by the installer as `opendeploy-containerd.service`. Runtime binaries/config live under `/var/lib/opendeploy/runtime/`; containerd root state lives under `/var/lib/opendeploy-containerd/`; transient state lives under `/run/opendeploy-containerd/`; the socket is `/run/opendeploy/containerd.sock`.

The deployment runtime supports Linux only. The `ctrd` client is compiled on all platforms so platform-independent unit tests run on macOS; container operations still require an accessible containerd daemon and Linux runtime. The networking package retains non-Linux stubs.

## Backoff

Container crash backoff is 1 second to 60 seconds, doubling per consecutive crash. If a container runs for at least 15 seconds before crashing, the local crash count resets.

## Storage Failure Policy

All DB calls go through `Must*` variants when failure is an internal invariant violation. The process is expected to run under a supervisor that restarts it; startup rebuilds in-memory state from the database.

Rules for new code:

- Writes use `Must*`.
- Reads where the key is an internal invariant use `Must*`.
- Reads driven by user input where "not found" is expected use non-`Must*` and translate to `ApiErr`.
