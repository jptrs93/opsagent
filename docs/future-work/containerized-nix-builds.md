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

- Scope note (2026-08-24): the only place in the repo that configures Nix is
  `testing-vms/test-orchestrator/main.go`, which writes `/etc/nix/nix.conf` in
  the node VMs. OpenDeploy does not manage Nix installation or configuration on
  real nodes, so the exposure below described the *test harness* posture;
  production runs whatever the operator's out-of-band install produced, and
  upstream defaults to `sandbox = true` on Linux. The harness previously set
  `sandbox = false`, meaning e2e validated a build path production likely does
  not use — a sandbox-incompatible flake could pass CI and fail on a real node.
  That flag is now `sandbox = true` so the harness is representative.
- The harness also installs `nix-setup-systemd` and enables `nix-daemon`, so
  test nodes are multi-user daemon installs: builds already run as `nixbld*`
  with no store write access, and store poisoning is already prevented there.
  The single-user concern below remains unasserted rather than observed.
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
- What `sandbox = true` is and is not worth, stated by threat: it closes host
  filesystem access essentially completely (the `machine.key` / `primary.db` /
  other-repo-checkout axis, and it does so on single-user installs too), and it
  closes network for *normal* derivations — which is where dependency-supplied
  code runs (npm `postinstall`, cargo `build.rs`, `go generate`, gradle
  plugins). Against a **compromised dependency**, the realistic threat given
  flakes come from the deploying user's own repo, it is therefore highly
  effective. Against a **deliberately hostile flake author** it is weak: the
  author controls the derivation structure and can simply declare a
  fixed-output derivation with any hash, since the build body runs before the
  hash is checked, retaining host-network code execution. So it closes most of
  the severity, not most of the surface — and the residual is exactly what the
  container targets, where one netns covers normal and fixed-output alike.

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

## Store poisoning and privilege separation

Why a shared writable `/nix` is dangerous, and what actually protects it:

- Store paths are input-addressed: the hash in `/nix/store/<hash>-name` derives
  from the derivation, not the output bytes. The NAR hash in `db.sqlite` is
  checked only by `nix store verify`, never at use time. Whoever can write the
  store can silently replace any path (or the database) and every later cache
  hit on the node embeds the tampered artifact into other deployments' images.
- Lowered privilege is not the mechanism; privilege separation between the
  process running the untrusted build script and the process writing the store
  is. A build that runs as one unprivileged uid which also owns `/nix` has full
  poisoning ability — that uid executes the flake's build script.
- Nix's multi-user design provides the split: only a root store-writer creates
  output paths and registers them, and builders are forked as `nixbld*` users
  with no store write. The daemon is not just a write gate — it executes the
  builds. The `nix build` client evaluates the flake to derivations and hands
  them over the socket; the daemon forks builders and registers outputs.
- The daemon socket protocol is internal and unstable, so the Go agent cannot
  speak it directly; a nix CLI must run inside the container world regardless,
  both to evaluate and to execute the stream script (whose shebang needs `/nix`
  at the canonical path).
- Escalation blast radius is the same in every model below: builders run as
  `nixbld*` in the same container as a privileged store-writer, so a
  nixbld-to-root escalation inside that container yields store write.

## Build execution model (undecided)

Three candidate shapes, all sharing one host `/nix` volume:

- **A. Standalone nix per build container** (the original sketch). One-shot
  container runs `nix build` directly. As container-root with
  `build-users-group` set, nix forks builders as `nixbld*` even without a
  daemon, preserving the split; run as a single non-root uid it has no
  poisoning protection at all. Even the correct form leaves the evaluator —
  which consumes fully attacker-controlled flake code — running as the
  privileged store-writer, and every concurrent build is an independent
  privileged writer on the shared store.
- **B. Long-lived daemon container.** One container per node runs `nix-daemon`
  as container-root owning the `/nix` mount; the agent uses containerd exec to
  run the client and the stream script as an unprivileged user inside the same
  container. Single store writer, GC safe at any time, cancellation is killing
  the exec (daemon aborts on client disconnect), and no per-build container
  lifecycle — exec with captured stdio replaces the one-shot `cio.WithStreams`
  path. Costs: all builds plus the daemon share one cgroup and one netns, so no
  per-build resource limits or egress policy, and mounts are fixed at container
  start, so the whole worktrees directory is readable by every build.
- **C. Per-build container running the full multi-user split.** Each build
  container starts its own daemon (or root nix with `build-users-group`) plus
  an unprivileged client. Combines the poisoning guarantee and unprivileged
  evaluator with per-build cgroups, per-build egress netns, and mounting only
  that build's checkout — a build cannot read other repos' checkouts, which B
  allows. Residual: N concurrent privileged writers on one store. The routine
  locking is kernel file locks and sqlite over a shared bind mount — one
  kernel, so this is the supported multi-writer topology. The sharp edge is GC:
  temproots are keyed by pid and liveness-checked in the collector's own pid
  namespace, so GC run from inside any build container can conclude another
  container's in-flight roots are dead and delete paths under a live build.
  Mitigation: never run GC from build containers; the agent runs it as a
  maintenance pass when no builds are in flight, which matches the intended
  OpenDeploy-owned size-bounded policy.

Leaning: C dominates B on isolation at the cost of the centralized-GC rule and
a per-build container start (hundreds of milliseconds against multi-minute
builds); B's durable advantages are single-writer simplicity and less
container plumbing. A in its naive form is the poisoning regression and in its
correct form is C minus the unprivileged evaluator. No decision yet.

## Image export and layer caching (undecided; preferred variant identified)

The current import path pays full cost on every cache-miss build: the
`streamLayeredImage` script tars every layer every time (it cannot know what
the destination has), and although containerd dedupes blob writes, a
docker-archive over a pipe is a sequential full-content format, so the
importer must read through every byte regardless. On a new commit where the
multi-GB base closure is unchanged, generation + pipe + ingest still run in
full (~30s observed); only the unpack step is incremental via snapshot
chainIDs. Skipping unchanged layers requires a digest-negotiated transfer, not
a pipe.

Variants considered:

- **Preferred: agent-side ingest via the nix2container Go library.** User
  flakes output `nix2container.buildImage` instead of a stream script: a small
  JSON store path listing each layer's store paths with the layer digest and
  size computed at build time; layer tarballs are never materialized. The
  agent (Go) imports `github.com/nlewo/nix2container` directly, reads the
  JSON, checks each digest against the ctrd content store, regenerates
  deterministic tars only for missing layers, writes them to the content
  store, composes manifest/config, tags, and unpacks. An app-code-only change
  ingests one or two small layers. No registry endpoint, no push protocol, no
  auth token, no skopeo, and no network surface reachable from the build
  netns — the build's only output is the JSON path, and the agent reads the
  shared store host-side after the build exits.
- **nix2container + skopeo push to a registry endpoint on the agent.** The
  push half of the OCI distribution API is small and would write into the
  ctrd content store. Rejected as first choice: it adds a listener reachable
  from the build netns (under in-container `sandbox = false` the untrusted
  build script shares that netns with the pusher, so an unauthenticated
  endpoint is a second poisoning channel; mitigation is a per-build ref-scoped
  one-time token — an OpenDeploy-issued result capability, not a violation of
  the no-fetch-credentials decision). Its one durable advantage is doubling as
  the transport for later multi-node image distribution; agent-side ingest
  does not preclude adding this later and shares the JSON+library foundation.
- **Keep `streamLayeredImage`, cache (store-path set → layer digest) mappings
  from its `conf.json` and skip known layers.** Rejected: requires
  re-implementing the script's tar generation bit-for-bit against nixpkgs
  internals; fragile.

Details for the preferred variant:

- The JSON references canonical `/nix/store/...` paths and tar entry names
  must stay canonical, but the host store lives at
  `/var/lib/opendeploy-nix/nix`. Since Nix leaves the host, `/nix` is free:
  bind-mount or symlink the volume there and the agent reads paths as
  written. Alternatively verify whether the library accepts a root offset.
- Ingest regenerates tars from live store paths, so it must complete before
  any GC maintenance pass; this slots into the agent-owned idle-time GC rule
  from the execution model section with no extra machinery.
- Digest-claim trust: the JSON is produced by the untrusted build, which can
  claim any digest. Skipping ingest purely because a claimed digest exists in
  ctrd lets a malicious flake incorporate another deployment's layer into its
  own image and read it at runtime — a cross-deployment content-disclosure
  channel on shared nodes. Mitigations: regenerate and hash even on a hit
  (keeps the ingest saving, pays a local read), amortize with an agent-side
  cache of verified store-path-set → digest mappings, or scope dedup to
  layers previously ingested from the same repo. Cheap, but must not be
  discovered later.
- Contract change: flakes adopt nix2container (a public flake input,
  consistent with the vendored/publicly-fetchable constraint). The preparer
  detects the artifact type — executable script keeps the legacy full-stream
  import, JSON gets the layer-cached ingest.
- Interaction with the execution models: the container-to-host image
  transport disappears entirely, removing the one-shot `cio.WithStreams`
  stdout-capture work from A/C and eroding B's exec-simplicity advantage. The
  variant is otherwise orthogonal to the A/B/C choice and to store poisoning
  (digests are computed from actual content, so a tampered store yields
  different digests). The commit-level image ref check stays the first-line
  cache; layer caching targets the new-commit-small-diff case it misses.

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
  cost of one image-sized write and read. This bullet applies only to the
  legacy stream-script path; the preferred nix2container variant above has no
  container-to-host image transport at all.
- Container startup and pull costs apply only on a cache miss. The containerd
  image ref check short-circuits before any container starts.

## Trade-offs

- Gained: Nix removed from the install and uninstall path, per-node Nix version
  drift removed, egress policy over fixed-output derivations using the existing
  netns and nftables machinery, cgroup limits, and a boundary independent of
  directory permissions.
- Lost: a shared writable `/nix` host directory lets one build poison the cache
  for every deployment on the node. The multi-user daemon prevents this today.
  Preserving the guarantee requires keeping the builder/store-writer privilege
  split inside the container world — see "Build execution model (undecided)".
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

## Resolved: sandbox is enabled in the harness

`sandbox = false` was not load-bearing. Verified 2026-08-24 by flipping the
harness to `sandbox = true` (`testing-vms/test-orchestrator/main.go`) and
running the full e2e suite on freshly recreated VMs with an empty `/nix` store:
**125 cases passed, 0 failed, `RUN_EXIT=0`**, including every Nix build path
(`create-baseline-nix-docker-deployment` at 26s cold, asset-backed,
virtual-network, https-echo, protostream, websocket-echo, and both rollovers).

No `sandbox-paths` entry was needed. The mock-mode test CA — installed to
`/usr/local/share/ca-certificates` and baked into the system bundle, a host
path outside the store — was the predicted blocker and did not materialise:
Nix bind-mounts its configured CA file to the canonical
`/etc/ssl/certs/ca-certificates.crt` inside the chroot for fixed-output
derivations, and the other two consumers (substituter fetches of
`cache.nixos.org`, flake-input fetches of `github.com`) run daemon- and
evaluator-side, outside any sandbox.

The one failure in the first run was unrelated pre-existing drift: commit
9ac15a9 rewrote `ref_usage.go` so referenced-asset deletes return "Asset still
in use: referenced by …" instead of the bare sentinel, without updating
`e2e/cases/space-moves.js`. Fixed in that file as part of this change.

The original open question is kept below for the reasoning trail.

## Open questions

- Why `sandbox = false` was set. Now answered empirically — see above; no
  impure host-state dependency existed. The original reasoning: the likely
  cause is that mock mode resolves
  `github.com`, `api.github.com`, and `cache.nixos.org` through `/etc/hosts`,
  while the Nix sandbox substitutes a minimal `/etc/hosts`. If so the fix is
  `sandbox-paths = /etc/hosts=/etc/hosts` and production can run sandboxed. If
  real builds depend on impure host state, the flag cannot be flipped and the
  container becomes the only route to isolation. Two later findings narrow
  this: for fixed-output derivations the sandbox bind-mounts the host's
  `/etc/resolv.conf`, `/etc/services`, and `/etc/hosts` (only normal
  derivations get the minimal localhost-only file), and the derivations that
  need the mock-mode mappings are exactly the fetchers — so mock mode may pass
  under `sandbox = true` with no config change; verify with a real run. An
  audit of the radkit flakes (2026-08-23) found nothing sandbox-incompatible:
  every network touch is a fixed-output derivation (`fetchPnpmDeps`, uv2nix
  wheels, `vendorHash`) or offline (`gradle --offline` with no external
  dependencies), and substituter fetches happen daemon-side outside the
  sandbox anyway.

## Sequencing

- Security and install simplification are separate arguments. `sandbox = true`
  closes most of the current exposure as a configuration change; the residual
  is fixed-output derivation egress, cgroup limits, and seccomp coverage. Done
  for the harness (see "Resolved" above). It is not a production change, since
  OpenDeploy does not manage `nix.conf` on real nodes — the remaining gap is
  that nothing asserts a node's Nix is sandboxed and multi-user, which is worth
  a startup check or an install-docs statement whichever way the container
  decision goes.
- Independent of the container decision: assert or document the multi-user Nix
  requirement.
- Also independent of the container decision: the nix2container export change.
  Landing it first removes the `cio.WithStreams` transport work from the
  container migration and shrinks that estimate; it delivers the layer-caching
  win on the current host-Nix path immediately.
- Estimated container implementation effort is two days to a working path and
  three to five days to production confidence. Risk concentrates in store
  seeding, build image upgrades, and validation on real Linux nodes. Most of
  `nixdocker.go` is unchanged: image ref derivation, cache check, checkout,
  validation, import, log streaming, and cancellation.
