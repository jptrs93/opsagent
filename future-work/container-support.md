# Container support — remaining work

Phase 1 (the `container` runner + `containerImage` preparer, host networking,
opendeploy-owned supervision, default data volume, anonymous pull, bundled+pinned
containerd) has shipped. Its design and behaviour are documented in
[docs/engineering/engine.md](../docs/engineering/engine.md) (containerRunner,
ContainerImagePuller, the dedicated containerd) and
[docs/product/deployments.md](../docs/product/deployments.md) (the `container`
runner and `containerImage` prepare variants).

This file tracks the work deliberately left out of phase 1.

## Logging (immediate next change)

Phase 1 discards container stdout/stderr (`cio.NullIO`) — there is no run-log
output for container deployments yet. The next change wires it up.

containerd does no log management (no `docker logs`, no json-file driver, no
rotation): it hands the task its stdio and the caller decides where it goes. The
shim creates FIFOs for stdout/stderr, and something must drain them or the
container blocks when the pipe buffer fills.

Plan: give the task `cio.LogFile(apigen.RunOutputFile(deploymentID, configVersion))`
so the **shim itself** writes stdout+stderr straight to the same
per-deployment/version run-log file `osProcess` uses
(`RunOutputDir/{deploymentID}_{version}`). This reuses the existing "view run
output" UI streaming (`streamRunLog`) and the `RunOutputRequest.version` selector
unchanged. Two properties fall out of the shim owning the file:

- **Survives opendeploy restarts** — the shim keeps writing while opendeploy is down,
  so there is no log gap across restarts (better than draining a FIFO inside
  opendeploy's process, which would drop output during a restart).
- **Reattach needs nothing** — on reattach the shim is still writing to the same
  path; opendeploy re-streams the existing file.

Known gap (shared with osProcess today): `cio.LogFile` is append-only with **no
rotation**, so a chatty container grows the file unboundedly. Rotation is a
shared future concern (a `binary://` logging URI, a logging plugin, or
lumberjack-style rotation like opendeploy's own server log), not a blocker. Avoid
containerd's CRI-style `binary://` logging — that is the K8s log-format path
opendeploy does not need.

## Future phases

- **Resource limits** — cgroup memory/CPU caps via OCI `linux.resources`
  SpecOpts on the container runner. The config struct is shaped to take them.
- **Isolation hardening** — drop the default capability set to least-privilege,
  add a seccomp profile, mount/PID namespace separation, `no-new-privileges`.
  Phase 1 keeps containerd defaults (permissive, Docker-equivalent).
- **Privileged ports under a non-root user** — add `CAP_NET_BIND_SERVICE` to the
  *ambient* set when `user` is non-root (the default cap set covers root only).
- **Registry credentials** — a resolver with auth (per-deployment or global
  registry creds, likely via the secrets store). Phase 1 is anonymous pull only,
  so private images fail.
- **Network isolation** — CNI/bridge + port mapping, to run multiple instances
  of a service on one host without port collisions. Phase 1 is host-net only.
- **Tag/version listing** — registry tag enumeration in the deploy overlay (needs
  credentials + registry API). Today the user types the tag/digest manually.
- **`nixImage` preparer** — `dockerTools.buildLayeredImage` → import into
  containerd: daemonless image builds from the existing nix machinery.
