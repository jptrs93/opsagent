# Repo Mirror

VM-native test fixture started with `test-orchestrator repo-mirror-up` and used by `testing-vms/run.sh` when `OPD_REMOTE=mock`.

It serves:

- Git smart HTTP for mirrored test repositories.
- A GitHub-shaped releases API and release asset downloads.
- OpenDeploy, containerd, and runc release artifacts.
- A local TLS OCI registry populated with the container images used by E2E.
- A self-warming `cache.nixos.org` proxy/cache for Nix binary cache paths.

The harness maps `github.com`, `api.github.com`, `cache.nixos.org`, and the registry host to the repo mirror VM inside `/etc/hosts`, so OpenDeploy uses production URLs while tests avoid remote artifact downloads during the run once caches are warm.

The Go app is built by the host orchestrator and copied into the VM as `/usr/local/bin/repo-mirror`.

Commands:

- `repo-mirror prepare` seeds `/srv/git`, `/srv/releases`, and the local OCI registry from `/srv/mock-artifacts`.
- `repo-mirror serve` runs the GitHub-shaped HTTPS API, release download, proxy, and Git smart HTTP server.
- `repo-mirror serve` also handles `Host: cache.nixos.org`, serving cached Nix binary cache files from `/srv/nix-cache` and proxying misses when enabled.

`repo-mirror prepare` fails closed for missing release/runtime artifacts and OCI images unless `OPD_REPO_MIRROR_REFRESH=true` is set. That refresh mode is the explicit online seeding path; normal mock runs use assets already copied from the host cache into `/srv/mock-artifacts`.

Nix cache mode is controlled by `OPD_NIX_CACHE_MODE`:

- `proxy-cache` (default) serves local files and fetches/stores misses from real `https://cache.nixos.org`, logging every miss as a warning.
- `strict` serves local files only and returns 404 for misses.
- `off` disables proxying and returns 404 for cache paths.
