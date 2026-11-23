# Root agent and runtime deployment

The agent runs as root, and containerd and runc become a third per-node
system deployment that upgrades through the same group rollout as
`opendeploy` and `opendeploy-net`. The first change removes a privilege
boundary that does not exist; the second removes the last operation that
needs a root login on every node. This note records the current state, the
design of both changes, the migration, and the open items.

This note extends [../engineering/engine.md](../engineering/engine.md),
[../engineering/security-severity.md](../engineering/security-severity.md)
and the system deployment group behaviour in
[../product/deployments.md](../product/deployments.md).

## Status

Proposed 2026-09-12. Part 1 was implemented on 2026-11-24; the section below
records the shipped form and where it departs from the proposal.
[../engineering/engine.md](../engineering/engine.md) (Host privileges) is the
reference for the current behaviour, and "Current state" below describes the
state before the change.

Part 2 is superseded for now by startup reconciliation, shipped 2026-09-13:
the agent compares the containerd and runc symlinks under
`/var/lib/opendeploy/runtime/bin` with the versions pinned in
`lib/runtimebin` before anything else starts, and when a pin moved it
downloads and verifies the component, flips the symlinks, restarts
`opendeploy-containerd.service` through the existing sudoers rule, waits for
the daemon to report the new version, and rolls the symlinks back if it does
not. It never downgrades and never blocks boot: a failure is logged and the
node continues on the installed runtime. A release that moves the pin
therefore reaches every node on its next agent restart, including the agent
upgrade from the Deployments page, and `opendeploy upgrade` runs the same
code. Nodes report the installed versions in their status and the Cluster
page shows them. The pin table, download, checksum and symlink code moved
out of the installer into `lib/runtimebin` so the installer and the agent
cannot drift.

What this does not give: a per-node hold-back, an independent rollback of
the runtime, or an operator-visible run log for the swap. Part 2 remains the
design if those become necessary, and the manifest, preparer and runner
sections still describe how it would be built.

## Current state

### Privileges

The service runs as the `opendeploy` user with these ambient capabilities:
`CAP_NET_BIND_SERVICE`, `CAP_CHOWN`, `CAP_DAC_OVERRIDE`,
`CAP_DAC_READ_SEARCH`, `CAP_SYS_ADMIN`, `CAP_NET_ADMIN` and
`CAP_SYS_PTRACE`. `NoNewPrivileges` is off so the agent can call `sudo`. A
sudoers drop-in at `/etc/sudoers.d/opendeploy` allows `systemctl` restart,
stop and start of `opendeploy.service`, the `systemd-run --no-block` form of
the restart, and a restart of `opendeploy-containerd.service`. The containerd
socket is group-readable by the `opendeploy` group through the `gid` in
`config.toml`.

The process is root-equivalent by four independent routes, each a few lines
of code: `CAP_DAC_OVERRIDE` writes any file, including `/etc/sudoers` and
root's `authorized_keys`; `CAP_SYS_ADMIN` mounts and enters namespaces;
`CAP_SYS_PTRACE` attaches to root-owned processes; and the containerd socket
starts a privileged container with the host root mounted, which is also the
agent's legitimate job for host mounts. The capabilities it lacks add a step,
not a boundary.

Ambient capabilities survive `execve`, so every child the agent starts holds
the same set: `git` for repository access, the litestream backup child, and
`sudo`. Nothing clears the ambient set before a child runs. The 2026-09-11
audit's critical finding OD-01 was this shape, an exporter running with the
service's capabilities and socket. It was closed by moving the work into a
container; the next host-side child produces the same finding again.

The uid split provides file ownership attribution and a handful of uid-keyed
kernel checks. It provides no security boundary.

The installer branches on `isRoot()` throughout: a fresh install needs root,
an upgrade may run as `opendeploy` and then skips the runtime, and an
unprivileged upgrade fails with "upgrade requires root once" when a state
directory is missing. The sudoers drop-in is written only by a fresh install;
the containerd restart rule dates from 2026-07-07, so nodes installed before
that never receive it.

Ingress is already outside this process. Netproxy, the per-node
`opendeploy-net` container, terminates HTTPS and forwards TLS passthrough in
its own network namespace. The root-equivalent process hosts the web UI and
API listener and the cluster and enrollment mTLS listeners.

### Runtime upgrades

containerd and runc are pinned in `backend/lib/runtimebin` with
per-architecture checksums, installed under
`/var/lib/opendeploy/runtime/versions/<name>-<version>/`, and selected by
symlinks in `/var/lib/opendeploy/runtime/bin/`. The installer provisions them
on a fresh install, and every agent start reconciles them against the pin
(see Status). Before Part 1 the agent did this unprivileged: its ambient
`CAP_DAC_OVERRIDE` let it write the root-owned runtime directories and the
sudoers drop-in allowed `systemctl restart opendeploy-containerd.service`.
Both units use `KillMode=process`, so containers survive a containerd
restart, and the container runner re-issues its wait when the stream breaks.

## Part 1: the agent runs as root

Implemented 2026-11-24.

The unit sets `User=root`, `Group=root` and `NoNewPrivileges=yes`, and drops
`AmbientCapabilities`. The sudoers drop-in, the `sudo` indirection, the
socket `gid` and the `opendeploy` account are gone from fresh installs.
Self-restart keeps the `systemd-run --no-block systemctl restart` form,
because that avoids the cgroup teardown killing the restart command.

The security severity guide states the trust boundary: the agent is the
node's root control plane, and agent compromise is node compromise by design.
The boundaries that count are workload to agent or host, network or API to
agent, node to cluster, and user to API. A finding of the form "the agent
could do X on the host" is not a finding.

The installer has one path. Fresh install and upgrade both require root, the
`isRoot()` branches and the unprivileged upgrade are removed, and the
"requires root once" states no longer exist. Every runtime root is
root-owned; deployment volumes keep their per-workload ownership.

### Departures from the proposal

- **Host-side children run as root.** The proposal dropped `git` and the
  backup child to the `opendeploy` uid. That uid is weaker than it looks: the
  containerd socket was group-readable by `opendeploy`, and uid 999 collides
  with the user of common images (a Postgres container's data files are
  owned by 999), so a process under it reads and writes workload data.
  Fixing both needs a dedicated uid and more mechanism. `git` as root holds
  no more than it did with seven ambient capabilities and the socket. The
  isolation step for `git` is a container (see Open items).
- **The `opendeploy` account is removed**, not kept. Nothing runs under it.
- **Existing nodes are converted by hand.** The proposal left
  `opendeploy`-owned files in place. Upgraded nodes are instead chowned to
  root over SSH so they match a fresh install, which is the only layout the
  e2e suite exercises. The commands are in
  [../../upgrade_log.md](../../upgrade_log.md).
- **No startup reconcile of units and `config.toml`.** The agent does not
  rewrite its own unit or the containerd unit. `opendeploy upgrade` as root
  does, and restarts containerd when `config.toml` changed.
- **No mount-namespace hardening.** `PrivateTmp`, `ProtectHome` and
  `ProtectKernelLogs` give the service a private mount namespace. The agent
  bind-mounts named network namespaces under `/run/netns` and containerd
  must see them, so these stay off with `ProtectSystem`. Only
  `NoNewPrivileges` is set.
- **A transition fallback.** An agent of this release that starts under the
  old unprivileged unit keeps working: it logs a warning and prefixes its
  two `systemctl` restarts with `sudo -n`. The version is rolled out from the
  Deployments page first and each node is converted afterwards, because a
  node that runs a build other than its deployment's target restarts into
  the target. The fallback can be deleted once no node runs the old unit.

The alternative of keeping the uid split and moving the privileged
operations behind a small root helper is the long-term split recorded under
open items; it is the right end state if the threat model comes to include
hostile space administrators or shared nodes, and it is not blocked by
running as root now.

## Part 2: containerd and runc as the `opendeploy-runtime` deployment

### Identity and version

`opendeploy-runtime` is a third internal identity in space 0, one deployment
per node, seeded at enrollment beside `opendeploy` and `opendeploy-net`. Its
version list is the opsagent release list that the other two already use, and
"runtime vX.Y.Z" means the containerd and runc versions that release pins.
This keeps one code-reviewed table as the source of truth and avoids a
separate version catalogue.

### Manifest

The release build generates `runtime-manifest.json` from the installer's
table and publishes it as a release asset covered by the release's
`sha256sums.txt`:

```json
{
  "components": [
    {"name": "containerd", "version": "2.3.5",
     "url": {"amd64": "https://github.com/containerd/containerd/releases/download/v2.3.5/containerd-2.3.5-linux-amd64.tar.gz"},
     "sha256": {"amd64": "2f0a…dd44"},
     "binaries": ["containerd", "containerd-shim-runc-v2", "ctr"]},
    {"name": "runc", "version": "1.5.1",
     "url": {"amd64": "https://github.com/opencontainers/runc/releases/download/v1.5.1/runc.amd64"},
     "sha256": {"amd64": "177d…f6f"},
     "binaries": ["runc"]}
  ]
}
```

### Preparer

The preparer fetches the selected release, verifies the manifest against
`sha256sums.txt`, downloads each component from its upstream URL, verifies
the sha256, and lays the binaries into the versions directory. A component
already present at the pinned version is not downloaded, so a release that
does not move the runtime prepares instantly. The stage and apply code moves
out of the installer into a shared `lib` package that the fresh install and
the preparer both call.

The agent self-upgrade preparer gains the same verification. Today it
downloads the release binary over HTTPS without checking it against
`sha256sums.txt`; only the installer path verifies.

### Runner

The runner repoints both symlinks atomically and restarts containerd only if
a symlink changed. It then confirms that the daemon's version API reports the
expected version within a bounded wait and that the count of running tasks is
unchanged. On failure it repoints the symlinks at the previous version
directory, restarts again, and reports the run as crashed with the reason in
its log. The previous version directory is kept for this; older ones are
pruned. The reported version comes from the daemon, not the symlink, so the
group row shows real drift.

After a successful restart the runner resets the agent's containerd client so
it re-reads the daemon version. The client decides the log scheme, `binary`
or `binary-v2`, once per connection, and new tasks must pick up the new
daemon's scheme without an agent restart.

Existing containers keep the shim they were started with until their next
rollover; the runner writes that to its log. A "roll workloads to adopt the
new shim" action is deferred until a shim vulnerability makes it necessary.

### Rollout and UI

The status page's system-group rule extends to the third name. The existing
overlay does the rest: one node at a time, wait for the node to report the
new version, primary last, stop on the first failure, with "Align versions"
locking every node to the primary's selection. The groups stay uncoupled, so
a runtime release can be held back while agents move, or applied on its own.

### Compatibility

No ordering constraint between the groups is enforced. The agent adapts to
the daemon it finds, and containerd's API is compatible across 2.x. The pairs
that are tested are the agent and the runtime of the same release, which is
what "Align versions" produces.

### e2e coverage

The harness already mirrors the containerd tarball and runc binary and
publishes a mock upgrade release. The mock upgrade release's manifest pins a
second mirrored runtime version, so the case exercises a real swap: upgrade
the runtime group, confirm the daemon version on each node, confirm every
running deployment kept its container and run number, and confirm a
deployment started after the swap logs through the new scheme.

## Open items

- `git` in a container, on the pattern of the Nix build: the pinned build
  image, the Git cache mounted read-write, no host environment, the build
  network namespace, and one container per operation rather than per
  command. Checkouts for Nix builds are the first candidate because a build
  already takes minutes. Branch and commit listings and the commit check on
  save run on interactive paths and would each gain a container start; that
  cost is not measured.
- Removing the transition fallback (`sudo -n` when the agent is not root)
  once every node runs the root unit.
- Whether the agent group upgrade should offer to align the runtime group in
  the same overlay, with the default off.
- The long-term split into a root node helper and an unprivileged process
  holding the web UI, API, git, logs, metrics and the primary database.
- Whether `ctr` keeps shipping. It is unused by the agent and useful to
  operators; the default is to keep it.
