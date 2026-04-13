# Deployment engine

## Overview

The engine package (`backend/engine/`) orchestrates deployments through an operator-per-deployment model. Each deployment gets a `DeploymentOperator` that runs a reconciliation loop, reacting to state changes and delegating to a preparer and a runner.

Key files:
- `backend/engine/operator.go` — `DeploymentOperator` reconciliation loop.
- `backend/engine/preparer/preparer.go` — `Preparer` interface, `StartPrepare`/`ReAttach` dispatchers.
- `backend/engine/preparer/nixbuild.go` — `NixBuilder` for cloning, checking out, and running `nix build`.
- `backend/engine/preparer/ghrelease.go` — `GithubReleaseDownloader` for fetching prebuilt release assets.
- `backend/engine/preparer/gitmanager.go` — `GitManagerImpl` for fetching repo info and commit history (used by `NixBuilder`).
- `backend/engine/runner/runner.go` — `Runner` interface, `Create`/`ReAttach` factories.
- `backend/engine/runner/osprocess.go` — `osProcessRunner` with its internal spawn/respawn/backoff loop.
- `backend/engine/runner/osprocess_unix.go` — Unix-specific process spawning (`ForkExec`) and signal handling.
- `backend/engine/runner/systemd.go` — `systemdRunner` for systemd-managed deployments.

## Deployment data model

The deployment state is split across two proto messages:

- **`DeploymentConfig`** `{id, config_id, version, updated_at, updated_by, spec, desired_state, deleted}` — the deployment's identity, config, and desired state. `id` is the integer primary key; `config_id` is the human-readable `DeploymentIdentifier{environment, machine, name}`; `version` is a per-deployment monotonically increasing config version number. `desired_state` contains `{version, running}` set by user actions.

- **`DeploymentStatus`** `{status_seq_no, timestamp, deployment_id, preparer, runner}` — the deployment's runtime state, containing:
  - **`PreparerStatus`** `{deployment_config_version, artifact, status}` — written by the preparer. Tracks prepare progress and the resolved executable path.
  - **`RunnerStatus`** `{deployment_config_version, running_pid, running_artifact, status, number_of_restarts, last_restart_at}` — written by the runner. Tracks the currently running (or most recently run) process.

The `deployment_config_version` field flows from `DeploymentConfig.Version` → `PreparerStatus` → `RunnerStatus`. `number_of_restarts` resets to zero when a new version is deployed.

## Operator

`DeploymentOperator` runs a reconciliation loop that watches for deployment state changes and config updates. It does not mutate deployment state directly — it delegates to the preparer and the runner, which write their own sections.

The operator is deliberately minimal: it decides _which_ artifact should be
running, and creates/replaces/stops the runner at the boundaries. The runner
owns the full crash/respawn/backoff lifecycle for a given artifact — the
operator does not re-create the runner on each crash.

State tracked inside the operator loop:

- `currentRunner` — the live `runner.Runner` (or `Stopped()` sentinel).
- `currentPreparer` — the in-flight `Preparer` (or `finishedPreparer`).

Decision logic (in the reconciliation select loop):

- `config.Deleted` — deployment deleted; cancel preparer, stop runner, unsubscribe.
- `!config.DesiredState.Running` — stop runner.
- `config.Version > currentPreparer.Version()` — config ahead of preparer; cancel old and start new prepare.
- `preparerReady(status, config.Version) && config.Version > currentRunner.Version()` — preparer ready with new version; stop old runner, create new one.

On stop/replace the operator calls `currentRunner.Stop()` which blocks until
the runner has fully stopped and written its terminal state.

## Git manager

`GitManagerImpl` interacts with remote Git repositories via the GitHub API. It uses the GitHub token from `OPSAGENT_GITHUB_TOKEN` for private repo access.

Methods:
- `ListBranches(repoURL)` — lists remote branches via the GitHub API.
- `GetCommitLog(repoURL, branch, limit)` — fetches recent commits via the GitHub API. Defaults to 30 commits.

## Preparers (`backend/engine/preparer/`)

The `Preparer` interface:

```go
type Preparer interface {
    Cancel()
    Version() int32
}
```

`StartPrepare` dispatches to the correct implementation based on which variant is set on `DeploymentConfig.Spec.Prepare`. `ReAttach` resumes observation: if the previous run reached READY for the current config version, it returns a no-op handle; otherwise it starts a fresh preparation.

Version discovery methods are on the variant structs directly:
- `ListScopes(ctx, cfg)` — returns top-level scopes a user can pick from (branches for nix; nil for github releases).
- `ListVersions(ctx, cfg, scope)` — returns the list of available versions within that scope (commits for nix; release tags for github releases).

### NixBuilder

`NixBuilder` clones a repo, checks out a specific version, and runs `nix build`. A semaphore limits concurrency to one `nix build` invocation at a time.

Flow:

1. Reads `DeploymentConfig.DesiredState.Version` and starts the build in a goroutine.
2. Writes `PreparerStatus.Status = PREPARING` with the `deployment_config_version` from `DeploymentConfig.Version`.
3. Clones or fetches the repo into `{dataDir}/repos/{repo}/`.
4. `git checkout <version>`.
5. Runs `nix build --no-link --print-out-paths -L` in the flake directory (`filepath.Dir(cfg.Spec.Prepare.NixBuild.Flake)`).
6. Resolves the executable path from the Nix store output (single executable in `bin/` or the artifact itself).
7. On success: `PreparerStatus.Status = READY`, `artifact` set to the resolved executable.
8. On failure: `PreparerStatus.Status = FAILED`.

Prepare output is written to `{PrepareOutputDir}/{deploymentID}_{version}`.

`ListScopes` returns branches via the GitHub API. `ListVersions`
fetches the most recent 25 commits from the given branch via the GitHub API
(reuses `GitManagerImpl`).

### GithubReleaseDownloader

`GithubReleaseDownloader` fetches a prebuilt artifact from a GitHub release.
Flow:

1. Writes `PreparerStatus.Status = DOWNLOADING` with the `deployment_config_version` from `DeploymentConfig.Version`.
2. Fetches `/repos/{owner}/{repo}/releases/tags/{tag}` from the GitHub API, using `OPSAGENT_GITHUB_TOKEN` for auth.
3. Picks the asset by exact name from `cfg.Spec.Prepare.GithubRelease.Asset`; if unset, uses the first asset in the release.
4. Downloads to `{dataDir}/releases/{owner}/{repo}/{tag}/{asset}` via atomic rename. Redirects from the GitHub asset API are followed manually so the Authorization header isn't forwarded to the CDN. Existing file with the correct size is skipped.
5. `chmod 0755` on the downloaded file.
6. On success: `PreparerStatus.Status = READY`. On any failure: `FAILED`.

`ListScopes` returns nil (releases are flat — no branch dimension).
`ListVersions` calls `/repos/{owner}/{repo}/releases?per_page=50`, returning
each release's `tag_name` as the version id, `name` as the label, and
`published_at` as the time.

## Runners (`backend/engine/runner/`)

The `runner` package owns everything to do with keeping a deployment
artifact's process alive. The operator interacts with it only through two
entry points:

- `runner.Create(ctx, store, dep, status)` — fresh start for a new
  version. Dispatches to the correct variant based on `dep.Spec.Runner`;
  missing `Runner` or missing sub-variants default to `osProcess`.
- `runner.ReAttach(ctx, store, dep, prev)` — resume supervision
  of a deployment that was already running before opsagent restarted.

`Runner` is a minimal interface:

```go
type Runner interface {
    Stop()          // synchronous; blocks until the runner has fully stopped
    Version() int32
}
```

Runner goroutines stay alive until `Stop` is called. Transient
crashes are written to the store but handled internally: `osProcessRunner`
runs its own backoff/respawn loop; `systemdRunner` keeps polling so systemd's
own `Restart=` directive can recover the unit.

Stale-write guard: every state write checks
`s.Runner.DeploymentConfigVersion > r.status.DeploymentConfigVersion` before
mutating, discarding updates from superseded runners.

### osProcessRunner

`osProcessRunner` owns the full spawn/monitor/respawn/backoff loop for an
OS process. State is consolidated into a single `apigen.RunnerStatus` struct
field. Cross-goroutine PID visibility uses `atomic.StoreInt32`/`atomic.LoadInt32`
on `r.status.RunningPid`.

Flow:

1. `syscall.ForkExec` with `Setsid: true` (detached daemon). stdin → `/dev/null`,
   stdout/stderr → `{RunOutputDir}/{deploymentID}_{version}`.
2. Write `RUNNING` with the PID.
3. `awaitProcessOrCancel(pid)` — wraps blocking `Wait4` in a goroutine with `ctx.Done()` select.
4. If `Stop()` was called: write `STOPPED` and exit.
5. Otherwise: write `CRASHED`, sleep with exponential backoff (1s → 30s),
   bump `NumberOfRestarts` and `LastRestartAt`, then respawn (goto 1).

The stability reset still applies: if the process ran >= 15 seconds before
crashing, the local crash count is reset so a stable deployment doesn't get
stuck at the max backoff after the occasional crash.

`Stop()` owns the signal logic: sends SIGTERM, waits 3s, then SIGKILL. When
`leavePrevious` strategy is set, `Stop()` skips signals entirely — the app
handles its own rollover.

`OPSAGENT_*` environment variables are scrubbed from the spawned process so
secrets (master password hash, GitHub token) don't leak into deployed
artifacts.

#### Reattach

`runner.ReAttach` constructs an `osProcessRunner` with the PID from the
persisted `RunnerStatus`. The runner's first iteration polls that PID with
`kill(pid, 0)` (Wait4 only works on our own children). If the polled process
is still alive, the runner monitors it; if/when it exits, the runner falls
through to its normal spawn loop and respawns from the same artifact path.
An error count limit (15) prevents infinite polling on persistent errors.

#### leavePrevious strategy

When `strategy: "leavePrevious"` is set in the osProcess runner config,
`Stop()` cancels the context but does not send SIGTERM/SIGKILL to the old
process. This is for apps with built-in rollover behavior that kill the
previous process on their own port once the new version is ready.

### systemdRunner

`systemdRunner` manages a deployment via a systemd unit. Creation flow:

1. Write `STARTING`, symlink the artifact to `BinPath` via atomic rename.
2. `systemctl restart <name>`.
3. Enter the monitor loop.

The monitor loop polls `systemctl is-active <name>` every 2 seconds and
maps the result: `active`/`reloading` → `RUNNING`, `activating` → `STARTING`,
`deactivating`/`inactive` → `STOPPED`, `failed` → `CRASHED`. The loop does
*not* exit on terminal states — it keeps polling so that when systemd's own
`Restart=` directive brings the unit back, the next tick picks up the new
`active` and writes `RUNNING` again. The goroutine only exits when
`Stop` is called.

`Stop` cancels the monitor goroutine. It does NOT stop the systemd unit.
Unlike `osProcessRunner`, `systemdRunner` does not implement its own backoff —
systemd owns process-level restart behavior.

## Backoff

Exponential crash backoff (1s → 30s, doubling per consecutive crash, reset
after a >= 15 s stable run) lives inside `osProcessRunner.run()`. It is not
a separate package, not a decorator on the operator loop, and not applied to
the systemd runner.

## Storage failure policy

All DB calls go through `Must*` variants which panic on error. The process is expected to run under a supervisor (systemd, launchd, etc.) that will restart it; on startup the in-memory state is rebuilt from the database.

Rules for new code:

- **Writes** — always `Must*`. There is no sensible recovery from a write failure.
- **Reads where the key is an internal invariant** — use `Must*`. A missing key here is a bug, not a user error.
- **Reads driven by user input** where "not found" is an expected outcome — use the non-`Must*` variant and translate the error to an `ApiErr`.
