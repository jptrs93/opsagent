# Deployments

## Overview

Opsagent manages deployment configurations and deployment lifecycles. Users
create each deployment individually with a per-deployment YAML spec. The
system fetches available versions (git commits for nix builds, tag names
for github releases) on demand, prepares artifacts (builds or downloads),
and supervises running processes with automatic crash recovery.

## Deployment config

Each deployment has two explicit steps:

- **`prepare`** — produces an executable on disk. Pick exactly one variant.
- **`runner`** — runs the executable. Optional; defaults to `osProcess`.

A deployment is created from a single YAML document posted to
`POST /v1/deployment/create`:

```yaml
name: coflip_server
environment: PROD
machine: 192.168.1.100
prepare:
  nixBuild:
    repo: github.com/org/repo
    flake: nix/server/flake.nix
    outputExecutable: coflip_server
runner:
  osProcess:
    workingDir: /var/lib/coflip
    runAs: coflip
```

The spec of an existing deployment is updated by posting YAML in the
`yaml_content` field of `POST /v1/deployment/update`; the `name`,
`environment`, and `machine` identity fields are fixed at create time and
cannot be changed through this path.

### Prepare variants

| Variant | Fields | Description |
|---|---|---|
| `nixBuild` | `repo`, `flake`, `outputExecutable` | Clones the repo, checks out the desired version, runs `nix build`, and resolves the executable from the result. If `outputExecutable` is set, it selects that binary from `bin/`; otherwise it requires exactly one executable output. |
| `githubRelease` | `repo`, `asset`, `tag` | Fetches the given release from GitHub (using `OPSAGENT_GITHUB_TOKEN` for private repos) and downloads the named asset (or the first asset if unset) to `{dataDir}/releases/{owner}/{repo}/{tag}/{asset}`. |

### Runner variants

| Variant | Fields | Description |
|---|---|---|
| `osProcess` *(default)* | `workingDir`, `runAs`, `strategy` | Spawns the artifact as a detached daemon via `fork/exec` with `setsid`. The runner monitors the process directly and restarts it on crashes with exponential backoff. Used when no `runner` block is set. `strategy: "leavePrevious"` skips terminating the old process on upgrade for apps with built-in rollover. |
| `systemd` | `name`, `binPath` | Installs the artifact into `binPath` via atomic symlink and runs `systemctl restart <name>`. Polls `systemctl is-active` for lifecycle state. Systemd owns process-level restarts. |

### Config versioning

Each deployment's `DeploymentConfig.Version` is a per-deployment
monotonically increasing integer that bumps on any spec or desired-state
change. Every bump is persisted to `deployment_config_history` so the UI
can reconstruct the sequence of changes.

## Deployment state

Each deployment's runtime state is structured into sections owned by different components:

### DesiredState

Set by user actions (deploy or stop). Contains the target `version` (commit hash or release tag) and a `running` boolean. Audit fields (`updated_at`, `updated_by`) and the config `version` are on the parent `DeploymentConfig`, not on `DesiredState` itself.

### PreparerStatus

Driven by the preparer. Tracks prepare progress with status values:
`PREPARING`, `DOWNLOADING`, `READY`, `FAILED`. On success, contains the
resolved `artifact` (executable path) and the `deployment_config_version`
from `DeploymentConfig.Version`.

### RunnerStatus

Driven by the runner. Tracks the running process with `running_pid`,
`running_artifact`, `status` (`NO_DEPLOYMENT`, `RUNNING`, `STOPPED`, `STARTING`,
`CRASHED`), `deployment_config_version`, `number_of_restarts`, and `last_restart_at`.

## Deployment identification

Each deployment has an integer `id` (primary key) assigned when the deployment is first created via `POST /v1/deployment/create`. The `DeploymentIdentifier{environment, machine, name}` tuple is human-readable metadata stored on `DeploymentConfig.ConfigID`. All API requests, storage keys, and log file paths use the integer `id`.

## Deployment status display

The status page shows one card per deployment, sorted with
OPSAGENT_SYSTEM last, then by environment, name, machine, and id. Each
card carries a per-environment tinted background and displays:

- Deployment name with history link
- Status badge (Running/Stopped/Starting/Crashed/No Deployment) — clickable to view run output
- Stop/Start buttons
- Two-column info panel: deployment info (deployed by, deployed at, version) and runtime info (restart count, last restart time)
- Prepare status with link to prepare output (build log for nix, download log for github release)
- "Update" button that opens an overlay for version selection and optional per-deployment YAML edits

## Deploy workflow

1. The user clicks "Update" on a card. The overlay fetches available
   versions via `POST /v1/deployment/versions` — 25 most recent commits
   per scope for nix (scopes are branches), all releases for github release.
2. The user picks a version (and optionally edits the YAML spec) and submits.
3. The frontend calls `POST /v1/deployment/update` with the target version
   and, if the spec was edited, the new `yaml_content`.
4. The backend writes the new spec (if any), sets `DesiredState`
   (version, running=true), and bumps `DeploymentConfig.Version`.
5. The operator's reconciliation loop picks up the change and starts a
   preparer.
6. The preparer clones/fetches or downloads, resolves the executable, and
   writes `PreparerStatus.Status = READY`.
7. The operator creates a runner, which writes `RunnerStatus.Status =
   STARTING` then `RUNNING` with the PID.

## Crash recovery

The `osProcess` runner owns crash recovery directly: on process exit it
writes `RunnerStatus.Status = CRASHED`, sleeps for an exponentially
increasing delay (1s → 60s, doubling per consecutive crash), and respawns
the same artifact. `number_of_restarts` increments on each respawn and
resets on new deployments. If the process runs stably for 15+ seconds before
crashing, the local crash counter is reset — preventing permanent escalation
from occasional crashes.

The `systemd` runner leaves crash recovery to systemd itself. Opsagent just
polls `systemctl is-active` and writes the observed state.

## Deployment history

The history sidebar shows a chronological log of all deployment config and status changes. Config entries show the version number and what changed (version deployed, running toggled, deleted). Status entries show preparer and runner state transitions (diff-rendered against the previous entry so unchanged sections aren't repeated). All entries are fetched via `POST /v1/deployment/history` with the integer deployment ID. History is stored in `deployment_config_history` (PK `deployment_id, version`) and `deployment_status_history` (PK `deployment_id, status_seq_no`) — the composite primary keys already cover `deployment_id`-leading lookups.

## Empty state

When no deployments exist, the status page displays "No deployments configured. Create a deployment config first." Clicking "Add deployment" opens an overlay with a per-deployment YAML template.
