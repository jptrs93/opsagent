# Deployment engine

## Overview

The engine package (`backend/lib/engine/`) orchestrates deployments through an operator-per-deployment model. Each deployment gets a `DeploymentOperator` reconciliation loop that reacts to config/status changes, starts a preparer, and then starts or replaces a runner when the prepared image is ready.

Key files:

- `backend/lib/engine/operator.go` — `DeploymentOperator` reconciliation loop and preparer dispatch.
- `backend/app/primary/runtime.go` — primary dependency graph and operator startup.
- `backend/lib/engine/prepare/preparer.go` — in-flight handle lifecycle and shared preparation helpers.
- `backend/lib/engine/prepare/nixdocker/` — builds a Nix flake that streams an OCI/Docker image and imports it into containerd.
- `backend/lib/engine/prepare/containerimage/` — pulls an ordinary registry image into containerd.
- `backend/lib/engine/prepare/opendeployrelease/` — internal-only OpenDeploy release preparation: the self-deployment executable and the `opendeploy-net` image built from it.
- `backend/lib/repo/git/` — Git repo/branch/commit and checkout service used by validation, version discovery, and Nix Docker builds.
- `backend/lib/repo/github/` — authenticated GitHub release and asset download client.
- `backend/lib/engine/ctrd/` — containerd client wrapper behind Linux build tags.
- `backend/lib/engine/runner/runner.go` — `Runner` interface and factories.
- `backend/lib/engine/runner/container.go` — public containerd runner.
- `backend/lib/engine/runner/opendeploy.go` — internal-only runner for the OpenDeploy self-deployment.
- `backend/lib/network/` — machine-local virtual networking, host routes, nftables egress NAT, and host-port DNAT.

## Data Model

Public deployment configs currently select exactly one workload. A container
workload is stored in `spec.container1Spec`, with its artifact source in
`source`, process and mount settings in `runtime`, and desired `version` and
`running` state on the workload itself. Public source variants are
`nixDockerBuild` and `remoteImage`.

Internal exceptions:

- `spec.opendeploySpec` is retained for the OpenDeploy self-deployment. It carries only the desired release version; the release source, systemd unit, and install path are compile-time constants.
- Public create/update validation rejects the internal branch, and public state/history responses redact its `runtime` while retaining source and workload state.

`PreparerStatus.Artifact` is the resolved runtime artifact. For public deployments this is always a local containerd image ref. For the internal system deployment it is the downloaded OpenDeploy binary path consumed by the internal opendeploy runner.

Preparation is reported as two stages. `PreparerStatus.Inputs` tracks resolving assets, secrets, and configs; `PreparerStatus.Image` tracks producing the artifact (nix build, remote image pull, or GitHub release download). Those two are the only stored and transmitted preparer state.

The single status that gates runner start, that retention reads, and that holds a prepare-log stream open is `PreparerStatus.Rollup()`, derived from the pair. It is neither persisted nor sent, so the stages can never disagree with it. `PreparationStatus` field 3 held it before it became derived and is reserved.

The rollup ranks a ready image above a resolving inputs stage. That is what lets an input retry on an already-prepared instance publish its stage without demoting the rollup that keeps the runner running.

## Operator

`DeploymentOperator` owns concrete references to the stateful artifact preparers and the runtime-input service, along with the runner reconciliation loop. It passes the runtime-input instance to every container runner it creates or reattaches. Ordinary container-image preparation is a stateless package operation; image preparation and runners use the process-wide lazy `ctrd.Default` client directly. A single private switch selects the GitHub release, Nix Docker, ordinary container image, or internal GitHub release image path. Primary constructs and starts its operator in `app/primary/runtime.go`; secondary does so in `app/secondary/secondary.go`.

On a secondary, the operator starts behind a boot sync gate: `RunAll` is held until the primary's first assignment snapshot has been applied to the local store, or for `bootSyncTimeout` when the primary is unreachable, after which it runs from the cached assignments exactly as before. Cached assignments can be arbitrarily stale, and acting on them seconds before a fresh snapshot arrives has caused real damage — a self-deployment runner reverting a just-installed binary to the cached version, and container network namespaces derived from cached configs that had lost their space identity. The cluster session signals the gate after `applySnapshot`; `RunAll` snapshots and subscribes atomically, so everything applied before it starts is in its initial view.

`prepare.Handle` is the concrete cancellation/completion handle for the complete preparation operation. The operator owns its goroutine and all status transitions: it first ensures all runtime inputs are available, then calls the selected artifact preparer synchronously, and publishes the terminal status only after both stages complete. Concrete artifact preparers only perform strategy-specific artifact work and report an `ImageStatus`.

Inputs run before the image stage so a cheap and commonly transient failure — a secret the primary has not distributed yet — fails before committing to a build that can take minutes. The two stages are otherwise independent. `prepare.StatusUpdate` is the only way to publish a transition. Nix builds are serialized across deployments to avoid Nix-store contention; image pulls and the two internal release preparations are allowed to run independently.

Decision flow:

- `config.Deleted` — cancel preparer, stop runner, unsubscribe.
- `!config.WorkloadRunning()` — stop runner.
- `config.Version > currentPreparer.Version()` — cancel old prepare and start a new one.
- `preparerReady(status, config.Version) && config.Version > currentRunner.Version()` — stop old runner and create a new one.
- A persisted `READY` container image that is absent or not unpacked locally — prepare the same config version again without changing desired-state history.
- A container spawn that reports a locally unavailable image — stop retrying that image, signal the operator, and prepare the same config version again.

`Stop()` is synchronous. The operator waits until the runner has stopped and written terminal status before moving on. Same-version artifact repair waits until the replacement preparation has published a non-`READY` transition before accepting its terminal `READY`, so a queued runner update carrying the stale preparer status cannot restart the missing image.

Container deployments can opt into `container1Spec.upgradeStrategy = ROLLOVER`. The operator starts a candidate runner for the prepared config version, waits for its readiness signal, promotes it, and stops the old runner. The default/unspecified strategy is `RECREATE`, which preserves the stop-then-start behavior above. Virtual networking can promote without host-port bind contention; host networking requires the workload to defer binding conflicting host ports until after readiness.

A candidate publishes status to its scheduled instance throughout, the same as any other runner. It has no incumbent to clobber: an instance's config is pinned to its version, so a candidate is only ever created when nothing is running for that instance. It publishes `STARTING` until the readiness signal arrives and `RUNNING` only after, because `RUNNING` is what tells the scheduler it may hand the placement the instance address. A candidate that crashes before signalling is recorded as `CRASHED` and respawned with the usual backoff; the readiness timeout is a deadline across every attempt, and expiring it stops the candidate and releases the operator, which keeps the old runner active. Suppressing these writes previously made a candidate that crashed during startup invisible and, because no status change was published, left the operator with nothing to react to and the rollout stalled.

## Preparers

`nixdocker.Preparer` derives a node-local image ref from a cache schema version, the Nix repository/flake/target, the node platform, and `ContainerSpec.Version`. The ref is `opendeploy.local/nix-docker-build/v1/{sourceHash}:{commit}` and is intentionally independent of deployment ID and runtime-only config. After acquiring the node-wide Nix build semaphore, the preparer reuses that image when it is already present and unpacked in containerd. A cache miss asks `repo/git.Manager` to prepare a local checkout of the configured Git repo at the desired commit, verifies the configured repository-relative `flake.nix` path is still a regular file in that checkout, and runs `nix build --no-update-lock-file --no-link --print-out-paths -L` in the configured flake directory. An optional local `target` such as `.#radkitRpaClientImage` is appended to that command; an empty target builds the default output. It executes the resulting image stream and pipes it into `ctrd.Client.Import`. Bumping the cache schema version creates a new image namespace and lazily invalidates images produced under older preparer semantics.

The primary validates every effective transition to a running Nix source before persisting it. The desired version must be a full commit hash; validation contacts the remote, fetches that exact repository-wide commit, verifies its identity, and checks the Git tree entry for the flake path is a regular file rather than a directory, symlink, submodule, or missing path. Branches and 25-entry commit lists are discovery data only. Stopped deployments retain structural validation but may save sources that are currently inaccessible. Checkout validation remains defense in depth on the machine that prepares the deployment. Validation, version discovery, and preparation share the Git manager and its bare partial metadata cache under the data directory.

Clone URLs are credential-free. The GitHub token is supplied per invocation through `GIT_CONFIG_COUNT`/`GIT_CONFIG_KEY_0`/`GIT_CONFIG_VALUE_0` as an `http.extraHeader` scoped to `https://github.com/`, so it is never written to `.git/config` by clone or `remote set-url` and never appears in `/proc/<pid>/cmdline`. `GIT_TERMINAL_PROMPT=0` makes a missing or rejected token fail rather than block. Checkouts holding a token in `.git/config` from an earlier version are rewritten on their next use, because `EnsureCheckout` and `ensureMetadataRepo` set the remote URL on every call. This requires git 2.31 or newer.

`containerimage.Preparer` pulls `container1Spec.source.remoteImage.image` plus the workload version tag/digest into containerd and unpacks it. Pulls are anonymous in the current phase.

`opendeployrelease.Preparer` is internal-only. `PrepareBinary` downloads the architecture-specific executable used by the OpenDeploy self-deployment; `PrepareImage` downloads the same release binary and packages it into the internal `opendeploy-net` OCI image. Both share one release download path on top of `repo/github.Client`, which is unauthenticated: the OpenDeploy release repository is public, so the GitHub token is only distributed to workers with Nix-built deployments.

Asset, secret, and config preparation dependencies are grouped in one instance-owned `runtimeinputs.RuntimeInputs`. It owns the validated in-memory secret and config caches as well as the providers that populate them. The operator uses this service before artifact preparation and explicitly passes the same instance to container runners for typed env-ref expansion at spawn and respawn. Every deployment strategy passes through this common stage before artifact preparation. On process-start reattachment, the operator rechecks runtime inputs and verifies that a persisted container image is locally present and unpacked before reusing it.

When reattachment finds a prepared instance whose inputs are unavailable, the operator retries the inputs in the background rather than re-preparing: re-preparing fetches the same inputs from the same place and would publish a terminal failure nothing retries out of. The retry publishes the inputs stage so the stuck instance is visible, while the recorded artifact and the `READY` rollup both survive it.

Primary backup restore preserves desired config and append-only status history, but runtime observations belong to the machine that produced the backup. The installer clears the mutable current preparer and runner fields for non-system deployments assigned to the replacement primary. Their unchanged config versions are then prepared normally on first startup. The OpenDeploy self-deployment is excluded because the installer has already installed and started that binary; worker statuses are retained because surviving workers reconcile their own local runtime state.

Prepare output is written to `{PrepareOutputDir}/{deploymentID}/{version}.log`.
OpenDeploy-generated entries use `==> message` and failures use `==> ERROR: message`; subprocess output is streamed unchanged between those entries.

## Filesystem layout

`ainit` derives and creates the fixed primary/secondary runtime roots before opening databases or starting engine components. These include the asset cache, local large-asset store, Git cache parents, netproxy state directory, readiness and resolver roots, worker TLS directory, ACME cache, releases, volumes, and build/run log roots. Dataplane only derives these paths because its netproxy directory is mounted read-only.

Operations continue to create dynamic descendants whose names or ownership are runtime-specific: deployment log directories, release versions, Git repositories and worktrees, deployment volumes, readiness run directories, and generated resolver directories. Code using a fixed root assumes `ainit` has already created it.

## Runners

`Runner` remains a small interface so internal and future runner variants can coexist:

```go
type Runner interface {
    Stop()
    Version() int32
    ArtifactMissing() <-chan struct{}
    // Serve claims the instance's stable inbound address for this placement
    // (host route plus published host ports); idempotent, called by the
    // operator whenever the placement's target state is RUN_SERVING.
    Serve() error
}
```

Public deployments run through `containerRunner`. It creates one deterministic containerd container per deployment config version (`opendeploy-{deploymentID}-v{version}`), wires stdout/stderr through a dedicated binary log consumer, waits for task exit, and respawns with exponential backoff on crashes. The consumer writes merged half-hourly UTC files under `{LogWALDir}/{deploymentID}/{YYYYMMDD_HHMM}_{version}_{run}.logbin`. Each binary record carries its timestamp, config version, run number, and stdout/stderr stream. `version` is the deployment configuration version; `run` starts at `1` and increments for each respawn of that version. A rollover candidate has a newer configuration version, so it writes distinct files while it overlaps the old runner. Host-network deployments join the host network namespace. Virtual-network deployments get a pre-created Linux netns from `backend/lib/network`, and the containerd OCI spec joins that namespace.

Run-log reads go through `lib/log/logreader`. It identifies all candidate `.logbin` files for a deployment, turns each into a chronological `LogLine` stream, and merges those streams by timestamp for historical searches. It only scans existing files and does not tail active writes.

Container runner behavior:

- Environment variables are stored as typed `EnvVarValue` entries. Exactly one of literal `value`, `secretVersionId`, `configVersionId`, asset ref, or Address ref is set. An Address ref persists `{addressDeploymentId, addressSpaceId}` and, at spawn, derives the target's stable inbound virtual IPv6 address `I` from the local cluster ULA prefix without fetching target deployment state. Address refs never expose `O`. Targets must be virtual-networked on the same node; target deletion, removal of virtual networking, and space moves are rejected while references exist.
- Default data volume is created under `{dataDir}-volumes/{deploymentID}/default` and mounted at `/data`, unless `runtime.defaultVolume.disabled` is set or `runtime.defaultVolume.containerPath` overrides the destination.
- Additional host mounts and OpenDeploy-managed asset mounts are translated to containerd bind mounts.
- `devShmSizeKb` optionally resizes the container's default `/dev/shm` tmpfs from containerd's default 64 MiB using a KiB value.
- `fileDescriptorLimit` optionally overrides the OCI `RLIMIT_NOFILE`; when unset, OpenDeploy sets both soft and hard limits to `2048`.
- Every virtual container run gets a netns/veth pair, stable inbound IPv6 address `I`, run-scoped preferred outbound IPv6 address `O`, machine-local IPv4 egress address, and a host `/128` route for `O`. Both IPv6 addresses are assigned before process start; `I` has `preferred_lft=0`. Activation routes `I` to the current run, and optional nftables `portForwarding` rules target that run.
- Rollover candidates get a per-run Unix socket directory mounted at `/run/opendeploy` and `OPENDEPLOY_READINESS_SOCK_PATH=/run/opendeploy/readiness.sock`. The app signals readiness by writing `ready\n` to that socket after warmup. In virtual mode, OpenDeploy promotes the candidate by replacing the `I` route and `portForwarding` rules, then stops the old runner. The promoted run continues to use `O` as its preferred outbound source for its full lifetime. In host mode, rollover is cooperative: the candidate shares the host network namespace, so it must defer binding conflicting host ports until after readiness and then wait for the old runner to stop.
- Reattach uses `ctrd.LoadTask` by deterministic id; if no running task exists, the runner starts fresh.
- Stop sends SIGTERM, waits up to 3 seconds, sends SIGKILL if needed, then deletes the task/container/snapshot.

## Networking

The current networking implementation is machine-local. See `docs/engineering/networking.md` for details.

The runner only consumes networking primitives. It derives `I` from cluster prefix, space, deployment, and ordinal, and derives `O` from those fields plus normalized nonzero config-version and run slots. Raw versions and run numbers remain full-width outside address derivation. The runner asks the network manager to prepare both addresses and the `O` route, activates `I` for the published run, passes the netns path to containerd, writes a generated `resolv.conf` when netproxy DNS is known, and writes endpoint status back to storage. DNS, endpoint state, ingress, and typed Address refs expose `I`, never `O`.

Cross-machine IP-in-IP routing, ingress, and policy enforcement are outside the current runner path.

`opendeployRunner` is internal-only for the OpenDeploy self-deployment. It symlinks the downloaded binary to the fixed bin path, writes `STARTING`, and asks systemd to restart the unit. The old process does not poll systemd after requesting restart; the restarted process marks itself `RUNNING` when the operator reattaches. Public API validation rejects opendeploy spec config.

## Containerd Runtime

OpenDeploy runs its own bundled containerd provisioned by the installer as `opendeploy-containerd.service`. Runtime binaries/config live under `/var/lib/opendeploy/runtime/`; containerd root state lives under `/var/lib/opendeploy-containerd/`; transient state lives under `/run/opendeploy-containerd/`; the socket is `/run/opendeploy/containerd.sock`.

The deployment runtime supports Linux only. The `ctrd` client is compiled on all platforms so platform-independent unit tests run on macOS; container operations still require an accessible containerd daemon and Linux runtime. The networking package retains non-Linux stubs.

## Backoff

Container crash backoff is 1 second to 1 hour, doubling per consecutive crash. If a container runs for at least 15 seconds before crashing, the local crash count resets.

## Storage Failure Policy

All DB calls go through `Must*` variants when failure is an internal invariant violation. The process is expected to run under a supervisor that restarts it; startup rebuilds in-memory state from the database.

Rules for new code:

- Writes use `Must*`.
- Reads where the key is an internal invariant use `Must*`.
- Reads driven by user input where "not found" is expected use non-`Must*` and translate to `ApiErr`.
