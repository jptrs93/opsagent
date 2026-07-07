# OpenDeploy Lima VM E2E Harness

This directory contains the VM-based E2E harness. It runs the Playwright flows from `testing-vms/e2e` against a Lima VM cluster.

The harness creates real Ubuntu VMs with Lima:

- `opendeploy-primary` — OpenDeploy primary.
- `opendeploy-secondary` — OpenDeploy worker.
- `opendeploy-playwright` — Playwright browser/test runner.
- `opendeploy-repo-mirror` — repo mirror VM; active in `OPD_REMOTE=mock`, idle in `OPD_REMOTE=real`.

The primary Web UI is served over HTTPS at `https://primary.opendeploy.test` by default. The harness writes `/etc/hosts` entries inside the VMs and installs a test CA so browser/WebAuthn tests use a real HTTPS origin.

## Requirements

- macOS with Lima installed: `brew install lima`.
- Lima `user-v2` networking, enabled by default in modern Lima releases.
- Apple Silicon or Intel Mac. The harness maps host architecture to matching Linux release binaries.
- Enough disk space for four Ubuntu VMs.
- `pnpm` and Go on the host when using the default `OPD_REMOTE=mock`, because mock OpenDeploy release binaries are built from the local checkout.

## Run

```sh
bash testing-vms/run.sh
```

Defaults:

- Deletes and recreates the VM cluster each run.
- Uses `OPD_REMOTE=mock`, which starts the repo mirror VM and serves GitHub-shaped release/git traffic plus mirrored OCI images locally.
- Installs OpenDeploy release `v0.0.256` and upgrades to `v0.0.256` by default.
- Runs `FLOWS=bootstrap-enroll-nixdocker` from the Playwright VM.
- Copies Playwright results to `testing-vms/test-results` and `testing-vms/playwright-report`.

Useful overrides:

```sh
RESET=false bash testing-vms/run.sh
FLOWS=bootstrap-enroll-nixdocker bash testing-vms/run.sh
OPD_INSTALL_VERSION=v0.0.256 bash testing-vms/run.sh
OPD_REMOTE=real bash testing-vms/run.sh
OPENDEPLOY_GITHUB_TOKEN=... bash testing-vms/run.sh
```

## Mock Remote Mode

`OPD_REMOTE=mock` keeps OpenDeploy release, git, runtime, OCI, and Lima base-image traffic pointed at local artifacts. The harness copies an untracked host-side artifact cache into the repo mirror VM, then publishes the local checkout as the `jptrs93/opsagent` git repo used by the E2E fixtures.

By default, mock mode publishes locally built OpenDeploy binaries under the configured release tags (`OPD_MOCK_OPENDEPLOY_SOURCE=local`) so the VM HTTPS harness can test unreleased installer behavior. Set `OPD_MOCK_OPENDEPLOY_SOURCE=real` to serve the prepared real release binaries from the artifact cache instead.

Prepare or refresh the untracked artifact cache explicitly before a mock run:

```sh
bash testing-vms/prepare_mock_artifacts.sh
```

You can also ask `run.sh` to do this preparatory network step first:

```sh
OPD_PREPARE_MOCK_ARTIFACTS=true bash testing-vms/run.sh
```

After preparation, `OPD_REMOTE=mock bash testing-vms/run.sh` fails closed if required release binaries, runtime archives, OCI archives, or Lima images are missing from `testing-vms/.mock-artifacts`.

By default, unknown GitHub paths return 404 except Nixpkgs paths needed by Nix flake evaluation. Set `OPD_REPO_MIRROR_PROXY_UNKNOWN=false` to fail closed for every unknown GitHub path, or `OPD_REPO_MIRROR_PROXY_UNKNOWN=true` to proxy all unknown GitHub paths.

## Local Checkout Mode

To test local backend/frontend changes without publishing a release:

```sh
OPD_LOCAL_CHECKOUT=true bash testing-vms/run.sh
```

This builds the local checkout as `v0.0.0`, publishes it into the repo mirror as the latest OpenDeploy release, and installs nodes with `opendeploy install --use-self`.

Mock mode maps `github.com`, `api.github.com`, and the local OCI registry host to the repo mirror VM through `/etc/hosts`. Real mode leaves those names untouched while still creating the repo-mirror VM so the topology stays fixed.

## Backup Restore Extension

```sh
OPD_BACKUP_RESTORE=true bash testing-vms/run.sh
```

The Playwright flow writes restore settings into `testing-vms/test-results/backup-restore.env`. The VM backup-restore extension then destroys only the primary VM, recreates it, and runs `opendeploy install primary --restore-backup ...` against the MinIO deployment on the worker VM.

## Cleanup

```sh
bash testing-vms/cleanup.sh
```

This deletes the known VM harness instances and `testing-vms/.tmp`. Results and reports are left in place.

## Notes

- The E2E flow accepts the secondary enrollment as UI machine `worker-1`.
- Workloads still use OpenDeploy's bundled containerd inside the worker VM, so this avoids Docker-in-Docker and privileged systemd containers while preserving real Linux host semantics.
- If you want host browser access to the VM network, use Lima's `limactl tunnel`; the automated Playwright path does not need host access.
