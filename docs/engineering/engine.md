# Deployment engine

## Overview

The engine package (`backend/engine/`) orchestrates deployments through an operator-per-deployment model. Each deployment gets a `DeploymentOperator` that runs a reconciliation loop, reacting to state changes and delegating to a preparer and a runner.

Key files:
- `backend/engine/operator.go` — `DeploymentOperator` reconciliation loop.
- `backend/engine/store.go` — `Store` interface for deployment state reads and writes.
- `backend/engine/preparer/preparer.go` — `Preparer` interface, `PrepareWrapper`, `Prepare` dispatcher, and `PrepareOutputPath`.
- `backend/engine/preparer/nixbuild.go` — `NixBuilder` for cloning, checking out, and running `nix build`.
- `backend/engine/preparer/ghrelease.go` — `GithubReleaseDownloader` for fetching prebuilt release assets.
- `backend/engine/preparer/gitmanager.go` — `GitManagerImpl` for fetching repo info and commit history (used by `NixBuilder`).
- `backend/engine/runner/runner.go` — `Runner` interface, `Create`/`ReAttach` factories, and `RunOutputPath`.
- `backend/engine/runner/osprocess.go` — `OSProcessRunner` with its internal spawn/respawn/backoff loop.
- `backend/engine/runner/osprocess_unix.go` — Unix-specific process spawning (`ForkExec`) and signal handling.
- `backend/engine/runner/systemd.go` — `SystemdRunner` for systemd-managed deployments.

## Deployment data model

The `Deployment` proto message is structured into three nested sections, each owned by a different component:

- **`RunningStatus`** `{pid, version, artifact_path, status, seq_no, number_of_restarts, last_restart_at}` — written by the runner. Tracks the currently running (or most recently run) process.
- **`Preparation`** `{version, artifact_path, status, seq_no}` — written by the preparer. Tracks prepare progress and the resolved executable path.
- **`DesiredState`** `{version, running, updated_at, updated_by, seq_no}` — written by user actions (deploy/stop). `running` is a bool.

The `seq_no` field flows from client → `DesiredState` → `Preparation` → `RunningStatus`. The client must send a `seq_no` greater than the current value. `number_of_restarts` resets to zero when `seq_no` changes (new version deployed).

## Operator

`DeploymentOperator` runs a reconciliation loop that watches for deployment state changes and config updates. It does not mutate deployment state directly — it delegates to the preparer and the runner, which write their own sections.

The operator is deliberately minimal: it decides _which_ artifact should be
running, and creates/replaces/stops the runner at the boundaries. The runner
owns the full crash/respawn/backoff lifecycle for a given artifact — the
operator does not re-create the runner on each crash.

State tracked inside the operator loop:

- `currentRunner` — the live `runner.Runner` (or nil).
- `currentRunnerSeqNo` — the `seq_no` the current runner was created for.
- `currentPrepare` — the in-flight `PrepareWrapper` (or nil).

Decision functions:

- `needsCreatePrepare` — `Preparation.Status == PREPARE_REQUESTED` and no active preparer.
- `needsCancelPrepare` — active preparer's version differs from `DesiredState.Version`.
- `needsStopRunner` — `currentRunner != nil` and `DesiredState.Running` is false.
- `needsReplaceRunner` — `currentRunner != nil`, preparation is `READY`, and `Preparation.SeqNo > currentRunnerSeqNo` (new version ready).
- `needsStartRunner` — `currentRunner == nil`, `DesiredState.Running`, and we have either a READY preparation or a RunningStatus artifact to reuse.

On stop/replace the operator calls `currentRunner.RequestStop()` and waits on
`<-currentRunner.Done()` before creating the next runner — RequestStop is
synchronous (bounded by the OS-process grace period) and guarantees the old
runner has written its terminal state and stopped touching the store.

`resolveStartPrep` determines what artifact to hand to the runner. If the
preparer has produced a newer `seq_no` than what is running, use that;
otherwise fall back to the last-known running artifact (reuse on stop/start
without re-preparing).

## Git manager

`GitManagerImpl` interacts with remote Git repositories via the GitHub API. It uses the GitHub token from `OPSAGENT_GITHUB_TOKEN` for private repo access.

Methods:
- `ListBranches(repoURL)` — lists remote branches via the GitHub API.
- `GetCommitLog(repoURL, branch, limit)` — fetches recent commits via the GitHub API. Defaults to 30 commits.

## Preparers (`backend/engine/preparer/`)

The `Preparer` interface has three methods:

- `Build(ctx, cfg, state)` — creates a `PrepareWrapper`, produces an executable on disk, and writes `Preparation.Status`.
- `ListScopes(ctx, cfg)` — returns top-level scopes a user can pick from (branches for nix; nil for github releases).
- `ListVersions(ctx, cfg, scope)` — returns the list of available versions within that scope (commits for nix; release tags for github releases).

`Prepare` is a small dispatcher held by the handler that routes each call to
the correct implementation based on which variant is set on `DeploymentConfig.Prepare`.

### NixBuilder

`NixBuilder` clones a repo, checks out a specific version, and runs `nix build`. A semaphore limits concurrency to one `nix build` invocation at a time.

Flow:

1. `Build(ctx, cfg, state)` reads `DesiredState.Version`, creates a `PrepareWrapper`, and starts the build in a goroutine.
2. Writes `Preparation.Status = PREPARING` with the `seq_no` from `DesiredState`.
3. Clones or fetches the repo into `{dataDir}/repos/{repo}/`.
4. `git checkout <version>`.
5. Runs `nix build --no-link --print-out-paths -L` in the flake directory (`filepath.Dir(cfg.Prepare.NixBuild.Flake)`).
6. Resolves the executable path from the Nix store output (single executable in `bin/` or the artifact itself).
7. On success: `Preparation.Status = READY`, `artifact_path` set to the resolved executable.
8. On failure: `Preparation.Status = FAILED`.

Prepare output is written to `{PrepareOutputDir}/{key}_{version}.out`.

`ListScopes` returns branches via `git ls-remote --heads`. `ListVersions`
fetches the most recent 25 commits from the given branch via the GitHub API
(reuses `GitManagerImpl`).

### GithubReleaseDownloader

`GithubReleaseDownloader` fetches a prebuilt artifact from a GitHub release.
Flow:

1. Writes `Preparation.Status = DOWNLOADING` with the `seq_no` from `DesiredState`.
2. Fetches `/repos/{owner}/{repo}/releases/tags/{tag}` from the GitHub API, using `OPSAGENT_GITHUB_TOKEN` for auth.
3. Picks the asset by exact name from `cfg.Prepare.GithubRelease.Asset`; if unset, uses the first asset in the release.
4. Downloads to `{dataDir}/releases/{owner}/{repo}/{tag}/{asset}` via atomic rename. Redirects from the GitHub asset API are followed manually so the Authorization header isn't forwarded to the CDN. Existing file with the correct size is skipped.
5. `chmod 0755` on the downloaded file and sets `bw.ArtifactPath` to its full path.
6. On success: `Preparation.Status = READY`. On any failure: `FAILED`.

`ListScopes` returns nil (releases are flat — no branch dimension).
`ListVersions` calls `/repos/{owner}/{repo}/releases?per_page=50`, returning
each release's `tag_name` as the version id, `name` as the label, and
`published_at` as the time.

## Runners (`backend/engine/runner/`)

The `runner` package owns everything to do with keeping a deployment
artifact's process alive. The operator interacts with it only through two
entry points:

- `runner.Create(ctx, store, key, config, prep)` — fresh start for a new
  version. Dispatches to the correct variant based on `config.Runner`;
  missing `Runner` or missing sub-variants default to `osProcess`.
- `runner.ReAttach(ctx, store, key, config, running)` — resume supervision
  of a deployment that was already running before opsagent restarted.

`Runner` is a minimal interface:

```go
type Runner interface {
    RequestStop() // synchronous; blocks until the runner has fully stopped
}
```

Runner goroutines stay alive until `RequestStop` is called. Transient
crashes are written to the store but handled internally: `OSProcessRunner`
runs its own backoff/respawn loop; `SystemdRunner` keeps polling so systemd's
own `Restart=` directive can recover the unit. The operator therefore never
needs to observe runner completion — it calls `RequestStop()` and moves on.

Stale-write guard: every state write checks
`d.RunningStatus.SeqNo == r.seqNo` before mutating. Because `RequestStop` is
synchronous, at most one runner is alive per deployment at any time.

### OSProcessRunner

`OSProcessRunner` owns the full spawn/monitor/respawn/backoff loop for an
OS process:

1. Write `STARTING` with the seqNo and bumped `NumberOfRestarts` (unless
   adopting an existing PID).
2. `syscall.ForkExec` with `Setsid: true` (detached daemon). stdin → `/dev/null`,
   stdout/stderr → `{RunOutputDir}/{key}_{version}.out`.
3. Write `RUNNING` with the PID.
4. Block on `Wait4` until the child exits.
5. If `RequestStop` was called: write `STOPPED` and exit.
6. Otherwise: bump local crash count and `NumberOfRestarts`, write `CRASHED`,
   sleep with exponential backoff (1s → 30s), then respawn (goto 1).

The stability reset still applies: if the process ran ≥ 15 seconds before
crashing, the local crash count is reset so a stable deployment doesn't get
stuck at the max backoff after the occasional crash.

Because backoff is internal to the runner, the operator does _not_ observe
`CRASHED` and respawn; those flips are entirely the runner's concern.

`OPSAGENT_*` environment variables are scrubbed from the spawned process so
secrets (master password hash, GitHub token) don't leak into deployed
artifacts.

#### Reattach

`runner.ReAttach` constructs an `OSProcessRunner` with an `adoptPid`
populated from the persisted `RunningStatus`. The runner's first iteration
polls that PID with `kill(pid, 0)` (Wait4 only works on our own children).
If the polled process is still alive, the runner effectively monitors it;
if/when it exits, the runner falls through to its normal spawn loop and
respawns from the same artifact path.

### SystemdRunner

`SystemdRunner` manages a deployment via a systemd unit. Creation flow:

1. If `cfg.Runner.Systemd.BinPath` already refers to the same file as
   `prep.ArtifactPath` (via `os.SameFile`), skip the install entirely and
   enter monitor mode. The existing `RunningStatus` is left untouched —
   this is the common path for opsagent restarts and avoids a spurious
   `STARTING → RUNNING` flicker.
2. Otherwise: write `STARTING`, copy the artifact to a sibling temp file
   next to `BinPath`, `chmod 0755`, then `os.Rename` it into place. Atomic
   rename avoids `ETXTBSY` on a running binary and lets opsagent-self-update
   work.
3. `systemctl restart <name>`.
4. Enter the monitor loop.

The monitor loop polls `systemctl is-active <name>` every 2 seconds and
maps the result: `active`/`reloading` → `RUNNING`, `activating` → `STARTING`,
`deactivating`/`inactive` → `STOPPED`, `failed` → `CRASHED`. The loop does
*not* exit on terminal states — it keeps polling so that when systemd's own
`Restart=` directive brings the unit back, the next tick picks up the new
`active` and writes `RUNNING` again. The goroutine only exits when
`RequestStop` is called.

`RequestStop` runs `systemctl stop <name>` and writes `STOPPED`. Unlike
`OSProcessRunner`, `SystemdRunner` does not implement its own backoff —
systemd owns process-level restart behavior.

## Backoff

Exponential crash backoff (1s → 30s, doubling per consecutive crash, reset
after a ≥ 15 s stable run) lives inside `OSProcessRunner.run()`. It is not
a separate package, not a decorator on the operator loop, and not applied to
the systemd runner.

## Storage failure policy

Opsagent treats any failure of the on-disk log stores (`backend/storage/logstore`) as an unrecoverable broken state. Outside the auth helpers, all DB calls go through the `Must*` variants on `KeyLogStore` / `TupleLogStore`, which panic on error. The process is expected to run under a supervisor (systemd, launchd, etc.) that will restart it; on startup the in-memory state is rebuilt by replaying the append-only log.

Rules for new code:

- **Writes** — always `Must*`. There is no sensible recovery from a write failure.
- **Reads where the key is an internal invariant** (e.g. tail-loop polling for a deployment we just fetched, the operator re-reading its own state) — use `Must*`. A missing key here is a bug, not a user error.
- **Reads driven by user input** where "not found" is an expected outcome (`PostV1AuthMaster` looking up a user by name, the JWT layer resolving a `kid`) — use the non-`Must*` variant and translate `logstore.ErrNotFound` to an `ApiErr`. These are the only auth-helper exemptions.

The policy is also called out in `backend/main.go` so it's visible at the entry point.
