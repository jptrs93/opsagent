# Core write update: implementation plan

Status: phase 1 implemented 2026-09-25, ships in v0.0.614. Phase 2a assessed
2026-09-26 with the asset store part implemented the same day (also v0.0.614).
Phase 2 planned 2026-09-25, not started. Phase 1 depended on the value reference pair work in
`value-reference-pairs-implementation-plan.md` landing first: both touch
`secrets/store.go`, `secrets/secrets.go`, `values/configs.go`, and
`values/references.go`. Phase 2 depends on both.

The end state is a state stream whose updates are facts, not projections:
one `CoreWriteUpdate` per Commit carrying seq, time, actor, and a list of
create, update, and delete mutations with the caller-owned definition as
payload. Version ordinals, facet versions, created times, and row ids are
derived by each consumer from the mutations it has folded. For that to work,
every change the write model considers real must be visible as a diff of the
definition, and a write that changes nothing must produce no mutation.

Phase 1 makes the value entities satisfy that rule. Phase 2 replaces
`CoreUpdate` with `CoreWriteUpdate`.

## Phase 1: seal id and no-op config writes

Landed as planned. Deviations: none. The migration statements stay in
`migrations.sql` until every cluster has rolled past v0.0.614, then fold into
its history note (step 8).

### Goals

1. A secret value write is a visible change to the `Secret` definition.
   Today `Secret { fs, space_id }` does not change when the value does; only
   the row's ciphertext and the projected `value_version` do.
2. The AEAD binding names a fact. Today it binds `(secret_id, value_version)`,
   a counted ordinal. It moves to `(secret_id, seal_id)`, an opaque identity
   issued once per value write.
3. A config write whose value equals the current value is a no-op: success,
   the current event returned, no row, no seq.

### Settled decisions

1. **Opaque seal id, not a ciphertext hash.** `smk_version` is recorded on
   every row, so re-sealing under a rotated key is a future operation. A
   hash-derived id would change on rotation and read as a new value. An
   opaque id issued at value write time survives rotation and is the right
   associated data for it.
2. **Zero re-seal migration.** The new AAD is
   `opendeploy-secret:user:s<secret_id>:<seal_id>`. Legacy rows get
   `seal_id = 'v' || value_version`, which reproduces today's AAD bytes
   exactly, so every existing ciphertext opens under the new function without
   the key. New ids use a different prefix so the namespaces cannot collide.
3. **Same plaintext twice is a new secret version.** The server would have to
   open the previous ciphertext to compare. Secret writes are rare and
   deliberate; a fresh seal is acceptable. This can be tightened later inside
   the seal path without another migration.
4. **No-op is success.** A no-op write returns the current event and consumes
   no seq. An error would break idempotent retries. `Commit` already treats
   an empty update as no commit.
5. **The value no-op does not suppress deployment rewrites.** A config set
   with `update_referencing_deployments` where the value is unchanged still
   repoints any referencing deployment that pins an older version to the
   current one. A deployment already at the current version is skipped, so no
   deployment event with an unchanged definition is written.
6. **Deployments are out of scope.** `RestartUpdate` writes a deployment
   event with an unchanged definition on purpose, and the frontend's
   `deploymentRestartEvent` detects a restart as a version with no facet
   changed. Making restart a visible diff is phase 2 work.
7. **`value_version` stays.** It remains the projected ordinal and half of
   `ValueRef`. Phase 1 only moves the cryptographic binding off it.

### Storage

`schema_secrets.sql` gains one column on `secret_event_log`:

```sql
seal_id TEXT NOT NULL DEFAULT '',  -- identity of the sealed value; AAD binds (secret_id, seal_id)
```

`migrations.sql` gains:

```sql
ALTER TABLE secret_event_log ADD COLUMN seal_id TEXT NOT NULL DEFAULT '';
UPDATE secret_event_log SET seal_id = 'v' || value_version WHERE seal_id = '';
```

Both are idempotent under `ApplyMigrations`: the add is tolerated as a
duplicate column on re-run, and the update matches nothing once applied.
Every row gets a seal id, including carry-forward rows, because the seal id
travels with the ciphertext it names.

Seal id format for new writes: `k` followed by 20 random bytes in lowercase
base32 without padding. Legacy ids are the literal `v<n>` strings.

### Secrets package

- `SealFunc` becomes `func(secretID int32, sealID string) (SealedValue, error)`.
  The store generates the seal id inside the write transaction, where it
  computes the version today, and passes it to the callback.
- `Record` and `Meta` gain `SealID string`.
- `userSecretAAD(secretID int32, sealID string)` formats the new string. It is
  the only AAD site; `openRecordLocked` and `sealFuncLocked` are its callers.
- `pq.SecretEvent` gains `SealID`. `InsertSecretEvent` writes it.
  `InsertSecretCarryEvent` copies `p.seal_id` forward in SQL alongside
  `smk_version`, `ciphertext`, and `nonce`. `secretEventColumns`,
  `scanSecretEvent`, and the record listing query at the bottom of
  `pq/values.go` read it.
- `CreateWithVersion` and `appendVersionWithDeploymentUpdates` issue the id
  and set it on the row and the returned `Record`.
- The cache in `Manager` is keyed by row id and unchanged.

### Wire

`Secret` gains `string seal_id = 3`. A value write is then a diff of the
definition, which is what phase 2 needs. Nothing in the frontend reads it in
phase 1; `derive.js` keeps deriving version lists from `valueVersion`.

### Config no-op

In `AppendConfigVersion`, the insert callback compares `value` with
`prev.Value.Value`. When equal it inserts nothing and returns the current
`value_version` with an empty `CoreUpdate`. `SetVersionedValueWithDeploymentUpdates`
then runs its rewrite loop against that version; `replaceDeploymentReferences`
reports whether it changed anything, and deployments it did not change are
skipped rather than rewritten. The handler returns the current `ConfigEvent`.

`RenameConfig`, `MoveConfigDirectory`, and `MoveConfigSpace` already return
`nil, nil` when nothing changes, as do their secret counterparts. The value
path is the only one without the check.

### Not changed in phase 1

- Asset uploads with identical content still append a version linking the
  existing content row. Assets carry `sha256` in the definition, so the same
  no-op rule applies cleanly; it is left for the phase 2 sweep with
  deployments.
- Secret value writes with identical plaintext, per decision 3.
- The worker's `local_runtime_inputs` AEAD, which binds kind and reference
  under the machine key and is unrelated to the primary's seal.

### Steps

Each step builds and passes tests on its own.

1. **Rebase onto the value reference pair change.** `SealFunc` and the
   append paths are edited by both.
2. **Column and migration.** Schema line, the two migration statements, and
   `pq` read and write plumbing for `seal_id` with the carry-forward copy.
   Add a `pq` test that inserts a row, appends a carry event, and asserts the
   seal id is copied.
3. **AAD move.** New `userSecretAAD`, `SealFunc` signature, seal id issuance
   in the store, `Record` and `Meta` fields. Update `store_test.go`,
   `secrets_test.go`, `webuihandler/secrets_test.go`, and
   `state/snapshot_replay_test.go` for the signature and the new field.
4. **Legacy open test.** Seal a value under the old
   `s<id>:v<version>` string directly with `aeadSeal`, insert the row with an
   empty seal id, run `Open` so the migration backfills `v<n>`, unlock the
   manager, and assert `RevealByID` returns the plaintext. This is the proof
   of decision 2.
5. **`Secret.seal_id` on the wire.** Proto field, regeneration, and the
   snapshot and update paths that build `Secret` from rows.
6. **Config no-op.** The comparison in `AppendConfigVersion`, the
   changed-report from `replaceDeploymentReferences`, and tests in
   `values_test.go`: same value produces no event and no seq; same value with
   update-deployments repoints a deployment pinned to an older version and
   skips one already current; the handler returns the current event.
7. **Docs.** `docs/engineering/secrets.md`: the AAD paragraph, the key files
   note on `SealFunc`, and a history note that legacy seal ids are the
   `v<n>` strings. `docs/product/deployments.md` or the configs section that
   describes value versions: a no-op set creates no version.
8. **After rollout.** Fold the two migration statements into the
   `migrations.sql` history note per its convention. Databases from before
   this release must step through it.

### Verification

- Unit suites in `secrets`, `values`, `webuihandler`, `pq`, and `state`.
- Manual: on a dev primary, set a config to its current value and confirm
  the version list does not grow and the response carries the current
  version; set a secret and confirm the new row has a `k`-prefixed seal id;
  restart the primary and reveal a secret written before the upgrade.
- The e2e harness has no secret or config case that would catch a regression
  here; the unit coverage in step 4 and step 6 is the gate.

## Phase 2a: the storage boundary

Phase 2 assumes the primary store is one write model under one seq. It is
not yet: some global state is written outside Commit, some sits in mutable
rows without a seq, and some tables mix global facts with this machine's
private bookkeeping. Phase 2a tidies that boundary without touching the
wire, so that the log can later reproduce the store on a replica. Asset
content bytes are the stated exception: they travel by their own channel.

### Classification

Assessed 2026-09-26 against every table in `pq/sql`.

**Already event logs under Commit with a seq.** The eight entity event logs
(deployment, scheduled instance, node, secret, config, asset, network
policy), the three authz event logs, and the two status logs
(`scheduled_instance_status`, `node_status_log`). Nothing to do.

**Global write model, under Commit, without a seq.** Convert to the event
log pattern or add `global_seq`: `spaces`, `users` (passkeys in `data_blob`), `value_directories`, `asset_directories`,
`system_config_revisions` (append-only already), `nix_store_resets`.

**Global write model written outside Commit.** Bring under Commit, with a
seq:

- `system_secrets`: cluster CA key, primary cluster key, workload CA key,
  web UI local CA key, ACME material, written by `secrets.Manager.SetInternal`
  from `pki/material.go` and `acmeissue` through the manager's root queries.
  Entity `SystemSecret` keyed by name; the name-bound AAD stays.
- `secret_keyslots`: the recovery slot is global (wraps the SMK under the
  recovery code); the machine slot is node-local by construction. Split into
  a `SecretsKeyring { smk_version, recovery slot }` singleton and a local
  machine slot table. Both are written by `Unlock` and
  `GenerateRecoveryCode` outside Commit today.
- `public_keys`: token verification keys, upserted at startup from
  `webuihandler/handler.go`. Entity keyed by kid.
- `agent_sessions`: long-lived agent tokens must survive rollover. Already an
  entity on the wire with its own sidecar; needs a seq and Commit.
- `personal_sessions`: existence and revocation are global facts. The
  activity touch is already throttled by `personalSessionActivityTouchInterval`,
  so one seq per session per interval is acceptable; `last_active_at` is the
  one field to demote to node-local if that proves noisy.

**Node-local, stays out of the log.** `asset_store` placement flags, the
machine keyslot, and the files under the data directory: the machine key,
`wireguard.key`, `issued-tls`, `fifo`, the releases directory, local asset
files, Litestream state. Runtime sidecars that are never persisted (backup
status, secrets lock status, ingress diagnostics, the netmap).

### Assets: implemented 2026-09-26

- `Asset` gained `storage_key`, carried on every `asset_event_log` row and
  backfilled from the store row matched by `sha256`. The event log alone
  now names every local file and S3 object; `asset_store` shrinks to this
  machine's placement flags and is rebuildable, though rebuilding it on a
  replica is deliberately not done here.
- Inline storage is gone. Content of every size takes the staged-file path;
  `assets.MigrateInlineContent` writes existing inline blobs out at startup,
  claims them locally, and drops the column, and the reconciler moves them
  to the configured target.
- `asset_migrations` is gone. The reconciler converges from the placement
  flags, the settings gate during a transition is gone with it (the S3
  identity pin in `ValidateSettingsUpdate` was the real protection), and
  `BackupStatus.asset_pending` is the progress signal;
  `asset_migration_running` is reserved.
- Rejected: naming objects by `sha256`. Hash-first uploads read the file
  twice and cannot work from a browser, and copy-after-upload is a
  server-side `CopyObject` (multipart above 5 GB) over content that is
  expected to reach hundreds of GB. A key chosen at upload start never needs
  a rename, and dedupe still works after the fact by linking the existing
  key.

### Open for the rest of 2a

- Whether personal sessions are in or out (argued in above).
- Direct-to-S3 streaming without local staging, for primaries with little
  disk: key-first already supports it; the local file becomes a cache
  filled after the S3 put, and the CLI can declare a sha for a dedupe
  pre-flight without anything depending on it.

## Phase 2: CoreWriteUpdate

Planned, not started. Everything below was checked against the code on
2026-09-25; file and function names are current as of that date.

### Goals

1. One `CoreWriteUpdate` per Commit: seq, time, actor, and the list of
   create, update, and delete mutations, each carrying the caller-owned
   definition. No version ordinals, facet versions, created times, event ids,
   or `deleted` flags on the wire.
2. `Snapshot` is a compacted log of the same message. The browser has one
   reducer for bootstrap and steady state, and `visibility.go` stops
   converting between two shapes.
3. Spaces, users, value directories, asset directories, authz rule
   templates, authz global rules, and the system config become entities in
   the same log with the same mutation shape, instead of mutable rows,
   replacement lists, and a revision id.
4. Observed statuses are entities in the same log. The primary records a
   node's report as a write with a global seq. The report's `updated_at` is
   the producing node's clock, used to drop stale reports when recording;
   it is not a substitute for the seq.
5. Optimistic concurrency tokens are the seq of the entity's last mutation.
6. Every write the model considers real is a visible diff of the definition,
   so the no-op rule from phase 1 extends to deployments and assets. Restart
   becomes a diff. An asset upload with unchanged content becomes a no-op.

### Settled decisions

1. **Three mutation messages, fake oneof.** `CoreMutation` holds exactly one
   of `CreateMutation`, `UpdateMutation`, `DeleteMutation`. Create and update
   carry `entity_type`, `entity_id`, and a `CoreEntity` payload with one field
   per definition; delete carries type and id. `entity_type` is redundant
   with the set `CoreEntity` field on create and update; the decoder checks
   they agree. Entity ids are `int64` because authz ids already are.
2. **Actor is per commit.** Today `author` is a column on every event row,
   set by the domain function from the request principal. Every row written
   in one Commit already has the same author, so it lifts to the update
   without loss. Mutations added by an `UpdateTrigger` (the scheduler) carry
   the actor of the commit that triggered them. Commits driven by the
   primary itself (startup reconcile, drain ticks, status writes) use actor
   0. The `author` columns stay in SQL; they are the projection's copy.
3. **One projection is precomputed: the deployment version ordinal.** It
   goes in a sibling `derived` field, never inside `CoreWriteUpdate`. The
   ordinal is load-bearing on the server (scheduler pins, worker cache, logs
   and metrics keys, `ScheduledInstance.deployment_version`) and the browser
   has to speak that key back for history, logs, and metrics. The browser
   cannot count it: the snapshot cannot ship a deployment's full history,
   because every restart and every update is an event and retention is
   unbounded. Everything else the browser shows today from envelopes is
   either derived by folding or is no longer shown.
4. **Value versions are counted, not shipped.** The snapshot ships the full
   history of every live secret, config, and asset, as it does today. The
   browser derives `value_version` as the count of value-changing mutations:
   `Secret.seal_id`, `Config.value`, `Asset.sha256`. Phase 1 made secret
   and config writes obey that rule; the asset no-op in this phase completes
   it. `ValueRef.version` in a deployment spec then resolves against the
   count. Before shipping, an audit query on the flippingcopilot and allevia
   databases checks `value_version` against the count of `value_changed`
   rows per entity; a mismatch on legacy rows means the ordinal has to move
   to `derived` as well, which is a fallback, not a design change.
5. **Visibility is per entity, by current space.** Today `filterSecrets`,
   `filterConfigs`, `filterAssets`, and `filterDeployments` test the space
   on each row, so a viewer without access to an entity's former space sees
   a truncated history. Counting needs the whole history, so visibility
   moves to the entity's current space: a viewer who can see the entity
   sees all of its mutations. `streamVisibility` already tracks the current
   space per entity and already forces a reset when it changes; the change
   is that the snapshot builder and `visibleUpdate` use that space for every
   mutation of the entity. This is a semantic change and is called out in
   the open items for confirmation.
6. **Snapshot compaction rules.** One `CoreWriteUpdate` per distinct seq,
   ascending, carrying the mutations retained at that seq.
   - Secrets, configs, assets: every mutation of every live entity.
   - Deployments: the create mutation (so created time and creator are
     facts in the log), the latest mutation, and every mutation whose
     version a live scheduled instance pins. A deleted deployment that a
     live instance still pins ships the pinned mutation and the delete
     mutation. This is today's `snapshot.go` retention rule expressed as
     mutations.
   - Nodes, scheduled instances, network policies, authz grants, templates,
     global rules, spaces, users, directories, system config: one
     `CreateMutation` at current state, at the seq, time, and actor of the
     entity's last mutation. The browser cannot distinguish it from a
     genuine create and does not need to.
   - Deleted entities are absent except for the pinned deployment case.
7. **Mutable tables join the seq.** `spaces`, `users`, `value_directories`,
   `asset_directories`, and `system_config_revisions` gain `global_seq`
   (last mutation), `updated_at`, and where missing `author`. They keep
   their current shape otherwise: no history, one row per entity, `DELETE`
   on removal. The `deleted` flags on `Space`, `ValueDirectory`, `AssetDirectory`,
   `AuthzRuleTemplateRecord`, and `AuthzGlobalRuleRecord` go; a removal is a
   `DeleteMutation`. Users are never deleted today and that does not change. Converting these tables to event
   logs is not needed for the wire and is not done.
8. **Authz templates and global rules are per-entity mutations.** Both
   already have event logs with `global_seq`. The `AuthzRuleTemplateList`
   and `AuthzGlobalRuleList` replacement wrappers go, along with the
   pointer-wrapper rationale in `authz/store.go`. A rule change that touches
   several rows produces several mutations in one update.
9. **System config is a singleton entity.** `entity_id` is 1. A settings
   write is an `UpdateMutation` with the full `SystemConfig` payload. The
   revision id stays in SQL as the projection's key; nothing on the wire
   references it. `SystemConfigVersion` and the unused `VersionedSubs` go.
10. **Observed statuses are latest-only entities.** `scheduled_instance_status`
    and `node_status_log` already carry `global_seq`, are appended under
    Commit, and are keyed by `(id, updated_at)`. A recorded report is an
    `UpdateMutation` of a `ScheduledInstanceStatus` or `NodeStatus` entity
    keyed by the instance or node id. `updated_at` stays inside the payload
    as the producer's clock; `RecordStatus` already refuses to publish a
    report older than the last recorded one, so consumers fold by seq alone
    and the browser's clock merge in `deploymentMerge.js` goes.
11. **Concurrency tokens are seqs.** `expected_version` on deployment update,
    node evict, enrollment accept, and network policy write becomes
    `expected_seq`, checked against the latest row's `global_seq` for the
    entity. The `referencing_deployments` list on config and secret set
    becomes `{deployment_id, expected_seq}`. `DeploymentSpecVersionRef` goes.
    The server still bumps and stores every ordinal; only the wire check
    changes.
12. **Secret reveal addresses a value by `ValueRef`.** `SecretRevealRequest`
    takes `{secret_id, version}` because event ids are no longer on the
    wire. The handler resolves the version to the row.
13. **Restart is a diff.** `Scheduling` gains `int32 generation = 3`.
    `RestartUpdate` increments it. The scheduler keeps detecting a restart
    through the deployment version bump it already sees, so nothing changes
    in `scheduler.go` or on the worker. The browser's `deploymentRestartEvent`
    compares `scheduling.generation` between consecutive mutations instead
    of comparing four facet versions. With that, `BuildDeploymentUpdateEvent`
    can reject a write whose definition equals the previous one, and does.
14. **Asset upload with unchanged content is a no-op.** Same rule as config:
    success, current state returned, no row, no seq. Content is compared by
    `sha256` before the identity Commit, so the large-asset file write is
    skipped too.
15. **The worker is untouched.** `CoreUpdate` is not referenced under
    `app/secondary`, `lib`, or `clusterhandler`; the scheduled-instance feed
    is `[]ScheduledInstanceState` and stays. The cluster protocol version
    does not change.
16. **No dual protocol.** The SPA is served by the primary and upgrades with
    it. The stream is protobuf only, and its consumers are the SPA and the
    `/v1/global/snapshot` JSON endpoint used by agents. Both change in one
    release. A tab left open across the upgrade reconnects, fails to decode
    the first message, and reloads; the reload on decode failure is added
    in this phase because it does not exist today.

### Wire

`api-contract/model_global_operations.proto`:

```proto
message CoreWriteUpdate {
  int64 seq = 1;
  google.protobuf.Timestamp time = 2;
  int32 actor = 3;                 // user id; 0 = system, negative = agent of user -actor
  repeated CoreMutation mutations = 4;
}

message CoreMutation {              // exactly one set
  CreateMutation create = 1;
  UpdateMutation update = 2;
  DeleteMutation delete = 3;
}

message CreateMutation {
  CoreEntityType entity_type = 1;
  int64 entity_id = 2;
  CoreEntity entity = 3;
}

message UpdateMutation {
  CoreEntityType entity_type = 1;
  int64 entity_id = 2;
  CoreEntity entity = 3;
}

message DeleteMutation {
  CoreEntityType entity_type = 1;
  int64 entity_id = 2;
}

enum CoreEntityType {                // numbers equal the CoreEntity field numbers
  CORE_ENTITY_UNSPECIFIED = 0;
  CORE_ENTITY_DEPLOYMENT = 1;
  CORE_ENTITY_SCHEDULED_INSTANCE = 2;
  CORE_ENTITY_NODE = 3;
  CORE_ENTITY_SECRET = 4;
  CORE_ENTITY_CONFIG = 5;
  CORE_ENTITY_ASSET = 6;
  CORE_ENTITY_NETWORK_POLICY = 7;
  CORE_ENTITY_SPACE = 8;
  CORE_ENTITY_USER = 9;
  CORE_ENTITY_VALUE_DIRECTORY = 10;
  CORE_ENTITY_ASSET_DIRECTORY = 11;
  CORE_ENTITY_AUTHZ_RULE_TEMPLATE = 12;
  CORE_ENTITY_AUTHZ_GRANT = 13;
  CORE_ENTITY_AUTHZ_GLOBAL_RULE = 14;
  CORE_ENTITY_SYSTEM_CONFIG = 15;
  CORE_ENTITY_SCHEDULED_INSTANCE_STATUS = 16;
  CORE_ENTITY_NODE_STATUS = 17;
}

message CoreEntity {                 // exactly one set
  Deployment deployment = 1;
  ScheduledInstance scheduled_instance = 2;
  Node node = 3;
  Secret secret = 4;
  Config config = 5;
  Asset asset = 6;
  NetworkPolicy network_policy = 7;
  Space space = 8;
  User user = 9;
  ValueDirectory value_directory = 10;
  AssetDirectory asset_directory = 11;
  AuthzRuleTemplate authz_rule_template = 12;
  AuthzGrantValue authz_grant = 13;
  AuthzGlobalRule authz_global_rule = 14;
  SystemConfig system_config = 15;
  ScheduledInstanceStatus scheduled_instance_status = 16;
  NodeStatus node_status = 17;
}

message DerivedUpdate {              // projections the server precomputes; may change without touching the facts
  int64 seq = 1;
  repeated DeploymentVersionLabel deployment_versions = 2;
}

message DeploymentVersionLabel {
  int32 deployment_id = 1;
  int64 seq = 2;                     // the mutation this labels
  int32 version = 3;
}

message StateStreamMsg {
  Snapshot snapshot = 1;
  CoreWriteUpdate write = 9;
  DerivedUpdate derived = 11;
  bool heartbeat = 4;
  BackupStatus backup_status = 5;
  IngressDiagnosticList ingress_diagnostics = 6;
  SecretsStatusResponse secrets_status = 7;
  AgentSessionList agent_sessions = 8;
  reserved 2;                        // CoreUpdate core
}

message Snapshot {
  int64 seq = 1;
  repeated CoreWriteUpdate writes = 23;
  DerivedUpdate derived = 25;        // labels for every deployment mutation in writes
  repeated AgentSession agent_sessions = 15;
  SecretsStatusResponse secrets_status = 19;
  BackupStatus backup_status = 20;
  IngressDiagnosticList ingress_diagnostics = 22;
  reserved 2 to 14, 16 to 18, 21;
}
```

`Space`, `User`, `ValueDirectory`, and `AssetDirectory` become pure
definitions: `id`, `created_at`, `author`, `updated_at`, and `deleted` leave
them. `AuthzRuleTemplate` and `AuthzGlobalRule` are the existing record
messages without `id` and `deleted`. `SystemConfig` is the existing settings
message; `SystemConfigVersion` goes. `Scheduling` gains `generation`.
`SecretRevealRequest` becomes `{secret_id, version}`. The event messages
(`DeploymentEvent`, `NodeEvent`, `ScheduledInstanceEvent`,
`NetworkPolicyEvent`, `AuthzGrantEvent`, `SecretEvent`, `ConfigEvent`,
`AssetEvent`) leave the public API. `DeploymentEvent` stays defined because
`ScheduledInstanceState.config` carries it to the worker over the cluster
protocol; only the public surface stops using it.

Concurrency fields:

| Request | Today | Phase 2 |
|---|---|---|
| `DeploymentUpdateRequestV2.expected_version` | current ordinal + 1 | `expected_seq` |
| `NodeEvictRequest.expected_version` | ordinal | `expected_seq` |
| `EnrollmentAcceptRequest.expected_version` | ordinal | `expected_seq` |
| `NetworkPolicyUpdateRequest.version` | ordinal | `expected_seq` |
| config and secret set `referencing_deployments` | `{id, spec_version}` | `{deployment_id, expected_seq}` |

The deployment check changes meaning as well as name: today the caller
sends the version it expects the write to produce; with seqs it sends the
seq of the mutation it last saw, the same rule as the other four.

### Storage

`migrations.sql` gains one block under the next release:

```sql
ALTER TABLE spaces ADD COLUMN global_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE spaces ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE spaces ADD COLUMN author INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN global_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN author INTEGER NOT NULL DEFAULT 0;
ALTER TABLE value_directories ADD COLUMN global_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE value_directories ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE asset_directories ADD COLUMN global_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE asset_directories ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE system_config_revisions ADD COLUMN global_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE system_config_revisions ADD COLUMN author INTEGER NOT NULL DEFAULT 0;
```

Rows that predate the columns carry seq 0 in synthetic creates until their
next write. A concurrency check against seq 0 passes when the row's stored
seq is 0, so first writes after the upgrade are not rejected. `updated_at`
0 falls back to `created_at` where one exists and to the snapshot time
otherwise.

No event table changes. `event_id`, ordinals, facet versions, `author`, and
`created_time` all stay in SQL as the server's projection.

`deployment_event_log` writes reject an unchanged definition once
`generation` exists (decision 13). `asset_event_log` writes reject unchanged
content (decision 14). Both are checks in the domain insert closure, in the
same place phase 1 put the config comparison.

### Backend

**`state` package.** `state.Update` becomes `apigen.CoreWriteUpdate`, built
through a small builder so producers do not hand-assemble the fake oneof:

```go
func (u *Update) Create(t apigen.CoreEntityType, id int64, e apigen.CoreEntity)
func (u *Update) Update(t apigen.CoreEntityType, id int64, e apigen.CoreEntity)
func (u *Update) Delete(t apigen.CoreEntityType, id int64)
func (u *Update) Label(deploymentID int32, version int32)
```

`Commit` stamps seq and time, takes the actor from the update, and publishes
a `state.Published { Write *CoreWriteUpdate; Derived *DerivedUpdate }`. An
update with no mutations and no labels consumes no seq, as today.
`Subscribe`'s `project` receives the published pair. `statetest` helpers change with it.

**Producers.** Every file that returns a `*state.Update` today switches to
the builder. From the inventory: `deployments/write.go` and
`deployments.go` (create, update, delete, restart), `nodes/nodes.go`,
`nodes/enrollment.go`, `nodes/evict.go`, `nodes/spaces.go`,
`networkpolicies/networkpolicies.go`, `secrets/store.go`,
`values/configs.go`, `values/references.go`, `values/directories.go`,
`assets/db_assets.go`, `assets/db_directories.go`, `authz/store.go`,
`users/users.go`, `systemconfig/store.go`, `nixstores/nixstores.go`,
`scheduledinstances/status.go`, `scheduler/instances.go`,
`scheduler/scheduler.go`, and `netmappublisher/publisher.go`. The mechanical
rule per site: an insert that creates an entity calls `Create`, one that
appends a version calls `Update`, a tombstone or row delete calls `Delete`,
and every deployment event insert also calls `Label` with the stored
ordinal. Status writers call `Update` with the status entity.

**Snapshot builder.** `state/snapshot.go` builds `[]CoreWriteUpdate` from
the retention rules in decision 6, grouped by seq. The value-entity listing
queries in `pq/values.go` already return full histories with `global_seq`,
`author`, and `created_time` per row. The deployment query keeps the
live-ordinal and pinned-tombstone logic and adds the first event per live
deployment. Latest-only entities read their current rows. The builder emits
`DerivedUpdate` labels for every deployment mutation it includes; the
status tables contribute synthetic creates like every other latest-only
entity. `snapshot_replay_test.go` becomes
the proof that folding the snapshot equals folding every update since seq
0, per entity type; it is the main test of this phase.

**Stream handler and visibility.** `state_stream.go` sends the snapshot,
then one `StateStreamMsg` per published triple with the visible parts.
`visibility.go` gets one function, `visibleMutations(ctx, []CoreMutation)
[]CoreMutation`, keyed by `entity_type`, using the current-space map per
entity for deployments, secrets, configs, and assets; the snapshot filter
maps it over every write and drops writes that become empty; the update
filter is the same call. `needsReset` reads space changes, grant mutations,
and space mutations off the mutation list. `snapshotUpdate` and
`updateSnapshot` go. Status mutations are filtered by the visibility of the
instance or node they describe, as today.

**Concurrency checks.** The five request sites in decision 11 compare the
supplied `expected_seq` with the entity's latest `global_seq`, read inside
mutate. Zero means no check, as zero `expected_version` does today. The
deployment reference rewrite in `values/references.go` compares per
deployment.

**Secret reveal.** `secrets.RevealByRef(secretID, version)` resolves the
row through the existing version listing and calls the current open path.
The row-id variant stays for internal callers.

**Agent endpoints.** `GET /v1/global/snapshot` returns the new `Snapshot`.
`agent_instructions.md` sections that read `deployment_events` and
`version` are rewritten against writes, `derived.deployment_versions`, and
`expected_seq`, including the note that the deployment check is no longer
"current version plus one".

### Frontend

`state/tree.js` becomes a fold over `CoreMutation`:

- One `Map<id, entry>` per entity type. Value entities and deployments keep
  `history: [{seq, time, actor, entity}]`; every other type keeps the
  latest entity plus `seq`, `time`, and `actor` of the last mutation.
  Deployment history is pruned to latest plus versions a live instance
  references, as today, and a delete mutation marks the entry deleted
  without dropping pinned history.
- Deployment version labels from `derived` land in `Map<deploymentId,
  Map<seq, version>>`. History rows show the label; a missing label shows
  the seq.
- Value versions are the running count of value-changing mutations per
  entity, computed in the fold and stored on each history row.
- Status entities fold like any other latest-only entity; the clock merge
  in `deploymentMerge.js` goes.
- Seq gating is per message: a write or derived part older than the current
  seq is dropped; a gap forces a reconnect, as today.

`state/derive.js` view models keep their output shapes where the pages read
them (`deleted`, `createdTime`, `author`, `versions`) and compute them from
the fold instead of from envelopes: `deleted` from the delete mutation,
`createdTime` and `author` from the first retained mutation, `valueVersion`
from the count. `lib/deployment.js`'s `deploymentRestartEvent` compares
`scheduling.generation`. `pages/secrets.js` reveals by `{secretId,
version}`. The `expectedVersion` and `specVersion` call sites listed in the
concurrency table send `expectedSeq` from the entry's last-mutation seq.
`capi/model.js` regenerates. The stream reader in `capi/capi.js` reloads
the page on a decode failure of the first message after connect.

`tree.test.js` fixtures move to mutations; the round-trip test (fold
snapshot, apply updates, compare with fold of all updates) is added on the
browser side too, against fixtures exported from the Go replay test.

### Docs

- `docs/future-work/global-state-stream-implementation-plan.md` gets a
  history note pointing here; its envelope and retention sections describe
  the pre-phase-2 shape.
- `docs/engineering/api.md`: the state stream section, the concurrency
  table, and the reveal request.
- `docs/engineering/frontend.md`: the state section.
- `docs/engineering/secrets.md`: reveal by reference.
- `agent_instructions.md` as above.
- `CLAUDE.md` index line for this plan.

### Steps

Each step builds and passes tests on its own. Steps 1 to 4 ship nothing to
the browser and can land one at a time; step 5 is the cut-over and lands
with 6 and 7 in one release.

1. **Restart as diff and asset no-op.** `Scheduling.generation`, the
   `RestartUpdate` bump, the unchanged-definition rejection in
   `BuildDeploymentUpdateEvent`, the asset content comparison, and
   `deploymentRestartEvent` reading `generation`. Tests: restart bumps
   generation and version; an update with an identical definition returns
   the current event and no seq; an identical asset upload returns the
   current event and writes no file.
2. **Mutable tables gain seq.** Migration block, `pq` read and write
   plumbing, and the domain writers stamping `global_seq` from the commit
   seq. No wire change.
3. **Value version audit.** A `pq` query and a test-only command that
   asserts `value_version` equals the count of `value_changed` rows for
   every secret, config, and asset. Run it against copies of the two
   production databases. Decide the fallback in decision 4 from the result.
4. **Builder and publisher.** `state.Update` builder, `Commit` stamping,
   `state.Published`, every producer converted, the snapshot builder, and
   the replay test. `apigen` gains the new messages while `CoreUpdate` is
   still served; the handler converts the published triple back to
   `CoreUpdate` for one step so the browser is unaffected. This is the
   largest step and the one to review most carefully; the conversion shim
   is what keeps it landable on its own.
5. **Wire cut-over.** Remove `CoreUpdate`, the event messages from the
   public API, the list wrappers, `SystemConfigVersion`, and the `deleted`
   flags. `StateStreamMsg` and `Snapshot` take the new fields. `visibility.go`
   rewrite. Concurrency fields and `SecretRevealRequest` change. Regenerate
   both sides.
6. **Frontend fold.** `tree.js`, `derive.js`, `deployments.js`, the call
   sites in the concurrency table, reveal, and the reload on decode failure.
7. **Docs and agent instructions.**
8. **After rollout.** Fold the migration block into the `migrations.sql`
   history note. Remove the row-id reveal path if nothing internal uses it.

### Verification

- `state/snapshot_replay_test.go`: for a seeded store with every entity
  type, space moves, a deleted deployment with a draining instance, a
  restart, and a no-op write, folding the snapshot equals folding the
  updates since seq 0 and value version counts equal the SQL ordinals.
- Visibility tests in `webuihandler`: a viewer gains and loses a space and
  sees a reset; a moved secret's whole history is visible from its new
  space and none of it from the old one; pinned versions of a deleted
  deployment reach a viewer who can see the instance.
- Concurrency tests: stale `expected_seq` is rejected on all five sites;
  zero passes; a legacy row with seq 0 accepts its first write.
- Frontend `tree.test.js` round trip against the Go fixtures.
- Manual on a dev primary: open two tabs, restart a deployment, move a
  secret between spaces, evict a node, edit settings, add a global rule,
  and watch both tabs converge; leave a tab open across a restart of the
  primary with the new build and confirm it reloads.
- The e2e harness drives deployments and node flows through the SPA and
  covers those paths end to end; the value, authz, settings, and restart
  paths rely on the unit coverage above.

### Open items

- Decision 5 changes what a viewer with access to only the current space
  can see of a moved secret, config, or asset: the whole history instead of
  the rows written in that space. Confirm before step 5.
- Whether `derived` should also carry `created_time` for latest-only
  entities (node enrolled-at, user created-at). Today `pages/cluster.js`
  and `pages/status.js` show these from envelopes. The plan drops them from
  the stream; if the pages need them back they go in `derived`, not in the
  facts.
- Whether agent sessions should become a core entity. They have no version
  today and ship as a replacement list sidecar; nothing in this phase needs
  them to move.
