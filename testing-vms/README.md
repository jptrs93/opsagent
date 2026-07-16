# OpenDeploy Lima VM E2E Harness

This directory contains the E2E harness. It runs the Playwright flows from `testing-vms/e2e` against a Lima VM cluster.

The harness creates real Ubuntu VMs with Lima:

- `opendeploy-primary` — OpenDeploy primary.
- `opendeploy-secondary` — OpenDeploy worker.
- `opendeploy-secondary-2` — OpenDeploy worker reserved for TLS passthrough coverage.
- `opendeploy-repo-mirror` — long-lived repo mirror VM for `OPD_REMOTE=mock`.

Playwright runs in Docker rather than in a Lima VM.

The primary Web UI is served over HTTPS at `https://primary.opendeploy.test` by default. The harness writes `/etc/hosts` entries inside the VMs and installs a test CA so browser/WebAuthn tests use a real HTTPS origin. The machine-local root CA is stored in the ignored `testing-vms/.test-ca` directory, is reused across runs, and is separate from disposable state under `testing-vms/.tmp`. Override its location with `OPD_VM_CERT_DIR`.

## Requirements

- macOS with Lima installed: `brew install lima`.
- Lima `user-v2` networking, enabled by default in modern Lima releases.
- Apple Silicon or Intel Mac. The harness maps host architecture to matching Linux release binaries.
- Enough disk space for four Ubuntu VMs.
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

- Deletes and recreates the primary/two-secondary VM cluster each run.
- Uses `OPD_REMOTE=mock`, which requires an already-running repo mirror VM serving GitHub-shaped release/git traffic plus mirrored OCI images locally.
- In the default local mock mode, bootstraps primary and worker from a locally
  built `v0.0.0` executable, then upgrades them to a locally built `v1.0.0`
  release served by the mirror.
- Runs `FLOWS=bootstrap-enroll-nixdocker` from the Playwright Docker container.
- Runs Playwright in Docker and writes results to `testing-vms/test-results` and `testing-vms/playwright-report`.

Useful overrides:

```sh
RESET=false bash testing-vms/run.sh
FLOWS=bootstrap-enroll-nixdocker bash testing-vms/run.sh
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

`OPD_REMOTE=real` and `OPD_MOCK_OPENDEPLOY_SOURCE=real` resolve the latest
GitHub release tag unless explicit install and upgrade versions are supplied.

## Mock Remote Mode

`OPD_REMOTE=mock` keeps OpenDeploy release, git, runtime, OCI, Nix binary cache, and Lima base-image traffic pointed at local artifacts or the repo mirror. `repo-mirror-up` copies an untracked host-side artifact cache into the repo mirror VM, then publishes the local checkout as the `jptrs93/opsagent` git repo used by the E2E fixtures.

By default, mock mode builds the current checkout twice: `v0.0.0` for implicit
self-bootstrap and `v1.0.0` as the mirrored upgrade/restore release. Set
`OPD_MOCK_OPENDEPLOY_SOURCE=real` to serve prepared real release binaries from
the artifact cache instead.

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

It also opens `127.0.0.1:18443` to `opendeploy-secondary-2:443` for TLS passthrough assertions. The tests use a client-side resolver override for `*.ingress.opendeploy.test`; each of the three exact SNI routes presents its own CA-signed certificate. Override the local tunnel port with `OPD_PLAYWRIGHT_TLS_INGRESS_PORT`.

Set `OPD_PLAYWRIGHT_BASE_URL` only when providing your own Docker-reachable primary URL. Add extra host mappings with `OPD_PLAYWRIGHT_ADD_HOSTS` if needed.

## Host Browser Access

After the harness has created the primary VM, start an SSH tunnel from the host:

```sh
bash testing-vms/tunnel.sh
```

The script starts the VM if needed and forwards `127.0.0.1:8443` to its HTTPS listener. Keep it running and open `https://primary.opendeploy.test:8443`. Override the local port with `OPD_PLAYWRIGHT_HOST_PORT` or the VM name with `OPD_VM_PRIMARY`.

Add the hostname to the macOS host file once if it is not already present:

```text
127.0.0.1 primary.opendeploy.test
```

After the first E2E run creates the long-lived root CA, trust it once on macOS:

```sh
sudo security add-trusted-cert -d -r trustRoot \
  -k /Library/Keychains/System.keychain \
  testing-vms/.test-ca/ca.crt
```

The leaf server certificate is renewed as needed without replacing the trusted root CA. Delete `testing-vms/.test-ca` only when you intentionally want to rotate the test CA; doing so requires trusting the newly generated root again.

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

This deletes the primary/two-secondary VM harness instances and cluster state. Results, reports, repo mirror, and shared test certificates are left in place. Use `repo-mirror-down` to delete the repo mirror VM explicitly.

## Notes

- The E2E flow accepts both secondary enrollments as UI machines `worker-1` and `worker-2` by their stable enrollment identities.
- Workloads still use OpenDeploy's bundled containerd inside the worker VM, so this avoids Docker-in-Docker and privileged systemd containers while preserving real Linux host semantics.
- Docker Playwright does not need to be on the Lima VM network, but it does need a Docker-reachable URL for the primary Web UI.
