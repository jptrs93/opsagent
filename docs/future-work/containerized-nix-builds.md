# Containerized Nix builds

Status: direction locked and implemented 2026-09-12. Delivery status,
prototype results and deviations are in
[containerized-nix-builds-implementation-plan.md](containerized-nix-builds-implementation-plan.md);
the shipped code paths are described in `docs/engineering/engine.md`. The
security finding this design closes is OD-01 in the 2026-09-11 security
review.

## Direction

- Nix builds run in a one-shot container started through the bundled
  containerd. The host has no Nix install. The node prerequisite set is
  containerd alone.
- The build image is the official `nixos/nix` image pinned by digest. It runs
  as container root with its built-in `nixbld` users, so `nix` forks every
  derivation builder as an unprivileged uid without a daemon process.
- Every repository has its own Nix store directory on the host, mounted at
  `/nix` in the build container. Builds of one repository run one at a time.
  Different repositories build concurrently with no shared state.
- The flake output is a nix2container image JSON. The agent ingests it into
  containerd with the nix2container Go library, generating only the layers
  containerd does not already hold. No build output is ever executed on the
  host.
- Store poisoning is contained, not prevented. A poisoned store affects only
  later builds of the same repository. Stores are reset on a schedule and on
  operator request.
- There is no compatibility path for `streamLayeredImage` flakes. Every
  deployed repository and every flake under `testexamples/` migrates to
  nix2container before the preparer change ships.

## What changes

Today `nixdocker.Preparer` checks out the repository on the host, runs
`nix build` as a host subprocess, executes the resulting stream script as the
OpenDeploy service process, and pipes its stdout into `ctrd.Client.Import`.
The stream script is whatever the flake author chose, so a repository
contributor runs arbitrary code as the service user, which holds ambient
`CAP_SYS_ADMIN`, the containerd socket, the machine key and the primary
database.

Under this design the preparer checks out the repository as before, starts a
build container with the checkout and the repository's store mounted, waits
for it to exit, reads the JSON it produced, and ingests the image. The build
container has no host secrets, no containerd socket, no capabilities beyond
the container default set, its own cgroup and its own network namespace.

Unchanged: image ref derivation and the commit-level cache check, Git checkout
and authentication on the host, source validation on the primary, prepare log
streaming, and the deployment operator's use of the preparer.

## Build container

### Image

`nixos/nix`, built from `docker.nix` in the Nix repository, pinned by digest
in the agent and pulled through `ctrd.Pull` on first use. It contains `nix`,
bash, coreutils, git, curl, openssh, findutils and the CA bundle. It creates
`nixbld1` through `nixbld32` with uids 30001 to 30032 in group 30000, sets
`build-users-group = nixbld` and `sandbox = false`, and runs as root.

Running as container root is required: `nix` needs `CAP_SETUID` and
`CAP_SETGID` inside the container to fork builders as `nixbld` users. The
container keeps only the runtime's default capability set and default seccomp
profile. Nix's own sandbox stays off because nested user namespaces conflict
with that seccomp profile; the container is the isolation boundary.

Upgrading the image is a digest bump in the agent, or the
`OPENDEPLOY_NIX_BUILD_IMAGE` setting on a node. New stores seed from the new
image. A store seeded from a different image digest is deleted and reseeded
before its next build; the store is a cache, so this costs one cold build.

### Invocation

One container per build, created and started through `ctrd.Client` with a
one-shot variant of `RunTask` that streams stdout and stderr to the prepare
log and returns the exit status. Cancellation kills the task.

| Mount | Container path | Mode |
|---|---|---|
| Repository store `.../stores/<key>/nix` | `/nix` | read-write |
| Repository checkout | `/build/src` | read-only |
| Empty directory over the checkout's `.git` | `/build/src/.git` | read-only |
| Generated `nix.conf` | `/etc/nix/nix.conf` | read-only |
| Per-build scratch directory | `/build/tmp` | read-write |
| CA bundle, when `OPENDEPLOY_NIX_BUILD_CA_BUNDLE` is set | `/etc/ssl/certs/opendeploy-build-ca.crt` | read-only |

The checkout mount is read-only because `repo/git.Manager` reuses one
worktree per repository across builds. The scratch directory backs `TMPDIR`
because builds write large temporary trees and a tmpfs would consume memory.
It is deleted after the build.

Environment: `TMPDIR=/build/tmp`, `HOME=/build/tmp/home`, the image's `PATH`
and `SSL_CERT_FILE`; with a configured CA bundle, `SSL_CERT_FILE`,
`NIX_SSL_CERT_FILE` and `GIT_SSL_CAINFO` point at the mounted bundle. Nothing
from the agent's environment is forwarded. The container receives no
credentials. Flake inputs must be vendored or publicly fetchable; a fetch
that needs authentication fails with a distinct error.

Command: `nix build --no-update-lock-file --no-link --print-out-paths -L
path:/build/src?dir=<flake dir>#<target>` with the flake directory as the
working directory. The `path:` reference keeps `nix` from running Git
against the checkout. The last stdout line is the JSON store path.

Resources: a memory limit, a CPU quota and a pids limit on the container
cgroup. Because builders are children of the container, the limits cover the
derivation builds themselves. Defaults come from the node (three quarters of its
memory with a 512 MiB floor, every CPU, 4096 pids) and are overridden per
node with `OPENDEPLOY_NIX_BUILD_MEMORY_MB`, `OPENDEPLOY_NIX_BUILD_CPUS` and
`OPENDEPLOY_NIX_BUILD_PIDS`. A build killed at the memory limit is reported
as such from the container cgroup's OOM counter.

### Generated `nix.conf`

```
experimental-features = nix-command flakes
sandbox = false
build-users-group = nixbld
substituters = https://cache.nixos.org/
trusted-public-keys = cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=
max-jobs = auto
```

OpenDeploy owns this file. `substituters`, `trusted-public-keys`, `max-jobs`
and `cores` become plain settings later.

### Networking

The build container joins its own network namespace created by the network
manager under the reserved deployment id 16777215 in space 0, with DNS
through netproxy as workload containers have. Its policy is egress only:
outbound to public addresses for substituters and fixed-output fetches, no
inbound, and no reachability of the cluster prefix beyond netproxy's DNS
port, peer node underlay addresses, the host itself, link-local, loopback or
cloud metadata addresses. At most two build attachments exist per node. One
namespace covers normal and fixed-output derivations alike, which Nix's own
sandbox cannot do.

## Stores

### Layout

```
/var/lib/opendeploy-nix/
  etc/nix.conf                             generated once per node
  template/<image-digest>/nix/             seeded once per build image
  stores/<repo-key>/nix/                   one store per repository
  stores/<repo-key>/tmp/<build-id>/        per-build scratch, deleted after the build
  stores/<repo-key>/store.json             seed time and image digest
  stores/<repo-key>/verified-layers.json   layer digests the agent computed
```

`<repo-key>` is the hex SHA-256 of the trimmed repository URL, the same key
the Git manager uses for checkouts. A store holds `store/`, `var/nix/db/` and
`var/nix/profiles/` as a complete Nix root.

### Seeding

A new store is created from the template for the current image digest. Store
paths under `store/` are hardlinked, which is free on one filesystem because
store files are immutable. The database and profiles under `var/` are copied,
never hardlinked, so each store has its own SQLite database. The template is
produced once per image digest by running the image with the whole
`/var/lib/opendeploy-nix` root mounted at `/mnt/opendeploy-nix` and copying
its `/nix` into the template directory; the same single mount is what lets
store creation hardlink across the tree. Both run in a maintenance container
and finish with an atomic rename.

### Serialisation

One build at a time per repository, enforced by a per-repository lock in the
preparer. A node-wide cap bounds concurrent builds across repositories. One
writer per store removes multi-writer locking and garbage-collection
liveness concerns entirely.

### Ownership and maintenance

Store files are created by container root and are root-owned and read-only.
The agent reads them without privilege. Deletion, garbage collection and
store resets run in a one-shot maintenance container with the store mounted,
never as the agent process.

### Size budget

Measured on the e2e nodes after the full suite built all thirteen example
applications with a shared store: 2.8 GB and about 1,900 paths. A fresh
per-repository store measured 2.1 GB and 940 paths after its first
`httpecho` build, which includes compiling the nix2container tool and the Go
toolchain closure.

| Item | Size |
|---|---|
| nixpkgs source checkout, one per pinned revision | 470 MB each |
| stdenv closure | 396 MB |
| Go toolchain closure | 266 MB |
| Python toolchain closure | 226 MB |

A per-repository store lands between 1.2 GB and 1.8 GB depending on the
toolchain. The nixpkgs source tree is the largest single item and is
duplicated per store. A cross-store hardlink pass by content hash recovers
most of that when repositories share a nixpkgs pin. Deferred.

### Garbage collection and reset

The containerd image is the durable artifact and `--no-link` leaves no
garbage-collection roots, so a store is a pure cache and any of it may be
deleted at any time between builds. Three mechanisms, all run under the
repository lock:

- A size cap per store (`OPENDEPLOY_NIX_STORE_SIZE_CAP_MB`, default 6 GiB),
  enforced by `nix store gc` in the maintenance container after a build that
  pushed the store over the cap.
- A scheduled full reset per store (`OPENDEPLOY_NIX_STORE_RESET_HOURS`,
  default seven days), which deletes the store and reseeds it. This bounds
  the lifetime of any poisoned path.
- An operator action, `POST /v1/nix-store/reset` behind cluster update
  authority and the "Reset build store" button in the deployment inspector,
  recorded on the primary and delivered to every node, which reseeds the
  repository's store before its next build.

## Poisoning stance

The build container runs `nix` as the store owner, so any code that executes
inside a build of a repository can write that repository's store. This is
accepted with three bounds:

- Containment: a store is private to one repository. The people who can put
  code into a repository's build already control everything that repository
  deploys, so poisoning gives them nothing outside what they own.
- The builder split: derivation builders run as `nixbld` users, not as the
  store owner. A compromised dependency executing in a build hook cannot
  write the store and cannot persist after it is removed from the lockfile.
  The flake evaluator and `nix` itself run as the store owner; evaluation is
  pure by default and cannot read paths outside the flake.
- Reset: the scheduled full reset and the operator reset return a store to
  the seeded state.

Layer digests in the image JSON do not detect poisoning. They are computed
inside the build from the store as it is, so a poisoned path yields a
consistent digest. They exist for a different purpose, described next.

## Image output

### Flake contract

The selected flake output is a `nix2container.buildImage` derivation. Its
store path is a JSON file describing the image: the OCI image config and a
list of layers, each with its blob digest, size, diff id, media type and the
store paths it contains. Layer tarballs are not written to the store. The
`target` field of `NixDockerBuild` selects the output as today.

```nix
{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  inputs.nix2container.url = "github:nlewo/nix2container";
  inputs.nix2container.inputs.nixpkgs.follows = "nixpkgs";

  outputs = { nixpkgs, nix2container, ... }:
    let
      system = "x86_64-linux";
      pkgs = import nixpkgs { inherit system; };
      n2c = nix2container.packages.${system}.nix2container;
      app = pkgs.buildGoModule { pname = "httpecho"; version = "0.1.0"; src = ./.; vendorHash = null; };
    in {
      packages.${system}.default = n2c.buildImage {
        name = "opendeploy-test/httpecho";
        config.entrypoint = [ "${app}/bin/httpecho" ];
        maxLayers = 16;
      };
    };
}
```

Pinning `nix2container.inputs.nixpkgs.follows` keeps one nixpkgs source tree
per store. The nix2container tool is built from source in each store on first
use; a shared pin across repositories keeps that to one build per store.

### Ingest

The agent reads the JSON from the repository store after the container exits
and imports the image with the nix2container Go library:

1. For each layer, check whether a blob with the declared digest exists in
   the containerd content store.
2. Generate the tar for every layer that is missing, from the store paths
   under the repository store, with entry names rewritten to canonical
   `/nix/store/...` paths, and write it to the content store while hashing it.
3. Compose the OCI config and manifest, write them, tag the image with the
   derived local ref, and unpack it.

Digest claims are not trusted. The JSON is produced by the build, so a hostile
flake can name any digest, including a layer belonging to another
deployment's image. A layer is skipped only when its digest matches an entry
in the agent's own record of previously verified mappings from a sorted store
path set to a digest; otherwise it is regenerated and hashed, and the import
fails on a mismatch. The verified record is node-local and keyed by
repository store.

On a new commit whose base closure is unchanged, only the application layers
are generated. This replaces the current full stream on every cache miss.

### Cache ref

The image ref stays `opendeploy.local/nix-docker-build/<schema>/<sourceHash>:<commit>`.
The schema segment moves from `v1` to `v2` because the artifact contract
changed; `v1` images age out through the existing lazy invalidation.

## Node prerequisites and install

Nix leaves the install: no `nix` binary, no `/nix`, no daemon unit, no
`nix.conf`, and no `/nix` entry in the service unit's `PATH`. The build image
is pulled on first Nix build; a node that cannot reach the registry cannot
build until it can, which is a new failure mode and is reported as a distinct
prepare error. Mirroring the image to a project-owned registry is the
mitigation if pull limits or availability become a problem.

The e2e harness installs no Nix packages on nodes and writes no
`/etc/nix/nix.conf`. Mock mode mirrors `nixos/nix:2.35.2` with the other OCI
images and sets `OPENDEPLOY_NIX_BUILD_IMAGE` to the mirrored reference,
keeps mapping `github.com` and `cache.nixos.org` to the repository mirror
through the node's `/etc/hosts`, which every container receives, and sets
`OPENDEPLOY_NIX_BUILD_CA_BUNDLE` to the node's system bundle, which carries
the harness CA.

## Verification items

Settled during the prototype phase before the preparer was rewritten; the
results are recorded in the implementation plan:

- Layer tars regenerated from a store root other than `/nix`, with entry
  names rewritten to `/nix/store`, are byte-identical to the build's. The
  library's writer fixes the root, so its deterministic writer is ported
  with a root parameter.
- `path:` flake references make lazy Git fetches moot; the checkout's `.git`
  is masked regardless.
- The `nixos/nix` image runs `nix build` with `build-users-group = nixbld`
  under the containerd default seccomp profile without the sandbox.
- nix2container publishes no binary cache; the tool is built once per store.
