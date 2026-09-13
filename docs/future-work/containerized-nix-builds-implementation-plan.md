# Containerized Nix builds implementation plan

Status: all four phases landed 2026-09-12. The design is in
[containerized-nix-builds.md](containerized-nix-builds.md) and the shipped
flow is documented in `docs/engineering/engine.md`. This document keeps the
prototype results, what each phase delivered, where the implementation
deviates from the plan, and the open items.

## Phase 0 results

All four checks passed on an e2e node before the preparer was touched.

1. **Layer regeneration with a non-canonical root.** A throwaway probe
   regenerated every layer of a nix2container image from a store copied to
   `/var/lib/opendeploy-nix/proto/nix`, with entry names rewritten to
   `/nix/store`, and every digest matched the JSON. The library's tar writer
   hard-codes the `/nix/store` root, so the deterministic writer was ported
   (Apache-2.0) into `backend/lib/engine/prepare/nix2container` with a store
   root parameter rather than calling the library.
2. **Build users under containerd.** The pinned `nixos/nix` image built under
   the containerd default seccomp profile with `build-users-group = nixbld`
   and the sandbox off; the derivation builders ran as uids 30001 and 30010.
3. **Partial clone materialisation.** Moot: builds address the checkout as a
   `path:` flake reference, so `nix` never runs Git against it. The
   checkout's `.git` entry is masked with an empty directory anyway; a
   gitfile also works with `path:` references.
4. **nix2container tool cache.** The project publishes no binary cache. The
   tool is built from source in each store on first use, which the shared
   `nixpkgs.follows` pin keeps to one build per store. The template does not
   pre-seed it.

Measured after the first `httpecho` build in a fresh store: 2.1 GB and 940
paths, above the 1.2 to 1.8 GB the design estimated, because the first build
also compiles the nix2container tool and pulls the Go toolchain closure.

## What landed

### Phase 1: nix2container ingest

- Every flake under `testexamples/` builds a `nix2container.buildImage`
  with `nix2container.inputs.nixpkgs.follows = "nixpkgs"`; the lock files
  pin one nix2container revision and one nixpkgs revision.
- `backend/lib/engine/prepare/nix2container`: image description parsing and
  validation, the ported deterministic layer writer with a store root
  parameter, ingest into the containerd content store through
  `ctrd.ContentSession` with a digester on every regenerated layer, the
  per-store verified record `verified-layers.json`, manifest tagging and
  unpack.
- `nixdocker`: `imageCacheSchemaVersion` is `v2`; the executable-stream path
  and its errors are gone.

### Phase 2: build container

- `ainit`: `NixStoresDir = <data dir>-nix` created 0755 at startup, plus the
  `OPENDEPLOY_NIX_BUILD_IMAGE`, `OPENDEPLOY_NIX_BUILD_CA_BUNDLE`,
  `OPENDEPLOY_NIX_BUILD_MEMORY_MB`, `OPENDEPLOY_NIX_BUILD_CPUS` and
  `OPENDEPLOY_NIX_BUILD_PIDS` settings.
- `backend/lib/engine/prepare/nixstore`: repository keys shared with the Git
  manager (`repogit.RepoKey`), per-repository build slots, image pull and
  template seeding per digest, store creation from the template (`cp -al`
  for `store/`, `cp -a` for `var/`, atomic rename), the generated `nix.conf`,
  per-build scratch directories, the build container specification, and
  maintenance runs in a container with the whole stores root mounted at
  `/mnt/opendeploy-nix`.
- `backend/lib/engine/ctrd`: `RunBuild` (create, start, stream stdout and
  stderr to caller writers, wait, read the cgroup's `oom_kill` counter,
  delete), `Resources` on `ContainerSpec` applied as OCI memory, CPU quota
  and pids limits, `DefaultSeccomp`, `NoNetwork`, and the content session,
  `TagManifest` and `EnsureImage` helpers.
- `backend/lib/network`: build attachments under the reserved deployment
  id 16777215 in space 0 with veths `od16777215s<slot>`, the `build` sets,
  the `build_egress` chains jumped from `forward` ahead of dispatch (DNS to
  netproxy only inside the cluster prefix; the cluster `/48`, `fe80::/10`,
  peer underlays, `169.254.0.0/16` and `127.0.0.0/8` dropped), new `input`
  base chains that drop build traffic to the host except ICMPv6, peer
  underlay addresses fed from the topology, and a cap of two attachments per
  node. `netaudit` parses and compares the new chains and set.
- `nixdocker.Preparer` rewritten around the store manager, the build
  container and the ingest; distinct errors for image pull failure, memory
  limit, credentials, build exit status, non-image output, layer
  verification and import failure.
- Installer: `/nix/var/nix/profiles/default/bin` removed from the unit
  `PATH`; `/var/lib/opendeploy-nix` purged on uninstall.
- Harness: nodes install no Nix packages, write no `nix.conf` and enable no
  daemon; `nixos/nix:2.35.2` is mirrored with the other OCI images and mock
  mode writes `OPENDEPLOY_NIX_BUILD_IMAGE`, `OPENDEPLOY_NIX_BUILD_CA_BUNDLE`
  (the node's system bundle, which carries the harness CA) and
  `OPENDEPLOY_NIX_BUILD_MEMORY_MB=2048` into `/etc/opendeploy/env`.
- `testexamples/hostilebuild`: a derivation that records what a builder can
  reach, a `notimage` output and a `membomb` output.

### Phase 3: store lifecycle

- Size cap (`OPENDEPLOY_NIX_STORE_SIZE_CAP_MB`, default 6 GiB) enforced with
  `nix store gc --max-freed` in a maintenance container after a build that pushed
  the store over it.
- Scheduled reset (`OPENDEPLOY_NIX_STORE_RESET_HOURS`, default 168) and
  operator resets applied by a ten-minute maintenance loop on every node,
  under the repository's build slot.
- Operator reset: `POST /v1/nix-store/reset` with cluster update authority,
  persisted in the primary's `nix_store_resets` table, applied to the
  primary's own stores, fanned out as `MsgToSecondary.nix_store_resets` at the
  session head and on change, and exposed as a "Reset build store" button in
  the deployment inspector for Nix sources.
- Store size and path count, seed time and collection are written to the
  prepare log.

### Phase 4: cleanup and documentation

`docs/engineering/engine.md`, `docs/product/deployments.md`, the
`NixDockerBuild.target` comment, the design document and this document.

## Deviations from the plan

- The image is addressed as `path:/build/src?dir=<flake dir>#<target>`
  instead of `.#<target>` in the flake directory, so `nix` never invokes Git.
- The nix2container tar writer is ported rather than imported; the library
  cannot rewrite the store root.
- A store seeded from an older image digest is deleted and reseeded on its
  next build rather than receiving the new `nix` closure additively; the
  additive copy is not worth its own maintenance path when a reseed costs a
  few seconds.
- Template seeding and store maintenance mount the whole
  `/var/lib/opendeploy-nix` root in the maintenance container so `cp -al`
  hardlinks stay on one mount.
- The node-wide cap is two concurrent builds, enforced by the network
  manager's build attachment slots rather than a separate semaphore.
- The container keeps the runtime default capability set and seccomp
  profile; `no_new_privileges` is not set, matching the configuration the
  prototype validated.
- The checkout's `.git` entry is masked in the container even though `path:`
  references make it unnecessary.
- Mock mode maps `github.com` and `cache.nixos.org` to the mirror through
  the node's `/etc/hosts`, which every container receives, and points the
  build at the node's system CA bundle instead of a separate test bundle.
- The Phase 1 "only application layers regenerated" e2e case is not
  automated: the mirror serves fixed commits, so the suite cannot commit an
  application-only change. Layer reuse is covered by the unit tests of the
  verified record and observed in the prepare log ("layer i/n reused").
- The Phase 2 "two repositories build concurrently" case is not automated:
  every e2e flake lives in one repository, so there is one store per node.

## Node settings

| Setting | Default |
|---|---|
| `OPENDEPLOY_NIX_BUILD_IMAGE` | pinned `docker.io/nixos/nix@sha256:7a007c76…` |
| `OPENDEPLOY_NIX_BUILD_CA_BUNDLE` | unset (image bundle) |
| `OPENDEPLOY_NIX_BUILD_MEMORY_MB` | three quarters of `MemTotal`, at least 512 MiB |
| `OPENDEPLOY_NIX_BUILD_CPUS` | every CPU |
| `OPENDEPLOY_NIX_BUILD_PIDS` | 4096 |
| `OPENDEPLOY_NIX_STORE_SIZE_CAP_MB` | 6144 |
| `OPENDEPLOY_NIX_STORE_RESET_HOURS` | 168 |

## e2e coverage

`cases/nix-build-container.js` in the main flow: the cold-start prepare log
(image pull, template seed, store creation, container run, import), the
hostile build (builder uid in the `nixbld` range, empty effective capability
set, no agent state, environment file, machine key or containerd socket, no
write to an existing store path, netproxy present as DNS but unreachable on
TCP, metadata and host loopback unreachable, a public HTTPS host reachable),
the non-image output error, the memory limit error, and the operator reset
followed by a reseed on the next build. The rest of the suite exercises the
container build path for every example flake with Nix absent from the nodes.

The full suite passed from a wiped harness on 2026-09-12 (137 flow cases,
WireGuard transport checks and network policy kernel checks, 27 minutes) with
the nodes running containerd 2.3.5 and runc 1.5.1, the versions the installer
now stages.

## Open items

- Exposing the node settings above as cluster settings.
- Cross-store deduplication of the nixpkgs source tree when repositories
  share a pin.
- A project-owned mirror of the build image, decided when a pull failure is
  observed rather than in advance.
- A repository-level view for store resets; today the button lives on any
  deployment built from the repository.
