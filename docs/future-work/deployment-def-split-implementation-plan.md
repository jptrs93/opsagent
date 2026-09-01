# Deployment / DeploymentDef split: implementation plan

Status: complete. Phase 1 shipped in v0.0.553 (envelope+def split, column-backed rows, bridge fields live); phase 2 landed after the v0.0.553 rollout reached every cluster: the bridge flat fields are deleted with their numbers reserved, the def→flat mirror and flat→def fold are gone, and the v0.0.553 migrations were stripped per the migrations.sql convention. The optional clean blob re-encode/renumber was not done — `DeploymentDef` keeps its frozen numbers (2/8/10/11) so historical event rows keep decoding, which costs nothing.

Split `Deployment` into an event-log envelope and the caller-owned definition, so the
in-memory and wire `Deployment` becomes exactly "a `deployment_event_log` row
with the stored def blob deserialised". This makes the two-layer ownership already
enforced at runtime (`CreateDeploymentLocked` overwrites derived fields; the
validators treat `(existing, updated)` as the unit) a property of the types,
and removes the current double-encoding: today every envelope field is stored
both as a row column and again inside the `value` blob, with
`buildDeploymentEvent`'s assertion block existing only to keep the two copies
in sync, and `deploymentEventToProto` trusting the blob and ignoring the
columns entirely.

## Settled decisions

1. **No `deleted` flag anywhere.** Deletion is `event_type = 3` on the latest
   event. The frontend and all other consumers switch on the event type;
   shapes do not drift apart without clear reason.
2. **Phase 1 does not re-encode stored blobs.** `DeploymentDef` keeps the
   field numbers those fields have in today's `Deployment`; the dropped
   envelope numbers are marked `reserved`. Old rows decode because the
   generated decoders skip unknown fields (`default: SkipFieldValue` —
   verified in `apigen/encode.gen.go`). A later phase can do a clean
   re-encode migration and renumber.
3. **The sweep is accepted.** The nested shape propagates to Go, the
   frontend, and the user-docs data model: `cfg.Spec` → `cfg.Def.Spec` etc.
4. **Two explicit denormalised times**: `created_time` (the time of the
   object's first event, copied onto every row so age is displayable without
   searching for the first event) and `event_time` (the time of this event,
   today's `created_at` column, the old `UpdatedAt`).

## Target shapes

```proto
message DeploymentDef {
  // Field numbers are load-bearing: they are the numbers these fields have
  // in today's stored Deployment blobs. Old deployment_event_log rows decode
  // as DeploymentDef with the envelope fields skipped as unknowns.
  reserved 1, 3, 4, 5, 6, 7, 9, 12, 13; // old envelope + deleted numbers
  int32 node_id = 2;
  DeploymentSpec spec = 8 [(cp.go_value) = true];
  int32 space_id = 10;
  string name = 11;
}

enum DeploymentEventType {
  DEPLOYMENT_EVENT_TYPE_UNSPECIFIED = 0;
  DEPLOYMENT_EVENT_TYPE_CREATE = 1;  // must match the stored event_type ints
  DEPLOYMENT_EVENT_TYPE_UPDATE = 2;
  DEPLOYMENT_EVENT_TYPE_DELETE = 3;
}

message Deployment {
  // Envelope = deployment_event_log columns. See the compatibility section
  // before touching numbering: legacy flat fields must survive phase 1.
  int32 id = 1;
  int32 version = 13;
  int32 spec_version = 7;
  int32 space_version = 12;
  int32 name_version = 14;   // new on the model; the column already exists
  int32 author = 6;
  DeploymentEventType event_type = 15;
  int64 created_time = 16 [(cp.go_type) = "time.Time", (cp.js_type) = "Date"];
  int64 event_time = 17 [(cp.go_type) = "time.Time", (cp.js_type) = "Date"];
  DeploymentDef def = 18 [(cp.go_value) = true];

  // Phase-1 bridge fields, mirrored from def on every encode and folded
  // back into def on decode when def is absent. Deleted in phase 2.
  int32 node_id = 2;
  int64 legacy_created_at = 4;
  int64 legacy_updated_at = 5;
  DeploymentSpec spec = 8 [(cp.go_value) = true];
  int32 space_id = 10;
  string name = 11;
  reserved 3, 9; // 9 was `deleted` — dies now, not bridged (see below)
}
```

```sql
-- deployment_event_log target
ALTER TABLE deployment_event_log RENAME COLUMN created_at TO event_time;
ALTER TABLE deployment_event_log ADD COLUMN created_time INTEGER NOT NULL DEFAULT 0;
UPDATE deployment_event_log SET created_time = (
    SELECT e2.event_time FROM deployment_event_log e2
    WHERE e2.deployment_id = deployment_event_log.deployment_id AND e2.version = 1
) WHERE created_time = 0;
```

`migrations.sql` statements re-run on every startup and must stay idempotent:
the rename tolerates "no such column" on re-run, the add tolerates "duplicate
column", the backfill is guarded by `created_time = 0` (real event times are
epoch-ms, never 0). The backfill relies on the invariant that every
deployment has a `version = 1` create row, which `buildDeploymentEvent`
enforces. `schema_deployments.sql` is updated to the new shape for fresh
installs (schema applies before migrations; both paths converge). No
semicolons in migration comments — statements are split on them.

## The compatibility constraint (read first)

`Deployment` is not only an API/response shape. It is embedded by value in
`ScheduledInstanceState.config` (`model/scheduled_instances.proto`), and that
message:

- crosses the primary↔worker cluster stream (assignment pushes) and the
  enrollment accept (`EnrollmentAccepted.node_deployment` /
  `node_net_deployment`);
- is **durably persisted on every worker**
  (`UpsertLocalScheduledInstanceCache` stores `state.Encode()`), and read
  back on worker boot *before* the cluster session connects, so the engine
  can manage workloads while offline.

Naively moving `spec`/`name`/`node_id`/`space_id` into a new `def` field
breaks two real paths:

1. **Old worker ← new primary.** The primary upgrades first and then pushes
   the worker self-upgrade — and that push itself contains a `Deployment`.
   If the old worker's decoder finds no field 8 spec, prepare fails and the
   auto-upgrade stalls on every worker.
2. **New worker binary ← its own old local cache.** After upgrading, the
   worker reads back assignment blobs written by the old binary; a def-only
   decoder sees an empty spec for every running workload.

Hence the phase-1 bridge: the `Deployment` message keeps the legacy flat
fields at their old numbers *alongside* the new envelope+def fields.
Discipline, to prevent the mirror becoming drift:

- One constructor in the state package (`deploymentFromRow` or equivalent)
  assembles every `Deployment` from row columns + decoded `DeploymentDef`,
  and mirrors def → flat fields there and nowhere else. All code reads the
  def; nothing reads the flat fields.
- One decode wrapper: if `def` is zero after decode but legacy flat fields
  are populated (old cache, old peer), synthesize `def` from them. Workers
  use this wrapper for the local cache and stream messages.
- Phase 2 (a release after every cluster and worker has rolled forward,
  matching the migrations.sql strip pattern): delete the flat fields, mark
  their numbers reserved, delete the mirror and the fallback.

`deleted` (field 9) is *not* bridged: assignments are only pushed for live
deployments, so no worker path reads it, and the FE upgrades in lockstep with
the primary. It is reserved immediately.

## Storage changes

- **Columns become authoritative.** `deploymentEventToProto` is replaced by
  row assembly: envelope from `pq.DeploymentEvent` columns (including
  `name_version`, which finally surfaces on the model), def from
  `DecodeDeploymentDef(e.Value)`. New rows store `DeploymentDef.Encode()`
  only.
- **`buildDeploymentEvent` derives instead of asserting.** With callers no
  longer supplying envelope fields, the version-follows / sub-version
  assertion block disappears; the store computes `version`, `spec_version`,
  `space_assignment_version`, `name_version` from the previous cached event
  and the (def, event_type) it is writing. `created_time` is copied from the
  previous row (`= event_time` on create).
- **Write paths.** `CreateDeploymentLocked(ctx, def *apigen.DeploymentDef)`;
  `DeploymentUpdate` unchanged in spirit ({Spec, SpaceID});
  `DeleteDeploymentLocked` writes the previous def unchanged with
  `event_type = 3` — the tombstone must keep carrying the full spec (the
  recently-deleted fork flow reads it).
- **Deletion checks.** Every `cfg.Deleted` becomes an event-type comparison.
  Sites (production): `LiveState` consumers and validators, `FetchDeploymentSnapshot`,
  `ListActiveDeployments`, dup-identity scans, `ref_usage.go`, scheduler,
  `acmeissue`, `network_policies`, `cluster.go`, `global_state.go`,
  `statetest`. Compiler-driven once the field is gone.
- **`pinnedSpecEventToProto`** simplifies: envelope comes from the row;
  the base-overlay keeps supplying *current* identity (node/space/name/
  created_time) over the historical def exactly as today.

## Sweep inventory

- Backend: `cfg.Name/NodeID/SpaceID/Spec` → `cfg.Def.*` everywhere
  (scheduler, engine operator/preparers, webuihandler, state package, authz
  where it touches deployments, netmap builder). `storage.DeploymentKeyMatches`
  moves to def fields (or takes the def). `CreatedAt`/`UpdatedAt` →
  `CreatedTime`/`EventTime`.
- API contract: regenerate via `bash api-contract/proto_generate.sh`, then
  `go generate ./...`.
- Frontend: regenerated `capi/model.js`; hand-sweep of `deleted`,
  `created_at`/`updated_at`, and flat-field access (~10 files:
  deploymentMerge, deployments state, form/HCL/history/editor widgets,
  accessEditors, capi). History views improve: each history row now carries
  its own `event_type`.
- `user-docs/data-model/model.js` regenerated.

## Careful bits

1. **Worker wire + on-disk compat** — the whole section above. Do not skip
   the bridge; the self-upgrade push is the first thing that breaks.
2. **`DeploymentDef` numbers are frozen** at 2/8/10/11 with the rest
   reserved until the phase-2 re-encode. Any "tidy renumber" in phase 1
   silently corrupts every existing event row (fields skipped or misread).
3. **One-way door on the DB.** After the first new-format event row (def blob
   without envelope fields) and the column rename, an old binary cannot read
   the log (it trusts the blob for everything). Rollback = DB restore. Land
   the whole change in one release.
4. **`event_type = 0` rows.** The column has `DEFAULT 0`; verify real
   databases have no zero rows (the v0.0.549 one-time migration should have
   stamped all of them, and v0.0.550 stripped it). If any exist, they need a
   Go-side one-time backfill (version 1 → create, else update, delete
   recovered from the old blob's `deleted` bit while it is still readable) —
   SQL alone cannot decode blobs. Check before assuming.
5. **Tombstone visibility rules all flip together.** Dup-identity scans,
   snapshot filters, LiveState skips, recently-deleted, and authz visibility
   must all switch from `Deleted` to event-type in the same change —
   a half-converted filter either leaks tombstones into validation (breaking
   delete-then-recreate) or hides live deployments.
6. **`created_time` backfill idempotency** — guarded on `= 0`, after
   ApplySchema, no semicolons in comments, relies on the v1-create
   invariant.
7. **The mirror is a drift machine if it leaks.** Exactly one construction
   point mirrors def→flat and one decode point folds flat→def; nothing
   else may read or write the flat fields. Grep for flat-field access in the
   final review; phase 2 deletes them and the compiler proves it.
8. **`pq` layer rename.** `DeploymentEvent.CreatedAt` → `EventTime` plus new
   `CreatedTime` ripples through `queries.sql` and the generated/hand-written
   query layer; the `UNIQUE (deployment_id, version)` key and the
   spec-version index are untouched.
9. **Frontend merge/sort logic.** `deploymentMerge.js` and list ordering key
   on version/updated fields; renames land there, and `deleted` filters
   become event-type checks in the same commit as the regenerated model.

## Steps

1. Proto: add `DeploymentDef` + `DeploymentEventType`, reshape `Deployment`
   (envelope + def + bridge fields), regenerate.
2. SQL: schema + idempotent migrations (rename, add, backfill); update the
   `pq` row struct and queries; verify the `event_type = 0` question on a
   real database.
3. Store core: row-assembly constructor with the def→flat mirror, decode
   fallback wrapper, `buildDeploymentEvent` derivation,
   `pinnedSpecEventToProto`, write-path signatures, event-type deletion
   checks, `created_time` carry-forward.
4. Backend sweep, compiler-driven (validators, handlers, scheduler, engine,
   acme, network policies, statetest, tests).
5. Frontend + user-docs sweep.
6. Verify: full suite + `-race`; fresh install; upgrade of an existing dev
   database (migration + backfill + old-blob decode); create/update/move/
   delete/recreate; recently-deleted and history views; worker upgrade path
   against an old worker binary (bridge decode both directions).
7. Phase 2, one release later: strip bridge fields + fallback; then the
   clean blob re-encode/renumber migration if still wanted.
