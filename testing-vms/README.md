# OpenDeploy Lima VM E2E Harness

This directory contains the E2E harness. It runs the Playwright flows from `testing-vms/e2e` against a Lima VM cluster.

The harness creates real Ubuntu VMs with Lima:

- `opendeploy-primary` — OpenDeploy primary.
- `opendeploy-secondary` — OpenDeploy worker.
- `opendeploy-repo-mirror` — long-lived repo mirror VM for `OPD_REMOTE=mock`.

Playwright runs in Docker rather than in a Lima VM.

The primary Web UI is served over HTTPS at `https://primary.opendeploy.test` by default. The harness writes `/etc/hosts` entries inside the VMs and installs a test CA so browser/WebAuthn tests use a real HTTPS origin.

## Requirements

- macOS with Lima installed: `brew install lima`.
- Lima `user-v2` networking, enabled by default in modern Lima releases.
- Apple Silicon or Intel Mac. The harness maps host architecture to matching Linux release binaries.
- Enough disk space for three Ubuntu VMs.
- Docker for the Playwright runner.
- `pnpm` and Go on the host when using the default `OPD_REMOTE=mock`, because mock OpenDeploy release binaries are built from the local checkout.

## Run

```sh
bash testing-vms/run.sh
```

For the default `OPD_REMOTE=mock` mode, start the long-lived repo mirror first:

```sh
go run ./testing-vms/test-orchestrator repo-mirror-up
```

Defaults:

- Deletes and recreates the primary/secondary VM cluster each run.
- Uses `OPD_REMOTE=mock`, which requires an already-running repo mirror VM serving GitHub-shaped release/git traffic plus mirrored OCI images locally.
- Installs OpenDeploy release `v0.0.258` and upgrades to `v0.0.258` by default.
- Runs `FLOWS=bootstrap-enroll-nixdocker` from the Playwright Docker container.
- Runs Playwright in Docker and writes results to `testing-vms/test-results` and `testing-vms/playwright-report`.

Useful overrides:

```sh
RESET=false bash testing-vms/run.sh
FLOWS=bootstrap-enroll-nixdocker bash testing-vms/run.sh
OPD_INSTALL_VERSION=v0.0.258 bash testing-vms/run.sh
OPD_REMOTE=real bash testing-vms/run.sh
OPENDEPLOY_GITHUB_TOKEN=... bash testing-vms/run.sh
```

Repo mirror lifecycle commands:

```sh
go run ./testing-vms/test-orchestrator repo-mirror-up
go run ./testing-vms/test-orchestrator repo-mirror-status
go run ./testing-vms/test-orchestrator repo-mirror-down
```

The main run fails early in `OPD_REMOTE=mock` if the repo mirror is not healthy. Normal cleanup and reset leave the repo mirror and shared test certificates intact.

## Mock Remote Mode

`OPD_REMOTE=mock` keeps OpenDeploy release, git, runtime, OCI, Nix binary cache, and Lima base-image traffic pointed at local artifacts or the repo mirror. `repo-mirror-up` copies an untracked host-side artifact cache into the repo mirror VM, then publishes the local checkout as the `jptrs93/opsagent` git repo used by the E2E fixtures.

By default, mock mode publishes locally built OpenDeploy binaries under the configured release tags (`OPD_MOCK_OPENDEPLOY_SOURCE=local`) so the VM HTTPS harness can test unreleased installer behavior. Set `OPD_MOCK_OPENDEPLOY_SOURCE=real` to serve the prepared real release binaries from the artifact cache instead.

Mock runs check the untracked artifact cache and prepare missing files automatically. To refresh the cache explicitly:

```sh
bash testing-vms/prepare_mock_artifacts.sh
```

You can also force `repo-mirror-up` to refresh the cache before starting the fixture:

```sh
OPD_PREPARE_MOCK_ARTIFACTS=true go run ./testing-vms/test-orchestrator repo-mirror-up
```

After preparation, `repo-mirror-up` fails closed if required release binaries, runtime archives, OCI archives, or Lima images are still missing from `testing-vms/.mock-artifacts`.

By default, unknown GitHub paths return 404 except Nixpkgs paths needed by Nix flake evaluation. Set `OPD_REPO_MIRROR_PROXY_UNKNOWN=false` to fail closed for every unknown GitHub path, or `OPD_REPO_MIRROR_PROXY_UNKNOWN=true` to proxy all unknown GitHub paths.

The repo mirror also handles `cache.nixos.org` for worker Nix builds. `OPD_NIX_CACHE_MODE=proxy-cache` is the default: cached paths are served locally and misses are fetched from real `https://cache.nixos.org`, stored under `/srv/nix-cache`, and logged as warnings. Set `OPD_NIX_CACHE_MODE=strict` to fail on misses, or `OPD_NIX_CACHE_MODE=off` to disable the Nix cache proxy.

## Docker Playwright

The harness uses `mcr.microsoft.com/playwright:v1.57.0-noble` by default. Override it with `OPD_PLAYWRIGHT_DOCKER_IMAGE`.

By default the orchestrator opens a host SSH tunnel from `127.0.0.1:8443` to the primary VM's `:443`. The Playwright container then starts a local TCP proxy for `primary.opendeploy.test:443`, so the browser origin remains `https://primary.opendeploy.test` for WebAuthn. Override the host tunnel port with `OPD_PLAYWRIGHT_HOST_PORT`.

Set `OPD_PLAYWRIGHT_BASE_URL` only when providing your own Docker-reachable primary URL. Add extra host mappings with `OPD_PLAYWRIGHT_ADD_HOSTS` if needed.

## Local Checkout Mode

To test local backend/frontend changes without publishing a release:

```sh
OPD_LOCAL_CHECKOUT=true bash testing-vms/run.sh
```

This builds the local checkout as `v0.0.0`, publishes it into the repo mirror as the latest OpenDeploy release, and installs nodes with `opendeploy install --use-self`.

Mock mode maps `github.com`, `api.github.com`, `cache.nixos.org`, and the local OCI registry host to the repo mirror VM through `/etc/hosts`. Real mode leaves those names untouched and does not require the repo mirror.

## Backup Restore Extension

```sh
OPD_BACKUP_RESTORE=true bash testing-vms/run.sh
```

The Playwright flow writes restore settings into `testing-vms/test-results/backup-restore.env`. The VM backup-restore extension then destroys only the primary VM, recreates it, and runs `opendeploy install primary --restore-backup ...` against the MinIO deployment on the worker VM.

## Cleanup

```sh
bash testing-vms/cleanup.sh
```

This deletes the primary/secondary VM harness instances and cluster state. Results, reports, repo mirror, and shared test certificates are left in place. Use `repo-mirror-down` to delete the repo mirror VM explicitly.

## Notes

- The E2E flow accepts the secondary enrollment as UI machine `worker-1`.
- Workloads still use OpenDeploy's bundled containerd inside the worker VM, so this avoids Docker-in-Docker and privileged systemd containers while preserving real Linux host semantics.
- Docker Playwright does not need to be on the Lima VM network, but it does need a Docker-reachable URL for the primary Web UI.
