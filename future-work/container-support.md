# Container support — remaining work

Phase 1 (the `container` runner + `containerImage` preparer, host networking,
opendeploy-owned supervision, default data volume, anonymous pull, bundled+pinned
containerd) has shipped. Its design and behaviour are documented in
[docs/engineering/engine.md](../docs/engineering/engine.md) (containerRunner,
ContainerImagePuller, the dedicated containerd) and
[docs/product/deployments.md](../docs/product/deployments.md) (the `container`
runner and `containerImage` prepare variants).

This file tracks the work deliberately left out of phase 1.

## Logging follow-ups

Container stdout/stderr are received by a per-task binary log consumer. It
writes merged half-hourly UTC files to
`RunOutputDir/{deploymentID}/{YYYYMMDD_HHMM}_{version}_{run}.logbin`.
`version` is the deployment configuration version and `run` is its restart
sequence, so concurrently running rollover candidates write separate files.

Known gap: there is no retention or archival policy for completed log files.

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
