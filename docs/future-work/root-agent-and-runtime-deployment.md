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

Proposed 2026-09-12. Nothing is implemented. The runtime bump to containerd
2.3.5 and runc 1.5.1 in v0.0.597 still reaches existing nodes only through a
root `opendeploy upgrade` on each node. The root change lands first,
because it removes the sudoers and socket-group machinery the runtime
deployment would otherwise depend on.

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

The installer pins containerd and runc in `backend/app/installer/config.go`
with per-architecture checksums, installs them under
`/var/lib/opendeploy/runtime/versions/<name>-<version>/`, points symlinks in
`/var/lib/opendeploy/runtime/bin/` at them, and restarts containerd only when
a symlink changed. Only the root install path runs this. The in-app agent
upgrade replaces the agent binary and never touches the runtime, so a runtime
bump is a manual root login per node, once per release that moves the pin.
Both units use `KillMode=process`, so containers survive a containerd
restart, and the container runner re-issues its wait when the stream breaks.

## Part 1: the agent runs as root

The unit sets `User=root` and drops `AmbientCapabilities`. The sudoers
drop-in, the `sudo` indirection and the socket `gid` go away. Self-restart
keeps the `systemd-run --no-block systemctl restart` form, because that
avoids the cgroup teardown killing the restart command; only the `sudo`
prefix is removed. `NoNewPrivileges` turns on, since nothing needs to gain
privilege through `execve` any more.

The security severity guide states the trust boundary explicitly: the agent
is the node's root control plane, and agent compromise is node compromise by
design. The boundaries that count are workload to agent or host, network or
API to agent, node to cluster, and user to API. A finding of the form "the
agent could do X on the host" is not a finding. This paragraph, more than the
uid, is what stops the recurring audit flags.

Host-side children lose privilege instead of inheriting it. The `opendeploy`
account stays as the unprivileged identity for them: root starts `git` and
the backup child under that uid through the process credential, with a
minimal environment. A uid change away from root clears the capability sets,
so the children run with none. Today they run with all seven.

Systemd hardening that suits a root process manager stays or is added:
`PrivateTmp`, `ProtectHome`, `RestrictSUIDSGID`,
`SystemCallArchitectures=native`, `ProtectClock`, `ProtectKernelLogs`.
`ProtectSystem` stays off for the reasons already recorded in the unit.

The installer has one path. Fresh install and upgrade both require root,
the `isRoot()` branches and the unprivileged upgrade go, and the "requires
root once" states disappear. Data directories are root-owned except those a
child or a workload must write. Deployment volumes keep their per-workload
ownership. Existing `opendeploy`-owned files on upgraded nodes need no
migration; root reads and writes them as they are.

Because the agent is root, it also reconciles its own unit, the containerd
unit and `config.toml` against the templates embedded in its binary at
startup, rewriting and reloading when they differ. Configuration drift
between nodes and releases heals on the next agent upgrade, and no root
login is needed after the transition.

Transition: one root `opendeploy upgrade` per node, because the
unprivileged self-upgrade cannot change `User=` and cannot reload systemd.
That is the same manual step as the runtime bump, and it is the last one.

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

- Whether host-side children run under the existing `opendeploy` account or
  a dedicated one, and whether `git` should move into a container as the
  Nix build did.
- Whether the agent group upgrade should offer to align the runtime group in
  the same overlay, with the default off.
- The long-term split into a root node helper and an unprivileged process
  holding the web UI, API, git, logs, metrics and the primary database.
- Whether `ctr` keeps shipping. It is unused by the agent and useful to
  operators; the default is to keep it.
