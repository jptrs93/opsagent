# Deployment event log: migration and refactoring plan

Status as of 2026-09-01: **built, rolled out, and stripped**. The three
deployment tables (`deployments`, `deployment_spec_versions`,
`deployment_space_versions`) are replaced by the single append-only
`deployment_event_log` as the first application of a generalised versioning
pattern for the global state tree. The v0.0.549/550 rollout migrated every
cluster; the follow-up release dropped the old tables and the orphaned
placement-pin column and removed the one-time migration and the column-rename
migrations. Remaining work is in "Follow-ups unlocked by the log".

## Concept

`version(state_path)` is a derived deterministic function over the global state
tree: the version of any branch equals the number of times that branch's value
has changed across all `global_seq` numbers. `version(deployments[i])` is the
deployment's top-level version; `version(deployments[i].spec)` is its spec
version. Stored version columns are an efficient materialisation of this
function for the sub-parts that have isolated operations and CAS guards on
them — an implementation detail, not a separate concept.

Each object is versioned at the top level (every event bumps it) and sub-parts
with their own operations are explicitly version-tracked. Name will become a
versioned sub-part of the deployment regardless of table design, so its
version column is included now to avoid a later backfill. Scheduling gets no
sub-part: scheduling config will simply be part of the deployment spec.

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
    name_version             INTEGER NOT NULL,
    value                    BLOB NOT NULL,     -- full Deployment snapshot
    event_type               INTEGER NOT NULL DEFAULT 0,  -- AuthzVerb value: 1 create / 2 update / 3 delete
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

Follow-up (2026-09-01): `POST /v2/deployments/update` replaced the separate
update and move-space endpoints with a single request guarded by the
top-level version (`expected_version`), carrying exactly one update kind
(`version_only_update`, `running_only_update`, `spec_update`,
`assigned_space_update`); the store write is the version-CAS'd
`UpdateDeployment`.

## Built (2026-09-01)

- Table + index exactly per the target schema; the old tables served as the
  one-time migration source and were dropped after the rollout.
- Semantics as pinned down in step 2 of the original plan:
  - Delete is its own operation (`Service.DeleteDeployment`, request field
    `DeploymentDeleteRequest.version`), guarded by the top-level version. It
    bumps only the top-level version with `event_type = delete`; the spec is
    left untouched (no more forced workload-stopped tombstone append), so
    spec version is strictly "times the spec changed". The scheduler already
    terminates on `Deleted` alone.
  - `Deployment.version` (proto field 13) exposes the top-level version.
  - Event types are the `AuthzVerb` enum values (`DeploymentEventCreate` /
    `Update` / `Delete` alias `AUTHZ_VERB_CREATE`/`UPDATE`/`DELETE`), so the
    stored `event_type` is the verb that authorized the write.
  - The blob keeps its version fields (columns duplicated); write paths
    assert under `s.Mu` that the top-level version bumps by exactly one and
    each sub-version increments iff its sub-value changed
    (`buildDeploymentEvent`). Write paths base the next state on a fresh
    decode of the latest event, not the cache, so aliased cache mutations
    cannot corrupt CAS or no-op decisions.
  - Spec change detection is structural (`deploymentSpecsEqual`), never a
    byte comparison: map fields (env vars) encode in Go map iteration order,
    so two encodes of an equal spec can differ. The systemic fix — sorted,
    canonical map encoding in cleanproto — is worth doing eventually; the
    stored blobs themselves are unaffected (only comparisons were).
- Writes are one INSERT per mutation (create allocates
  `max(deployment_id)+1`; secret/config rotation writes its batch of events
  in the value's tx under one shared `global_seq`).
- Reads: boot load and refresh via latest-event-per-deployment; history is
  the per-deployment event range (full snapshots — the history endpoint now
  returns space moves and deletes too, and the sidebar keys on the top-level
  version); pinned spec fetch via the `(deployment_id, spec_version)` index
  with current identity overlaid; `CountDeploymentsForSpace` moved to the
  in-memory cache; `DeleteScheduledInstanceStatusesForNode` takes its
  except-list from the cache instead of joining the old tables.
- Scheduled instances snapshot `space_id` at scheduling time
  (`scheduled_instances.space_id`); the old `deployment_space_version_id`
  row-id pin was dropped after the rollout.
- One-time startup migration (removed after the rollout): ran only while the
  event log was empty and old rows existed; interleaved spec and space
  versions per deployment by `(global_seq, created_at)`, folded the create's
  spec-v1 + space-v1 pair into one create event, re-marked a tombstone's
  final event as the delete, started `name_version` at 1, and backfilled
  instance `space_id` in the same tx. Verified against a production DB
  snapshot (20 deployments, 1204 spec + 22 space versions → 1206 events)
  before rollout, as was the post-rollout strip (table/column drops with the
  event log and cache intact).

## Next steps

### Follow-ups unlocked by the log

- Name becomes a real sub-part with its own operation (rename support). Its
  version column already exists and is carried forward on every event.
  Scheduling config, when it arrives, lands inside the deployment spec.
- Undelete as an explicit event (version continuity across delete/restore is
  already preserved by the log; the FE currently only seeds a new deployment
  from a tombstone).
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
