# Deployments

## Overview

OpenDeploy manages deployment configurations and deployment lifecycles. Users
create each deployment individually through typed protobuf API messages. The
system fetches available versions (git commits for Nix Docker builds or image
tags for container images) when the user asks, prepares images in containerd,
and supervises running containers with automatic crash recovery.

## Deployment config

Each deployment currently has exactly one workload. Public deployments use
`spec.container1Spec`, which contains one artifact `source`, its `runtime`
configuration, and workload-local `version` and `running` desired state.

A deployment is created by posting a `DeploymentCreateRequest` to
`POST /v1/deployments/create`:

```json
{
  "name": "coflip_server",
  "spaceId": 1,
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
          "DATABASE_URL": {"configVersionId": 42},
          "DB_PASSWORD": {"secretVersionId": 99}
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

The spec of an existing deployment is updated by posting a `spec_update` in
`POST /v2/deployments/update`. Its name and `nodeId` placement are fixed at
creation; its space can be changed through the same endpoint's
`assigned_space_update` kind (see Config versioning).

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
| `runtime` | `user`, `envVars`, `overrideCommand`, `overrideWorkingDir`, `defaultVolume`, `crossDeploymentMounts`, `mounts`, `assetMounts`, `devShmSizeKb`, `fileDescriptorLimit` | Runs the selected source as a container via containerd with OpenDeploy-supervised crash/backoff. Networking is controlled by `spec.networking`. `envVars` contains typed literal, pinned secret/config, asset, or address references. A deployment may reference secrets only from its own space or the global space; creates, updates, and space moves reject other pins with `secret_reference_outside_space`, and the editor applies the same own-or-global scoping to `secret("name")`, `config("name")`, and `asset("name")` with the deployment's own space shadowing a same-named global item (an explicit `{ space = "name" }` option targets one of the two spaces exactly; the server enforces own-or-global locality for secret, config, and asset pins). `defaultVolume` controls the per-deployment data volume. `crossDeploymentMounts` references another same-node deployment by ID; `mounts` is the raw host-path escape hatch. Mount permissions are explicit `READ_WRITE`, `READ_ONLY`, or, where supported, `READ_EXECUTE`. Upgrade strategy and readiness are fields on `container1Spec`. Linux only. |

`opendeploySpec` remains an internal-only workload for the `OPENDEPLOY`
self-deployment. It carries only the desired release version; public
create/update validation rejects it, and the self-deployment cannot be
stopped.

### Networking

`spec.networking` controls container network mode and published ports.

- Create/update requests must set an explicit networking mode.
- `HOST` joins the host network namespace.
- `VIRTUAL` creates a per-run network namespace with stable inbound IPv6 address `I`, run-scoped preferred outbound IPv6 address `O`, and a machine-local IPv4 egress address. Both IPv6 addresses are preassigned; `I` is non-preferred and routed only to the current run, while `O` remains preferred and routed for that run's full lifetime.
- `portForwarding` publishes host-interface TCP or UDP ports to container ports through nftables DNAT and requires `VIRTUAL`, e.g. `{protocol: TCP, hostPort: 443, containerPort: 443}`.
- `ingress` currently accepts `TLS_PASSTHROUGH` routes in virtual mode. Each route has a hostname and `tlsPassthroughConfig: {hostPort, containerPort}`; `hostPort: 0` defaults to `443`. Routes are rendered into local netproxy state, which selects by TLS SNI and relays the original TCP stream to a READY backend without TLS termination. The primary node reserves `:443` for the Web UI until both share one listener.
- Virtual-mode deployments publish endpoint status for `.internal` DNS discovery through the per-machine netproxy deployment.
- An environment variable of type `Address` selects a same-node virtual deployment and stores its deployment and space IDs. The consuming container receives that target's stable inbound IPv6 address `I` when it starts; run-scoped `O` is never exposed through Address refs. A target cannot be deleted, changed out of virtual networking, or moved to another space while Address references remain (see Config versioning).
- Workers reconcile cross-machine fixed tunnels and workload routes. Equivalent primary-node remote routing, unpublished candidate `O` distribution, policy, and public ingress remain incomplete.

### Config versioning

A deployment is versioned at two levels. `Deployment.version` is the
top-level version: a per-deployment monotonically increasing integer that
bumps on every change of any kind. Sub-parts with their own operations are
tracked beneath it: `Deployment.specVersion` bumps only when the spec bytes
change, and `Deployment.spaceVersion` only when the space assignment changes.
Storage is a single append-only event log, `deployment_event_log`: every
mutation appends one row carrying a full `Deployment` snapshot (`value`)
plus queryable version columns (`version`, `spec_version`,
`space_assignment_version`, `name_version`), unique on
`(deployment_id, version)` — which is also the CAS backstop every guarded
operation transitively rests on. The current desired state is the deployment's
highest-version event; the UI reconstructs the sequence of changes from the
per-deployment event range.

Deleting is its own event, `POST /v1/deployments/delete`, guarded by the
top-level version (the request's `version` must equal the current one + 1).
It bumps only the top-level version — the spec and other sub-parts are left
untouched, so `specVersion` remains strictly "times the spec changed" — and
the delete row carries the full config as the tombstone (`eventType` marks
it). Delete is terminal: the deployment leaves the live cache, so tombstones
are read back from the event log (scheduler teardown, the recently-deleted
view) and any further write attempt has no predecessor event to follow.

A space move is its own update kind, `assigned_space_update` in
`POST /v2/deployments/update`, guarded like every update kind by the
top-level version the caller observed (the request's `expectedVersion` must
equal the current one + 1), so a stale client cannot silently move a
deployment back. The space
feeds the workload's derived inbound address, DNS name, and issued TLS
identity, so a live placement is never mutated by a move: each scheduled
instance snapshots the deployment's space at scheduling time
(`scheduled_instance_event_log.space_id`) and keeps deriving for that space, while the
scheduler compares resolved space values and treats a pin/config mismatch
exactly like a superseded spec version, replacing the placement through the
normal rollover (or recreate) path (comparing values rather than rows means
an A-B-A move cancels cleanly). A move is validated like a create into the
destination: the caller needs create access there, the node must allow the
space, secret refs must satisfy own-or-global locality against it, the (node,
space, name) identity must be free, and it is refused with
`deployment_address_referenced` while other deployments hold Address
references to the moved deployment. Space 0 (the internal opendeploy space)
is excluded from moves in both directions: the destination must be between 1
and the maximum space ID, and a deployment in space 0 cannot be moved out.

The selected workload's `version` and `running` fields inside the persisted
`DeploymentSpec` are the only authoritative desired state.

## Deployment state

Each deployment's runtime state is structured into sections owned by different components:

### Workload desired state

Set by user actions (deploy or stop). The selected `ContainerSpec` contains
the target `version` and `running` boolean; `OpendeploySpec` carries only the
target `version` and is always running. Audit fields (`updated_at`, `author`)
and the config revision remain on the parent `Deployment`.

Nix desired versions, when set, are full immutable commit hashes. Branch selection and the 25 most recent commits are discovery aids and are not persisted as source authority. Creating a running Nix deployment, starting one, changing its target commit, or changing its Nix source while it remains running performs synchronous remote commit and flake verification before persistence. Stopped Nix deployments still require structurally valid source fields but may omit the desired version and do not require remote accessibility until they transition to running; a stopped deployment may also retarget its version for its next start.

### Scheduled instance target state

Set by the primary scheduler. It describes what a placement should be doing, and
nothing else: `RUN_SERVING`, `RUN_STANDBY`, `RUN_DRAINING`, `TERMINATE`, and
`FINALIZED`. A placement whose runner has stopped is finalized unconditionally —
whether a replacement exists, and whether anyone still wants to look at how it
ended, are not questions about its schedule.

A standby superseded by a newer config is terminated immediately rather than
drained. Only `RUN_SERVING` owns the instance's inbound address, so a standby has
nothing in flight to wait for; draining it would keep a placement the config has
moved past alive until some later version happened to succeed. This is what keeps
a rollout that fails repeatedly — a prepare that errors, a container that never
reports ready — from leaving one live placement per pushed version.

Finalized instances are therefore not display state. Storage retains the last
instance of each `(deployment, instance_ordinal)` after it is finalized, evicting
it once a newer instance is created for that ordinal, and rebuilds that view from
SQL on boot. It is offered only to the Web UI state stream; reconciliation and
routing snapshots never include a finalized placement, which owns no address.

### PreparerStatus

Driven by the preparer. Reported as two stages: `inputs` (resolving assets,
secrets, and configs) and `image` (producing the artifact). The runner-gating
rollup (`PREPARING`, `DOWNLOADING`, `PULLING`, `READY`, `FAILED`) is derived
from the pair, never stored or sent — see [engine.md](../engineering/engine.md).
On success, contains the resolved `artifact` (local image ref) and the
`deployment_spec_version` from `Deployment.SpecVersion`.

### RunnerStatus

Driven by the runner. Tracks the running container task with `running_pid`,
`running_artifact`, `status` (`NO_DEPLOYMENT`, `RUNNING`, `STOPPED`, `STARTING`,
`CRASHED`), `deployment_spec_version`, `number_of_restarts`, and `last_restart_at`.

## Deployment identification

Each deployment has an integer `id` (primary key) assigned when it is created via `POST /v1/deployments/create`. Human-readable metadata lives directly on `Deployment` (`name`, `spaceId`), and application identity is `{nodeId, spaceId, name}`. Active-identity uniqueness is a Go-level check under the store mutex on create and space move (the identity lives inside the event snapshots, so it cannot be a SQL constraint). All API requests, storage keys, and log file paths use the integer `id`.

Deleting a deployment releases its human-readable identity tuple but retains its ID, configuration history, status history, logs, volumes, and other ID-owned records. Creating a deployment later with the same space, node, and name creates a completely new and independent deployment with a fresh ID and version history. It does not restore, continue, or otherwise inherit the deleted deployment.

### Node space policy

Each node carries an `allowed_spaces` list, and a deployment cannot be placed on a node that does not allow its space. Enforced in `validateNodeAllowsSpace`, called from the only two places a (node, space) pair can arise: `PostV1DeploymentsCreate` and the `assigned_space_update` kind of `PostV2DeploymentsUpdate`. There is no third — no update kind carries a node field, so a deployment never moves nodes after creation.

**The policy defaults fully open and only ever narrows deliberately.** A new node is inserted allowing every space that exists at the time, and creating a space opens it on every existing node (`AllowSpaceOnAllNodes`). Without that second half the list would be a snapshot taken at enrolment, and the first deployment into a newly created space would fail on every node with nothing to explain why. Deleting a space strips it from every list, so ids of spaces that no longer exist do not accumulate.

**The _system space (space 0) is always allowed.** `normalizeAllowedSpaces` unions it back in on every read and every write, so a bad migration, a hand-edited database, or a future writer cannot produce a node that refuses internal deployments — and no caller has to remember to include it. The UI shows it ticked and locked rather than as a choice that silently would not take.

`POST /v1/nodes/allowed-spaces` replaces a node's list, keyed by node identifier to match `POST /v1/nodes/rename`. It rejects a list naming a space that does not exist, and rejects a narrowing that would strip a space out from under deployments already on that node — the same shape as refusing to delete a space with live deployments. So the stored policy can never contradict what is running.

When the allow list shipped, existing installs were backfilled with every space that existed at the time (that migration has since been swept); a node created today starts out allowing every space, so the policy binds only where an operator has deliberately narrowed it.

### Recovering a deleted deployment

A "See recently deleted" button on the status page toolbar opens a table of the
deployments deleted most recently, fetched from `POST /v1/deployments/recently-deleted`.
Each row offers **Fork**, which opens the create form seeded with the deleted
deployment's spec.

There is deliberately no restore. A deployment's ID owns its volumes, logs, run
history, and network address; deletion is defined above as releasing the identity
while keeping all of that attached to the ID. Reviving the ID would silently
re-attach volumes and logs an operator has already discarded, and could fail
outright once another deployment has claimed the released identity tuple. A fork
is an ordinary create, so it passes the same validation as any new deployment —
identity uniqueness, node registration, host-port and ingress conflicts, address
references, and source verification.

Because the identity tuple was released, a fork of a deleted deployment keeps its
name, space, and node prefilled; forking a live deployment clears them, since
reusing that tuple would collide. If another deployment claimed the tuple in the
meantime, create rejects it with the usual duplicate-identity error.

## Deployment status display

The status page shows one table row per deployment, sorted with
OPENDEPLOY last, then by space, name, node, and id. Deployments can also be
grouped by space. The per-node internal `opendeploy` and `opendeploy-net`
deployments are the exception: each is presented as a single merged row whose
cells split vertically into one subline per node (secondaries first, primary
last). Their Update action opens a group upgrade overlay listing every node
with its current version and a target-release dropdown; an "Align versions"
toggle (on by default) locks all dropdowns to the primary's selection. The
browser then performs the rollout itself — one node at a time, waiting for
each node to report the new version, primary last, stopping at the first
failure. With the toggle off, only nodes whose dropdown was explicitly
changed are upgraded; untouched nodes are skipped. The `opendeploy` and
`opendeploy-net` groups are deliberately uncoupled: upgrading one group never
touches the other. The toolbar above the table holds the deployment search box
and "See recently deleted" on the left, and the grouping and opendeploy toggles
plus "Add deployment" and "Export" on the right. Each row displays:

- Deployment name, space, and node
- Status and version columns with one vertically stacked subcell per live scheduled instance, oldest first. During rollover, this shows the old and replacement instances together. An instance ordinal with no live instance — a stopped or deleted deployment — instead shows the last instance it ran, so the row still reports how that run ended. Versions use the pinned target until the runner reports its version; runner status badges are clickable to view run output.
- Restart count and last restart time for the newest scheduled instance
- Prepare status with link to prepare output (build/import/pull log)
- Deployment audit metadata plus History, Update, config, fork, and delete actions

The table is the "Overview" tab of the Deployments page. Update, Add
deployment, Fork, and restoring a recently deleted deployment each open an
editor as a further full-page tab beside it, so several edits can be open at
once; a tab closes on save or Cancel, and asks before discarding unsaved
edits. The editor has a UI form and an HCL Code surface over the same
document; Code is the default and the last choice is remembered per browser.
In HCL, `node`, `name`, and `space` sit directly in the `deployment` block.

## Source validation and versions

Source checks run only when the user asks. The editor footer shows the
source state as the union of its layers (repository, flake path, and flake
target for Nix; image for images): not validated, validating, valid,
invalid, or unchanged for an existing deployment whose source fields are
untouched. Validate checks the repository and lists its branches and the
selected branch's commits (or checks the image and lists its tags); picking a
commit checks the flake file at that commit. Editing any source field drops
the state back to not validated and clears the loaded versions. The version
picker beside it is disabled until the source is valid or unchanged, lists
the newest 25 commits of a branch (or the image's tags) with the deployed
version marked, and has its own refresh. Saving a running deployment
requires a valid or unchanged source and a selected version; saving it
stopped requires only well-formed fields.

## Deploy workflow

1. The user clicks "Update" on a table row. An unchanged source is trusted
   without any request; opening the version picker lists versions with one
   `POST /v1/deployments/versions` request (25 most recent commits for the
   branch carrying the deployed commit, or available image tags). Editing the
   repository or flake path requires Validate before the deployment can be
   saved running.
2. The user picks a version (and optionally edits the deployment spec) and submits.
3. The frontend calls `POST /v2/deployments/update` — a `version_only_update`
   carrying the target version, or a `spec_update` with the new typed `spec`
   (which carries the workload version and running state inside it) if the
   spec was edited.
4. For an effective running Nix transition, the backend verifies the exact remote commit and regular `flake.nix` tree entry, then writes the spec with the selected workload's version and `running=true`, and bumps `Deployment.Version`. Verification failure writes nothing.
5. The operator's reconciliation loop picks up the change and starts a
   preparer.
6. The preparer resolves runtime inputs, then clones/fetches, pulls, or
   imports the image, and publishes a preparer status whose rollup is `READY`.
7. The operator creates a runner, which writes `RunnerStatus.Status =
   STARTING` then `RUNNING` with the PID.

For container deployments with `upgradeStrategy = ROLLOVER`, step 7 starts a candidate container before stopping the old one. The candidate receives `OPENDEPLOY_READINESS_SOCK_PATH=/run/opendeploy/readiness.sock`; once warmup is complete it writes `ready\n` to that Unix socket. With virtual networking, the candidate can bind the same container port immediately in its own network namespace. It already has preferred, routed `O` and non-preferred `I`; OpenDeploy promotes it by flipping the `I` host route and any `portForwarding` rules, then stops the old container. Promotion does not change its addresses or source preference, so it keeps using `O` for outbound traffic until that run ends. With host networking, rollover is cooperative: the candidate shares the host network namespace with the old container, so it must not bind conflicting host ports before signaling readiness. After readiness, it should wait until OpenDeploy stops the old container and the port becomes free, then bind and serve.

## Crash recovery

The container runner owns crash recovery directly: on task exit it writes
`RunnerStatus.Status = CRASHED`, sleeps for an exponentially increasing delay
(1 second to 1 hour, doubling per consecutive crash), and respawns the same image.
`number_of_restarts` increments on each respawn and resets on new deployments.
If the container runs stably for 15+ seconds before crashing, the local crash
counter is reset.

## Deployment history

The history sidebar shows a chronological log of all deployment config and status changes. Config entries show the version number and what changed (version deployed, running toggled, deleted). Status entries show preparer and runner state transitions (diff-rendered against the previous entry so unchanged sections aren't repeated). All entries are fetched via `POST /v1/deployments/history` with the integer deployment ID. History is stored in `deployment_event_log` (`UNIQUE (deployment_id, version)`, one full-snapshot event per change — spec updates, space moves, and the delete) and `scheduled_instance_status` (PK `scheduled_instance_id, updated_at`), the append-only status log covering every scheduled instance of the deployment; `idx_scheduled_instance_status_deployment` covers the `deployment_id`-leading lookup.

## Empty state

When no deployments exist, the status page displays "No deployments configured. Create a deployment config first." Clicking "Add deployment" opens the typed deployment form.
