# Deployments

## Overview

Opsagent manages deployment configurations and deployment lifecycles. Users
define environments and deployments in a YAML config. The system tracks
config versions, fetches available versions (git commits for nix builds, tag
names for github releases) from remote repositories, prepares artifacts
(builds or downloads), and supervises running processes with automatic crash
recovery.

## Deployment config

Each deployment has two explicit steps:

- **`prepare`** — produces an executable on disk. Pick exactly one variant.
- **`runner`** — runs the executable. Optional; defaults to `osProcess`.

```yaml
environments:
  - name: PROD
    machine: 192.168.1.100
    deployments:
      - name: jnotesapp
        prepare:
          nixBuild:
            repo: github.com/jptrs93/jnotes
            flake: nix/jnotesapp/flake.nix
      - name: coflip_server
        prepare:
          nixBuild:
            repo: github.com/org/repo
            flake: nix/server/flake.nix
        runner:
          osProcess:
            workingDir: /var/lib/coflip
            runAs: coflip
      - name: opsagent
        prepare:
          githubRelease:
            repo: github.com/jptrs93/opsagent
        runner:
          systemd:
            name: opsagent
            binPath: /var/lib/opsagent/bin/opsagent
```

### Prepare variants

| Variant | Fields | Description |
|---|---|---|
| `nixBuild` | `repo`, `flake` | Clones the repo, checks out the desired version, runs `nix build`, resolves a single executable from the result. |
| `githubRelease` | `repo`, `asset`, `tag` | Fetches the given release from GitHub (using `OPSAGENT_GITHUB_TOKEN` for private repos) and downloads the named asset (or the first asset if unset) to `{dataDir}/releases/{owner}/{repo}/{tag}/{asset}`. |

### Runner variants

| Variant | Fields | Description |
|---|---|---|
| `osProcess` *(default)* | `workingDir`, `runAs` | Spawns the artifact as a detached daemon via `fork/exec` with `setsid`. The runner monitors the process directly and restarts it on crashes with exponential backoff. Used when no `runner` block is set. |
| `systemd` | `name`, `binPath` | Installs the artifact into `binPath` via atomic rename and runs `systemctl restart <name>`. Polls `systemctl is-active` for lifecycle state. On opsagent restart or crash recovery, if `binPath` already matches the prepared artifact, opsagent skips the install and just monitors — systemd owns process-level restarts. |

### Config versioning

Each save creates a new `ConfigVersion` with an auto-incrementing version number, timestamp, and the parsed config alongside the raw YAML. Versions are stored via the log store.

### Config history

All versions are retrievable via `GET /v1/config/history`. The frontend displays them in a right sidebar. Clicking a version loads its YAML into the editor.

## Deployment state

Each deployment's runtime state is structured into three sections, each owned
by a different component:

### DesiredState

Set by user actions (deploy or stop). Contains the target `version` (commit
hash or release tag), a `running` boolean, `updated_at`/`updated_by` for
audit, and a `seq_no` that must strictly increase with each user action.

### Preparation

Driven by the preparer. Tracks prepare progress with status values:
`PREPARE_REQUESTED`, `PREPARING`, `DOWNLOADING`, `READY`, `FAILED`. On
success, contains the resolved `artifact_path` (executable) and inherits
`seq_no` from DesiredState.

### RunningStatus

Driven by the runner. Tracks the running process with `pid`, `version`,
`artifact_path`, `status` (`NO_DEPLOYMENT`, `RUNNING`, `STOPPED`, `STARTING`,
`CRASHED`), `seq_no`, `number_of_restarts`, and `last_restart_at`.

## Deployment status display

The status page shows one card per deployment, grouped by environment. Each
card displays:

- Deployment name with history link
- Status badge (Running/Stopped/Starting/Crashed/No Deployment) — clickable to view run output
- Stop/Start buttons
- Two-column info panel: deployment info (deployed by, deployed at, version) and runtime info (restart count, last restart time)
- Prepare status with link to prepare output (build log for nix, download log for github release)
- Scope selector (branches for nix; hidden for github release) and version dropdown for deploying

## Deploy workflow

1. The status page loads scopes (branches for nix) via `POST /v1/list/scopes` and defaults to `main`.
2. Versions for the selected scope load via `POST /v1/list/versions` (25 most recent for nix; all releases for github release).
3. The user selects a version and clicks "Deploy". The request includes a `seq_no` (current + 1).
4. The backend validates `seq_no > current`, sets `DesiredState` (version, running=true), and `Preparation.Status = PREPARE_REQUESTED`.
5. The operator's reconciliation loop picks up the change and starts a preparer.
6. The preparer clones/fetches or downloads, resolves the executable, and writes `Preparation.Status = READY`.
7. The operator creates a runner, which writes `RunningStatus = STARTING` then `RUNNING` with the PID.

## Crash recovery

The `osProcess` runner owns crash recovery directly: on process exit it
writes `RunningStatus.Status = CRASHED`, sleeps for an exponentially
increasing delay (1s → 30s, doubling per consecutive crash), and respawns
the same artifact. `number_of_restarts` increments on each respawn and
resets on new deployments. If the process runs stably for 15+ seconds before
crashing, the local crash counter is reset — preventing permanent escalation
from occasional crashes.

The `systemd` runner leaves crash recovery to systemd itself. Opsagent just
polls `systemctl is-active` and writes the observed state.

## Deployment history

The history sidebar shows a chronological log of all deployment state
changes. Each entry shows the timestamp and what changed. Entries are
color-coded:
- **Green** — process became Running and stayed running (>30s) or is the latest entry
- **Red** — process crashed
- **Orange** — user-triggered action (deploy, stop)
- **Gray** — system transitions (starting, prepare status changes)

## Empty state

When no config exists, the status page displays "No deployments configured. Create a deployment config first." The config page opens with a default YAML template.
