# Deployment event log: migration and refactoring plan

Status as of 2026-09-01. Replaces the three deployment tables (`deployments`,
`deployment_spec_versions`, `deployment_space_versions`) with a single
append-only event log, as the first application of a generalised versioning
pattern for the global state tree.

## Concept

`version(state_path)` is a derived deterministic function over the global state
tree: the version of any branch equals the number of times that branch's value
has changed across all `global_seq` numbers. `version(deployments[i])` is the
deployment's top-level version; `version(deployments[i].spec)` is its spec
version. Stored version columns are an efficient materialisation of this
function for the sub-parts that have isolated operations and CAS guards on
them — an implementation detail, not a separate concept.

Each object is versioned at the top level (every event bumps it) and sub-parts
with their own operations are explicitly version-tracked. Scheduling and name
will become versioned sub-parts of the deployment regardless of table design;
they are included in the target schema now to avoid a later backfill.

## Target schema

```sql
CREATE TABLE IF NOT EXISTS deployment_event_log (
    id                       INTEGER PRIMARY KEY AUTOINCREMENT,
    global_seq               INTEGER NOT NULL,
    created_at               INTEGER NOT NULL,  -- epoch ms
    author                   INTEGER NOT NULL,
    deployment_id            INTEGER NOT NULL CHECK (deployment_id BETWEEN 1 AND 16777215),
    version                  INTEGER NOT NULL,  -- top-level: bumps on every event
    spec_version             INTEGER NOT NULL,
    space_assignment_version INTEGER NOT NULL,
    scheduling_version       INTEGER NOT NULL,
    name_version             INTEGER NOT NULL,
    value                    BLOB NOT NULL,     -- full Deployment snapshot
    event_type               INTEGER NOT NULL DEFAULT 0,  -- create / modify / delete
    UNIQUE (deployment_id, version)
);

CREATE INDEX IF NOT EXISTS idx_deployment_event_log_spec_version
    ON deployment_event_log (deployment_id, spec_version);
```

- `UNIQUE (deployment_id, version)` is the CAS backstop: every event bumps the
  top-level version, so racing writers collide on it, which transitively backs
  every sub-part CAS. Not optional.
- `(deployment_id, spec_version)` serves the pinned-spec fetch: any event with
  a matching spec_version carries the correct spec bytes in its snapshot.
- `value` is the full `Deployment` snapshot, not the changed sub-part. This is
  what makes point-in-time reconstruction O(1) (latest event at or before a
  seq is the full state, no replay) and deletes the current 3-table boot join.
- `AUTOINCREMENT` stays: no-rowid-reuse is the right property for a log.

## Cost analysis (settled)

Measured spec blobs: 17 B minimal, ~290 B for a loaded realistic spec, 1–2 KB
pathological. Per-event on-disk cost ≈ 375 B (row + indexes + typical
snapshot). Overhead vs the normalised layout ≈ 1 / (fraction of bytes that
actually changed): ~+20% at today's spec-dominated event mix, ~2× at 50/50,
~5× if tiny events dominate. Realistic multi-year volume is 10k–100k events
(4–40 MB) — a non-issue. The one discipline required: keep high-frequency
machine-driven state (instance state transitions, status reports) OUT of this
log, in the scheduled-instance tables where it lives today. This log records
intent changes only.

## Completed groundwork

- Primary/secondary protocol naming (`MsgToSecondary`/`MsgToPrimary`,
  `EnrollmentSecondaryMsg`, certu and enrollment surfaces). Shipped.
- `deployment_versions` → `deployment_spec_versions` table rename. Shipped and
  rolled out; the startup migration has been removed again.
- Spec-version naming accuracy pass: every reference to
  `deployment_spec_versions.version` is now named spec version end to end —
  proto fields (`Deployment.spec_version`,
  `ScheduledInstance.deployment_spec_version`, preparer/runner/issued-TLS/log
  query/prepare-output/run-report fields, `DeploymentSpecVersionRef`), DB
  columns (`scheduled_instances.deployment_spec_version`,
  `preparer_spec_version`/`runner_spec_version` on both primary and secondary
  status tables), Go (`Deployment.SpecVersion`, `ExpectedSpecVersion`,
  `Handle.SpecVersion()`, `Runner.SpecVersion()`), frontend, and docs. The
  workload/source version family (`target_version`, `ContainerSpec.version`,
  `DeploymentVersions*`, `running_version`) is deliberately untouched — it
  names deployable artifact versions, not state versions.

## Next steps

### 1. Ship and strip the column renames

Commit and deploy the spec-version naming wave everywhere. The
`RENAME COLUMN` migrations live in both `migrations.sql` files (primary:
`scheduled_instances.deployment_version`, `preparer_config_version`,
`runner_config_version`; secondary: the two status columns). Once every
cluster has rolled forward, delete the migration statements, mirroring the
handling of the table rename.

Side effect to remember: `IssuedTLSValue`'s persisted JSON tag changed
(`config_version` → `spec_version`), so each secondary's encrypted issued-TLS
cache entry invalidates once on upgrade and refetches from the primary.

### 2. Pin down event-log semantics before building

- Delete events bump the top-level version only, with `event_type = delete`;
  sub-versions unchanged. This replaces today's tombstone spec-version append
  (the CAS guard moves from spec version to top-level version), making the
  spec version strictly "times the spec changed".
- Define version continuity across undelete (today `deleted_at` is reset in
  place and history of the deletion is lost; the event log keeps it).
- Columns are the queryable index; the blob is truth. Decide whether to strip
  the version fields from the stored `Deployment` blob or accept the
  duplication with a write-time consistency assertion. Either way, add a
  cheap write-time check (under `s.Mu`, against the previous cached state)
  that each sub-version increments iff its sub-value changed — the schema can
  no longer enforce this structurally.

### 3. Build the event log

- New table + store write path: one INSERT per mutation replaces the current
  multi-table transactions (create currently writes 3 rows across 3 tables).
- Reads: boot load and single-deployment refresh become latest-event-per-
  deployment via the unique index; history is a per-deployment range scan
  (change detection by comparing sub-version columns to the previous row in
  Go); pinned spec fetch via the spec_version index. No shredded value
  columns are needed:
  - `CountDeploymentsForSpace` moves to the in-memory cache like every other
    filter (the SQL query is legacy).
  - Scheduled instances repoint `deployment_space_version_id` (a physical row
    id into a table that will no longer exist) to the
    `(deployment_id, space_assignment_version)` pair, resolving space in Go —
    or snapshot `space_id` into the instance row at scheduling time.
- One-time migration from the three tables, deterministic via `global_seq`:
  interleave spec and space versions per deployment; fold the create's
  spec-v1 + space-v1 (same seq) into one create event; synthesise the delete
  event from `deleted_at`; `scheduling_version`/`name_version` start at 1;
  top-level version is the running event count. Run once at startup off the
  old tables, then drop them after fleet rollout (same ship-then-strip cycle).

### 4. Follow-ups unlocked by the log

- History UI: the sidebar currently shows only spec versions (space moves are
  invisible in it). Switch the history endpoint from spec-version rows to
  event rows ordered by top-level version, with sub-part changes derivable
  per row.
- Scheduling and name become real sub-parts with their own operations
  (rename support; scheduling config moves out of implicit placement logic).
- Point-in-time reconstruction at any `global_seq` for backup/restore and
  debugging.
- Extend the same event-log pattern to other entities as they need it.

## Deferred / optional

- `ClusterNetMap` → `ClusterNetConfig`, `NetState` → `LocalNetState` renames.
- `DeploymentVersions`/`DeploymentVersionsRequest` (deployable source
  versions) rename to something like `DeploymentSourceVersions` to fully
  clear the term "deployment version" for the top-level version.
- Docs prose sweep: many docs still say "worker" in prose; code and identifier
  references are updated but a full terminology pass is outstanding.
- Log record schema: the log store's per-record `version` field (parquet/WAL)
  is the spec version but keeps its name — persisted format, renaming is not
  worth a format change. Revisit only if the format changes for other reasons.
