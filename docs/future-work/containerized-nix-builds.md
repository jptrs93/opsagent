# Containerized Nix builds

Exploratory notes on replacing the host Nix toolchain in `nixdocker.Preparer`
with a build container. No implementation is planned yet.

## Current build path

- `backend/lib/engine/prepare/nixdocker/nixdocker.go` checks containerd for a
  cached image ref, checks out the repo through `repo/git.Manager`, runs
  `nix build` as a host subprocess, then executes the resulting stream script
  and pipes it into `ctrd.Client.Import`.
- Host prerequisites are a `nix` binary on `PATH`, a populated `/nix`, a
  daemon unit, and `/etc/nix/nix.conf`. The service unit hardcodes
  `/nix/var/nix/profiles/default/bin` in `PATH`.
- Nix is installed out of band. The installer does not manage it.
- Flake outputs use `dockerTools.streamLayeredImage`. The produced script has a
  `/nix/store` shebang, so it can only execute where `/nix` exists at the
  canonical path.

## Security assessment

- The test harness configures `sandbox = false`, `allowed-users = *`, and
  `trusted-users = root opendeploy` (`testing-vms/test-orchestrator/main.go`).
  Upstream Nix defaults to `sandbox = true` on Linux.
- With `sandbox = false` a build runs as `nixbld*` with no chroot, no mount
  namespace, and no network namespace. It has host filesystem read subject to
  unix permissions, unrestricted network access, and no cgroup limits.
- Reachable over the network from an unsandboxed build: cloud instance
  metadata, the machine-local virtual network and every deployment inbound
  address `I` on the node, localhost control-plane listeners, and arbitrary
  egress.
- Secrets are protected by directory permissions rather than by Nix. `ainit`
  creates `DataDir`, `GitCacheDir`, and `GitWorktreesDir` at `0750`; the
  installer chmods `machine.key` and `primary.db` to `0600`. `nixbld*` is not
  in the `opendeploy` group.
- Resolved. `EnsureCheckout` previously ran `git remote set-url origin` with the
  token embedded, leaving it at rest in `{GitWorktreesDir}/<repo>/.git/config`
  and readable from `/proc/<pid>/cmdline` while any git command ran. Clone URLs
  are now credential-free and the token is injected per invocation as a
  github.com-scoped `http.extraHeader`. See `docs/engineering/engine.md`.
- Nothing asserts that Nix is a multi-user install. On a single-user install
  builds run as the `opendeploy` user, which can read `machine.key` and
  `primary.db`.
- A build with no cgroup limit can exhaust node memory and stop the control
  plane and all running deployments.

## Isolation comparison

- `sandbox = true` gives each derivation a mount namespace chrooted to declared
  store inputs, new PID/IPC/UTS namespaces, a user namespace, and a
  loopback-only network namespace. For filesystem and process isolation this is
  at least as strong as a container, because it whitelists the closure rather
  than shipping a userland.
- Fixed-output derivations run in the host network namespace by design. This
  covers `fetchFromGitHub`, `fetchNpmDeps`, Go `vendorHash` fetches, and cargo
  vendoring. No Nix setting narrows it. A fixed-output derivation has no host
  filesystem visibility, so the exposure is network pivot and beaconing rather
  than secret exfiltration.
- Nix installs a seccomp filter for store purity, not for attack-surface
  reduction. containerd applies a default profile that blocks additional
  syscalls.
- Nix cgroup support is experimental and off by default. containerd always
  creates a cgroup.

## Agreed scope

- The preparer checks out the Git repository on the host to the target commit,
  as it does today, and bind-mounts the checkout into the build container
  read-only. Git authentication stays on the host. A read-only mount is required
  because `EnsureCheckout` reuses one directory per repository across builds.
- The build container receives no credentials. All flake dependencies must be
  vendored or publicly fetchable. This is a product constraint on user flakes
  and needs a distinct error rather than a raw Nix failure. It excludes private
  flake inputs and authenticated binary caches. No credentials does not mean no
  network: the container still needs egress for `cache.nixos.org` and for
  fixed-output derivation fetches.
- `/etc/nix/nix.conf` is generated and owned by OpenDeploy and mounted
  read-only. With no credentials in scope, v1 needs no `netrc`, no
  `access-tokens`, and no `SecretRef` plumbing; `substituters`,
  `trustedPublicKeys`, `maxJobs`, and `cores` can be exposed later as plain
  settings. Operator edits to the generated file do not survive.
- The Nix store is one global volume. Per-repo volumes contain poisoning to a
  single trust domain and allow concurrent builds across repositories, but
  duplicate the 2-5 GB common closure per repository and force a cold
  substituter fetch for each new one. Derive the store path from a store-key
  function returning a constant so the choice can be revisited cheaply. If
  isolation later matters, prefer per-repo stores plus a shared local binary
  cache as a substituter over plain per-repo stores.

Open items in this scope:

- Whether `sandbox` is enabled inside the build container. Nested user
  namespaces conflict with the containerd seccomp profile, so `sandbox = false`
  with the container as the sole boundary is the pragmatic default. Under that
  choice normal and fixed-output derivations share one netns, so a single egress
  policy covers both. Nix's own sandbox cannot do this: it gives fixed-output
  derivations the host network unconditionally.
- Whether a partial clone can trigger a lazy object fetch during the build.
  `EnsureCheckout` uses `--filter=blob:none` and leaves a promisor remote
  configured. `checkout --force FETCH_HEAD` materializes the working tree, so
  the needed blobs should be local, but a lazy fetch inside the container would
  require both network and credentials. Verify on a real build.
- Garbage collection becomes mandatory once OpenDeploy owns the store. `--no-link`
  leaves no GC roots and the containerd image is the durable artifact, so the
  store is a pure cache and anything may be deleted. Prefer a size-bounded
  policy; an age-based one tends to clear the whole cache at once.

## Container design sketch

- Build containers run through the bundled containerd, so the node prerequisite
  set reduces from `{containerd, nix}` to `{containerd}`.
- Pin a `nixos/nix` image by digest and pull it through `ctrd.Pull` on first
  use.
- Bind-mount a host directory such as `/var/lib/opendeploy-nix/nix` at `/nix`.
  A plain bind mount preserves native filesystem performance and content
  addressed cache reuse. No fuse and no overlay in the build path.
- The image ships its own `/nix`, so an empty mount at that path removes Nix
  itself. Seed once by running the image with the host directory mounted
  elsewhere and `nix copy --no-check-sigs --to "local?root=/seed"`, which
  registers store paths and the SQLite database. Image upgrades re-seed
  additively; store paths are content addressed and do not collide.
- The stream script must execute inside the container. Run build and stream as
  one invocation and capture container stdout on the host for `ctrd.Import`.
  `RunTask` wires stdout into the binary log consumer for long-lived runners,
  so `ctrd` needs a one-shot run path using `cio.WithStreams`. The alternative
  is writing the tar to a mounted directory and importing from the file, at the
  cost of one image-sized write and read.
- Container startup and pull costs apply only on a cache miss. The containerd
  image ref check short-circuits before any container starts.

## Trade-offs

- Gained: Nix removed from the install and uninstall path, per-node Nix version
  drift removed, egress policy over fixed-output derivations using the existing
  netns and nftables machinery, cgroup limits, and a boundary independent of
  directory permissions.
- Lost: a shared writable `/nix` host directory lets one build poison the cache
  for every deployment on the node. The multi-user daemon prevents this today.
  Running the Nix daemon in a long-lived container and having per-build
  containers use its socket preserves the current guarantee.
- Nested user namespaces conflict with the containerd seccomp profile, so the
  build container would likely run `sandbox = false` internally and rely on the
  container as the boundary. Nesting both requires relaxing seccomp.
- OpenDeploy inherits the `nix.conf` surface. Custom binary caches and
  substituters become a config API concern.
- Builds depend on the build image being pullable. Air-gapped nodes and
  registry outages become a new failure mode.
- Mock-mode E2E maps `cache.nixos.org` through `/etc/hosts` and a test CA.
  `RunTask` already applies `oci.WithHostHostsFile`; the test CA bundle would
  need mounting.
- The store becomes a pure cache because the durable artifact is the containerd
  image and `--no-link` leaves no GC roots. Age or size based collection is
  safe and is not possible today.

## Open questions

- Why `sandbox = false` is set. The likely cause is that mock mode resolves
  `github.com`, `api.github.com`, and `cache.nixos.org` through `/etc/hosts`,
  while the Nix sandbox substitutes a minimal `/etc/hosts`. If so the fix is
  `sandbox-paths = /etc/hosts=/etc/hosts` and production can run sandboxed. If
  real builds depend on impure host state, the flag cannot be flipped and the
  container becomes the only route to isolation.

## Sequencing

- Security and install simplification are separate arguments. `sandbox = true`
  closes most of the current exposure as a configuration change; the residual
  is fixed-output derivation egress, cgroup limits, and seccomp coverage.
- Independent of the container decision: stop persisting the GitHub token into
  the worktree `.git/config` by using `GIT_ASKPASS` or a per-invocation
  `-c http.extraHeader=`, and assert or document the multi-user Nix
  requirement.
- Estimated container implementation effort is two days to a working path and
  three to five days to production confidence. Risk concentrates in store
  seeding, build image upgrades, and validation on real Linux nodes. Most of
  `nixdocker.go` is unchanged: image ref derivation, cache check, checkout,
  validation, import, log streaming, and cancellation.
