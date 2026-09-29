# Core write update: implementation plan

Status: phase 1 implemented 2026-09-25 and its seal id reversed 2026-09-28,
all in v0.0.614. Phase 2a assessed 2026-09-26 with the asset store part
implemented the same day and the remaining storage items landed 2026-09-28
(also v0.0.614).
Phase 2 planned 2026-09-25 and revised 2026-09-29, not started; the
proto draft in `api-contract/model/events.proto` is the only code.
Phase 1 depended on the value reference pair work in
`value-reference-pairs-implementation-plan.md` landing first: both touch
`secrets/store.go`, `secrets/secrets.go`, `values/configs.go`, and
`values/references.go`. Phase 2 depends on both.

The end state is a state stream whose updates are facts, not projections:
one `CoreWriteUpdate` per Commit carrying seq, time, actor, and a list of
create, update, and delete mutations with the entity as payload, built by
the `mutate` that wrote the rows. The store can produce the whole stream from
its tables, a client bootstraps from a compacted subsequence of it and
resumes from the last seq it applied, and each consumer wraps the entity
with the envelope it needs. For that to work, every change the write model
considers real must be visible as a diff of the entity, and a write that
changes nothing must produce no mutation.

Phase 1 makes the config write path satisfy that rule. Phase 2 replaces
`CoreUpdate` and `Snapshot` with `CoreWriteUpdate` on one event stream.

## Phase 1: no-op config writes, and the seal id that was reversed

Landed 2026-09-25 and partly reversed 2026-09-28, both inside the unreleased
v0.0.614. What stands:

1. A config write whose value equals the current value is a no-op: success,
   the current event returned, no row, no seq. `AppendConfigVersion` compares
   `value` with `prev.Value.Value`; `replaceDeploymentReferences` reports
   whether it changed anything so deployments already at the current version
   are skipped rather than rewritten. Same plaintext twice is still a new
   secret version, because the server would have to open the previous
   ciphertext to compare; that can be tightened later inside the seal path
   without a migration.
2. The secret AEAD binds a stored fact, not a projection. Phase 1 moved it
   from `(secret_id, value_version)` to `(secret_id, seal_id)`, an opaque id
   issued per value write and carried forward by renames and moves, and put
   `seal_id` on the wire so a value write would be a visible diff of
   `Secret`.

What was reversed, and why. `value_version` is a derivation over the
secret's own data model, which includes the ciphertext and every other
non-envelope column on the row: a row whose ciphertext changed for a value
write is a new value, and the writer already records that as
`value_changed`. The server therefore derives `value_version` and ships it
as a fact on the payload, the same treatment the deployment version
ordinal gets as `spec_version` (Phase 2 decision 4); the browser never
counts anything and never needs an identity for the value on the wire. That left the seal id with one job, the
per-value half of the associated data, and that half protects nothing:
every value of a secret is revealable under the same grants, so moving a
ciphertext between two values of one secret crosses no boundary. The
binding is now `secret_id` alone (`opendeploy-secret:s<secret_id>`),
`seal_id` is gone from the row and reserved on the wire (`Secret` field 3),
and the settled decision that an opaque id would survive a master key
rotation without reading as a new value is moot: a re-seal is a row with
`value_changed = 0`, the writer's call, not identity's.

Migration. Rows from v0.0.612 and earlier are bound to
`opendeploy-secret:user:s<id>:v<value_version>`; rows from unreleased
v0.0.614 builds to `...:s<id>:<seal_id>`. Both need the master key to move,
so `secrets.Manager.migrateSealsLocked` runs after every successful unlock
(`Open` with the machine key, `Unlock` with the recovery code): each row
that does not open under the current binding is opened under its legacy
one and re-sealed in place, the `system_secrets` rows are appended to the
event log (below), and the legacy column and table are dropped once no row
is left behind. A row that opens under no known binding is logged and left,
and the pass retries at the next unlock. The `system_secrets` table's
presence is the pending marker, so a fresh schema does not create it.
Remove the pass once every active cluster has rolled forward, per the
`migrations.sql` history-note convention.

Verification: `secrets_test.go` seeds one row under each legacy binding
plus a `system_secrets` row, opens the store, and asserts every row opens
under the current binding, the system secret is revealable, the artifacts
are gone, and a second open changes nothing. The e2e harness has no secret
case; the unit coverage is the gate.

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

**Global write model, under Commit, without a seq.** Done on 2026-09-28:
`space_event_log`, `user_event_log` (passkeys in `data_blob`), `value_directory_event_log`,
`asset_directory_event_log`, `system_config_event_log`, and `nix_store_reset_event_log` are
append-only event tables (`id`, `global_seq`, `event_time`, `author`, entity
id, `event_type` as `AuthzVerb`, full document), alongside `agent_session_event_log`
and `user_session_event_log` in the same shape. Rows are never updated or deleted; a
removal is an `event_type = 3` row and the live state is the newest row per
entity id that is not a delete. Space and directory ids are allocated as
`MAX + 1` under the commit lock, so a deleted space's id is no longer reused
(the old rowid primary key reused it). Legacy tables are renamed aside and
copied in as seq 0 create events by `pq.Open` on first start. Deleting users
stays future work; nothing writes a user delete row. The shape is described
in `docs/engineering/api.md`.

**Global write model written outside Commit.** Bring under Commit, with a
seq:

- `system_secrets`: merged into `secret_event_log` on 2026-09-28. The
  cluster and workload CAs, the web UI CA and bundle, and the ACME account
  key are ordinary secrets in the root of space 0, authored by the system,
  written under Commit with a seq, and sealed under the same `secret_id`
  binding as every other row. `SetInternal` appends a version and
  `RevealInternal` opens the newest one; the separate table, record type,
  cache, and name-bound AAD class are gone. Space 0 values are unreachable
  from every user-facing path: `authz.SystemSpaceAllows` refuses secret,
  config, and asset requests in space 0 before any grant is consulted, and
  refuses deployment creation there, so nothing can list, reveal, move,
  delete, or reference them; the manager's `Resolve`, `RevealByID`,
  `RevealByRef`, and `MetaByID` refuse space 0 rows as well, so the worker
  fetch path cannot reach them either. Deployments in space 0 stay viewable
  (the status and logs pages show the self and netproxy deployments).
- `secret_keyslot_event_log`: done on 2026-09-28, and not split. The earlier idea of
  keeping the machine slot node-local was wrong once a replica may take
  over: the replica needs a wrapped SMK it can open, and the replicated log
  is the channel that exists. The table is an append-only event table keyed
  by `(kind, node_id)` with `SecretKeyslotKind` an enum (1 machine, 2
  recovery); the machine slot is one row per node, the recovery slot is
  kind 2 under node 0. `Initialize`, `GenerateRecoveryCode`, and `Unlock`
  write under Commit with an author, and eviction deletes the evicted node's
  machine slot in the eviction commit. The associated data stays the kind
  name (`opendeploy-keyslot:machine`, `opendeploy-keyslot:recovery`): each
  slot has its own KEK, so a node binding would protect nothing and would
  have left the recovery slot on a fallback until someone next used the
  code. Nothing keyslot-shaped enters `CoreUpdate`. How a replica's slot gets written is
  left open: the clean answer is an asymmetric per-node machine key so the
  primary can seal the SMK to a node it never shares a key with, and the
  `(kind, node_id)` key and `smk_version` leave room for it.
- `public_keys`: gone on 2026-09-28. Bearer tokens are opaque and verified
  against the session row's hash, so there are no verification keys.
- `agent_session_event_log`: done on 2026-09-28. Every write (create, approve, token
  claim, status change, revoke) is a Commit that stamps `global_seq` and
  publishes the latest document in `CoreUpdate.agent_sessions`; the
  `StateStreamMsg` sidecar is reserved and the domain service no longer has
  its own pubsub or mutex.
- `user_session_event_log` (was `personal_sessions`, then `user_sessions`): done on 2026-09-28. Existence and
  revocation are global facts, written under Commit and published in
  `CoreUpdate.user_sessions`. The per-request activity touch and
  `last_active_at` were dropped the same day (its only reader was a column on
  the sessions page), so a session is created once and revoked once. The row
  carries a `kind` (`FULL` or `BOOTSTRAP`; the scope list is gone, see
  2026-09-28) and the token hash; the hash stays out of the wire projection. Both session tables are owner-filtered on the wire and never
  deleted: they grow unbounded, deliberately, with no retention horizon.

**Node-local, stays out of the log.** `asset_store` placement flags and the
files under the data directory: the machine key,
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

- `User` as a document with typed passkeys instead of the opaque
  `data_blob`; deferred until the entity shapes of Phase 2 are fixed.
- Direct-to-S3 streaming without local staging, for primaries with little
  disk: key-first already supports it; the local file becomes a cache
  filled after the S3 put, and the CLI can declare a sha for a dedupe
  pre-flight without anything depending on it.

## Phase 2: CoreWriteUpdate

Planned 2026-09-25, revised 2026-09-29 after the stream design discussion,
not started. Everything below was checked against the code on 2026-09-29;
file and function names are current as of that date.

### Goals

1. One `CoreWriteUpdate` per Commit: seq, time, actor, and the list of
   create, update, and delete mutations, each carrying the entity. The
   `mutate` that writes the rows builds and returns it; `Commit` stamps
   the seq and publishes it, as it does with `CoreUpdate` today.
2. The store produces the stream: `EventsSince(after)` yields every event
   after a seq in order, from the tables alone. The tables are the
   projection side of the boundary; their one obligation is to reproduce
   the stream.
3. One endpoint and one client reducer. A browser connects with the last
   seq it processed and receives events from there. A bootstrap is a
   compacted subsequence of the same events, so the browser cannot tell a
   bootstrap from a catch-up and has no snapshot code path.
4. Every table the primary writes under Commit is an entity type in the
   log, including sessions, Nix store resets, and keyslots. Field-level
   visibility is code, not schema: one strip function per client class.
5. Observed statuses are latest-only entities in the same log. The
   report's `updated_at` is the producing node's clock, used to drop stale
   reports when recording; it is not a substitute for the seq.
6. Optimistic concurrency tokens are seqs.
7. Every write the model considers real is a visible diff of the entity,
   so the no-op rule from phase 1 extends to deployments and assets.
   Restart becomes a diff. An asset upload with unchanged content becomes
   a no-op.

### Settled decisions

1. **Three mutation messages, fake oneof.** `CoreMutation` holds exactly one
   of `CreateMutation`, `UpdateMutation`, `DeleteMutation`. Create and update
   carry `entity_type`, `entity_id`, and a `CoreEntity` payload with one field
   per entity; delete carries type and id. `entity_type` is redundant with
   the set `CoreEntity` field on create and update; the decoder checks they
   agree. Entity ids are `int64` because authz ids already are. Session ids
   are strings today and become the row's autoincrement id on the wire, with
   the token id inside the payload.
2. **Commit is unchanged; `mutate` builds the event.** `Commit(ctx,
   preLockValidate, mutate func(*pq.Queries, int64) (*Update, error))` keeps
   its shape with `state.Update = apigen.CoreWriteUpdate`. `mutate` returns
   the event for the rows it wrote, with `time` and `actor` set from the
   `now` and `author` it stamped on those rows, and `Commit` sets `seq`.
   An update with no mutations consumes no seq and publishes nothing, as
   today. `UpdateTrigger`s append to the same update. There is no read-back
   and no runtime check: each `mutate` is responsible for returning exactly
   the mutations it wrote, and that correctness comes from code review, as
   it does for `CoreUpdate` now.
3. **One time and one author per commit.** `EventsSince` rebuilds the
   envelope from the rows, so every row written in one commit carries the
   `event_time` and `author` that the returned event carries. A `mutate`
   reads the clock once and threads that value through every insert,
   including a second domain function it calls (evict writes a keyslot
   delete and a node event; the reference rewrite writes a config row and
   deployment rows). Sites that call `time.Now()` per row today
   (`nodes/spaces.go`, `nodes/nodes.go`, `secrets/store.go`) change to the
   shared value. Stale observed reports already land with `global_seq = 0`
   and never appear in either the live or the replayed stream.
4. **Entity payloads carry facts, not envelope meta.** `id`, `version`,
   `seq`, `event_id`, `author`, `event_time`, and `deleted` leave every
   entity message; the envelope and the mutation kind carry them. Three
   things stay on the payload because they are facts a consumer cannot
   recompute once history is compacted: `value_version` on secrets, configs,
   and assets (`ValueRef` pins it), `spec_version` on deployments (instances
   pin it, logs and metrics key on it), and `created_time`. Consumers wrap
   the entity with the envelope on their own side (Go and JS) and add
   whatever projections they need. `DerivedUpdate` and
   `DeploymentVersionLabel` from the earlier draft are gone: the ordinal is
   a field, symmetric with `value_version`.
5. **Compaction drops mutations and never rewrites envelopes.** A bootstrap
   is a subsequence of the real stream: each retained mutation stays inside
   its original event with its real seq, time, and actor, and events left
   with no mutations are omitted. A mutation whose create was dropped is
   emitted as a create. Retention per type, which is today's `snapshot.go`
   rule expressed as mutations:
   - Secrets, configs, assets: every mutation of every live entity.
   - Deployments: the latest mutation and every mutation whose
     `spec_version` a live scheduled instance pins. A deleted deployment
     that a live instance still pins ships the pinned mutation and the
     delete.
   - Scheduled instances: non-final plus the latest final per ordinal
     without a live instance, as today.
   - Every other type, including the two status types: the latest mutation
     of each live entity.
   - Deleted entities are absent except for the pinned deployment case.
   Rows with `global_seq = 0` (migrated before their table was event
   shaped, and stale observed reports, which compaction never retains) form
   one genesis event at seq 0 with time 0 and actor 0, in table then row id
   order.
6. **Bootstrap and catch-up are one endpoint.** The request carries
   `after_seq`. The server replays exactly when `after_seq` is at or above
   the retention floor and no visibility-affecting event exists for this
   client in `(after_seq, now]`; otherwise it sends `reset = true` followed
   by the compacted bootstrap, then live events. The floor is 0 until
   observed history is ever pruned, so today every reconnect replays
   exactly. A client below the floor later gets the compacted form and
   nothing else changes for it. The client clears its tree on `reset` and
   otherwise folds; it never knows which form it received.
7. **Visibility is per entity, by current space, plus a strip.** Entity
   visibility moves to the entity's current space: a viewer who can see the
   entity sees all of its retained mutations. This is a semantic change from
   today's per-row space test and stays in the open items for confirmation.
   Field visibility is one function per client class in code, with no
   cleanproto option: `browserEntity(e CoreEntity) CoreEntity` clears the
   secret's `smk_version`, `ciphertext`, and `nonce`, the user's credential
   blob, both sessions' `token_hash`, the system config's
   `master_password_hash`, and drops keyslot mutations entirely. The browser
   sees those fields as never set. A test asserts every field the row types
   hold but the browser must not see is cleared. Internal consumers and a
   future replica are the unstripped class.
8. **Visibility resets come from the log.** Live: a grant mutation for the
   connected user, any template, global rule, or space mutation, a node
   allow-list change, an entity space move, or a network policy whose
   visibility flips schedules `reset` plus a bootstrap after the 200 ms
   debounce, as `needsReset` does today. Reconnect: the same predicate over
   `EventsSince(after_seq)` decides between exact replay and reset, so the
   rule is written once against mutations.
9. **Observed statuses are latest-only entities.** `scheduled_instance_status`
   and `node_status_log` already carry `global_seq`, are appended under
   Commit, and are keyed by `(id, updated_at)`. A recorded report is an
   `UpdateMutation` of a `ScheduledInstanceStatus` or `NodeStatus` keyed by
   the instance or node id. `updated_at` stays inside the payload as the
   producer's clock; `RecordStatus` already refuses to publish a report older
   than the last recorded one, so consumers fold by seq alone and the
   browser's clock merge in `deploymentMerge.js` goes. A cleared status is an
   update with an empty payload, as today.
10. **Concurrency tokens are seqs, checked as an upper bound.**
    `expected_version` on deployment update, node evict, enrollment accept,
    and network policy write becomes `expected_seq`; `referencing_deployments`
    on config and secret set becomes `{deployment_id, expected_seq}`. The
    server rejects when the entity's newest row has `global_seq` greater
    than `expected_seq`; equal or older passes. Zero means no check. The
    client sends the seq of the entity's last retained mutation as it held
    it when the form opened, which under decision 5 is the real seq. Legacy
    rows at seq 0 accept any token. `DeploymentSpecVersionRef` goes.
11. **Secret reveal addresses a value by `ValueRef`.** `SecretRevealRequest`
    takes `{secret_id, version}`. The handler resolves the version to the row.
12. **Restart is a diff.** `Scheduling` gains `int32 generation = 3`.
    `RestartUpdate` increments it. The scheduler keeps detecting a restart
    through the `spec_version` bump it already sees, so nothing changes in
    `scheduler.go` or on the worker. The browser's `deploymentRestartEvent`
    compares `scheduling.generation` between consecutive mutations. With
    that, `BuildDeploymentUpdateEvent` rejects a write whose entity equals
    the previous one.
13. **Asset upload with unchanged content is a no-op.** Same rule as config:
    success, current state returned, no row, no seq. Content is compared by
    `sha256` before the identity Commit, so the large-asset file write is
    skipped too.
14. **Sidecars stay replace-style messages.** Secrets status, backup status,
    and ingress diagnostics are not facts in the log. They ride the same
    connection as their own message fields, replacing the previous value,
    without a seq.
15. **The worker is untouched.** `CoreUpdate` is not referenced under
    `app/secondary`, `lib`, or `clusterhandler`; the scheduled-instance feed
    is `[]ScheduledInstanceState` and stays. The cluster protocol version does
    not change. Moving internal subscribers (scheduler, netmap publisher) onto
    the unstripped event is possible afterwards and is not part of this
    phase.
16. **New endpoint beside the old one, then the old one goes.** The event
    stream lands as `/v1/global/event-stream` while `/v1/global/state-stream`
    and `/v1/global/snapshot` keep serving `CoreUpdate`. The SPA moves in one
    release, then the old endpoint, `CoreUpdate`, `Snapshot`, and the event
    envelope messages leave the public API. The stream is protobuf only; a
    tab left open across that upgrade reconnects, fails to decode, and
    reloads. The reload on decode failure is added in this phase because it
    does not exist today.
17. **Entity types cover every committed table.** Beyond the seventeen in
    the draft: agent sessions, user sessions, Nix store resets, and secret
    keyslots. Keyslots are in the log for a replica and never reach the
    browser class. Authz templates and global rules are per-entity mutations;
    the `AuthzRuleTemplateList` and `AuthzGlobalRuleList` replacement
    wrappers go. System config is a singleton with `entity_id = 1`; its
    revision id stays in SQL as the projection's key and
    `SystemConfigVersion` goes.
18. **Tables are projection side.** The SQL shape is whatever makes reads
    and `EventsSince` cheap. An `<entity>_event_log` need not
    map one to one to its entity, and extra materialised tables such as
    `deployment_spec_versions (deployment_id, spec_version, global_seq)` are
    allowed. The one rule: replaying `EventsSince(0)` through the reference
    reducer equals the live tables, and that is the test.

### Wire

`api-contract/model/events.proto` (started 2026-09-29):

```proto
message CoreWriteUpdate {
  int64 seq = 1;
  int64 time = 2;                  // epoch ms, equal to event_time on every row of the commit
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
  CORE_ENTITY_AGENT_SESSION = 18;
  CORE_ENTITY_USER_SESSION = 19;
  CORE_ENTITY_NIX_STORE_RESET = 20;
  CORE_ENTITY_SECRET_KEYSLOT = 21;   // replica only; never in the browser class
}

message CoreEntity {                 // exactly one set; field numbers equal CoreEntityType
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
  AgentSession agent_session = 18;
  UserSession user_session = 19;
  NixStoreReset nix_store_reset = 20;
  SecretKeyslot secret_keyslot = 21;
}

message EventStreamRequest {
  int64 after_seq = 1;               // 0 = bootstrap
}

message EventStreamMsg {
  CoreWriteUpdate event = 1;
  bool reset = 2;                    // clear the tree; a bootstrap follows
  bool heartbeat = 3;
  BackupStatus backup_status = 4;
  IngressDiagnosticList ingress_diagnostics = 5;
  SecretsStatusResponse secrets_status = 6;
}
```

Entity messages become pure definitions plus the facts in decision 4.
`Secret` gains `value_version`, `smk_version`, `ciphertext`, `nonce`, and
`created_time`; the last four exist on the row today and the browser class
never sees the middle three. `Config` and `Asset` gain `value_version` and
`created_time`. `Deployment` gains `spec_version` and `created_time`.
`Space`, `User`, `ValueDirectory`, and `AssetDirectory` lose `id`, `author`,
`updated_at`, and `deleted`. `AuthzRuleTemplate` and `AuthzGlobalRule` are
the existing record messages without `id` and `deleted`. `SystemConfig` is
the existing settings message. `AgentSession` and `UserSession` gain
`token_hash` for the unstripped class. `NixStoreReset` and `SecretKeyslot`
are new messages over the two rows. `Scheduling` gains `generation`.
`SecretRevealRequest` becomes `{secret_id, version}`. The event envelope
messages (`DeploymentEvent`, `NodeEvent`, `ScheduledInstanceEvent`,
`NetworkPolicyEvent`, `AuthzGrantEvent`, `SecretEvent`, `ConfigEvent`,
`AssetEvent`), `CoreUpdate`, `Snapshot`, and `StateStreamMsg` leave the
public API at step 6. `DeploymentEvent` stays defined because
`ScheduledInstanceState.config` carries it to the worker over the cluster
protocol.

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
seq of the mutation it last saw, and the server rejects only a newer row.

### Storage

Every event table gains an index `idx_<table>_seq (global_seq, id)`. None
exists today, and `EventsSince` is a cursor over it.

`pq` gains two readers over the same per-table row functions:

- `EventsSince(ctx, q, after) iter.Seq2[*CoreWriteUpdate, error]`: a k-way
  merge of per-table cursors on `global_seq > after ORDER BY global_seq, id`,
  grouped per seq. `after < 0` includes the genesis event. Runs inside one
  read transaction so the result is a consistent prefix; the caller
  subscribes under the write mutex before reading so no event is missed
  between the cursor's end and the first live event.
- `Bootstrap(ctx, q, retain) iter.Seq2[*CoreWriteUpdate, error]`: the
  compacted subsequence of decision 5, from the existing live and pinned
  queries plus the value history listings, emitted in seq order with the
  original envelopes. `retain` is the visibility predicate per entity.

Each per-table row function returns `(pq.EventMeta, kind, entity_id,
CoreEntity)` from one row. The row-to-entity mapping it uses is the same
function the domain calls when it builds the mutation it returns from
`mutate`, so the live and replayed payloads come from one converter even
though the envelopes are assembled in two places. The read converters that
build today's `*Event` envelopes go when the envelopes go. Mutation order
inside a replayed event is entity type, then row id; a `mutate` appends in
write order, and no consumer depends on the order within one event.

No column changes beyond the indexes:
`event_id`, per-event `version`, the facet counters, and `created_time`
stay in SQL, the first two as the projection's keys and the last two as
facts read into the payload. Rows copied from pre-conversion tables carry
seq 0 and are the genesis event until their next write.

`deployment_event_log` writes reject an unchanged entity once `generation`
exists (decision 12). `asset_event_log` writes reject unchanged content
(decision 13). Both are checks in the domain insert closure, in the same
place phase 1 put the config comparison.

### Backend

**`state` package.** `state.Update` becomes `apigen.CoreWriteUpdate` and
`Commit` is otherwise untouched. A small builder keeps producers from
hand-assembling the fake oneof:

```go
func (u *Update) Create(t apigen.CoreEntityType, id int64, e apigen.CoreEntity)
func (u *Update) Update(t apigen.CoreEntityType, id int64, e apigen.CoreEntity)
func (u *Update) Delete(t apigen.CoreEntityType, id int64)
```

`IsEmpty` is `len(Mutations) == 0`. `notifyLocked` publishes the
`*CoreWriteUpdate` and `Subscribe`'s `project` receives it. `EventsSince` and `Bootstrap` are
methods on `Service` that take the read under the mutex when the caller
asks for a subscription in the same call. `statetest` helpers change with it.
`BuildSnapshot` becomes a test-only reducer output until step 6, then goes.

**Producers.** Every domain function that returns a `*state.Update` today
returns the new one through the builder. From the inventory: `deployments/write.go` and `deployments.go`,
`nodes/nodes.go`, `nodes/enrollment.go`, `nodes/evict.go`,
`nodes/spaces.go`, `networkpolicies/networkpolicies.go`,
`secrets/store.go`, `secrets/secret_keyslots` writers, `values/configs.go`,
`values/references.go`, `values/directories.go`, `assets/db_assets.go`,
`assets/db_directories.go`, `authz/store.go`, `users/users.go`,
`systemconfig/store.go`, `nixstores/nixstores.go`,
`scheduledinstances/status.go`, `agentsessions`, the user session writers,
`scheduler/instances.go`, `scheduler/scheduler.go`, and
`netmappublisher/publisher.go`. The rule per site: an insert that creates
an entity calls `Create`, one that appends a version calls `Update`, a
tombstone or row delete calls `Delete`, with the payload from the same
row-to-entity converter `EventsSince` uses; the event's `time` and `actor`
are the `now` and `author` the site stamped on its rows. Status writers
call `Update` with the status entity. `pq.EventMeta` stays as it is.

**Legacy stream during the transition.** Until step 6 the old handler still
serves `CoreUpdate`. Its `project` builds that from the existing `*AtSeq`
queries for the published seq, adding any that are missing, instead of
from the update; the domain converts each site once. The shim and the
`*AtSeq` queries go with the old surface.

**Event stream handler.** `PostV1GlobalEventStream(ctx, req)`: subscribe
under the mutex; if `after_seq` is below the floor or the reset predicate
holds over `EventsSince(after_seq)`, yield `reset` then `Bootstrap` with the
connection's visibility; otherwise yield `EventsSince(after_seq)`; then live
events, each passed through the same visibility. `visibleEvent(ctx, e)`
filters mutations by entity visibility, applies `browserEntity`, and drops
an event left empty. The debounce and 200 ms reset scheduling move over
from `state_stream.go` unchanged. Sidecars attach as today.

**Visibility.** `visibility.go` reduces to `visibleMutations(ctx,
[]CoreMutation) []CoreMutation` keyed by `entity_type`, using the
current-space map per entity for deployments, secrets, configs, and assets,
owner filtering for sessions, and instance or node visibility for statuses;
`needsReset` reads the mutation list; `browserEntity` is the strip;
`snapshotUpdate` and `updateSnapshot` go at step 6.

**Concurrency checks.** The five request sites compare the supplied
`expected_seq` with the entity's newest `global_seq`, read inside mutate,
rejecting only a newer row. The deployment reference rewrite in
`values/references.go` compares per deployment.

**Secret reveal.** `secrets.RevealByRef(secretID, version)` resolves the
row through the existing version listing and calls the current open path.

**Agent endpoints.** `GET /v1/global/snapshot` is replaced by a one-shot
`GET /v1/global/events?after_seq=` returning the same bootstrap as a list.
`webuihandler/agent_instructions.md` sections that read `deployment_events`
and `version` are rewritten against mutations, `spec_version`, and
`expected_seq`, including the note that the deployment check is no longer
"current version plus one".

### Frontend

`state/tree.js` becomes a fold over `CoreMutation` fed by
`/v1/global/event-stream`:

- One `Map<id, entry>` per entity type. Value entities and deployments keep
  `history: [{seq, time, actor, entity}]`; every other type keeps the latest
  entity plus `seq`, `time`, and `actor` of the last mutation. Deployment
  history is pruned to latest plus versions a live instance references, as
  today, and a delete mutation marks the entry deleted without dropping
  pinned history.
- `reset` clears every map and the seq; sidecars survive it.
- Status entities fold like any other latest-only entity; the clock merge
  in `deploymentMerge.js` goes.
- Seq gating is per message: an event at or below the current seq is
  dropped; a gap forces a reconnect with `after_seq` set to the last seq
  applied, as the overflow path does today.
- On reconnect the client sends its last applied seq and folds whatever
  arrives; it does not distinguish exact replay from bootstrap.

`state/derive.js` view models keep their output shapes where the pages
read them (`deleted`, `createdTime`, `author`, `versions`) and compute them
from the fold: `deleted` from the delete mutation, `createdTime` from the
payload, `author` from the envelope, version lists from `value_version`
and `spec_version` on the retained history. `lib/deployment.js`'s
`deploymentRestartEvent` compares `scheduling.generation`.
`pages/secrets.js` reveals by `{secretId, version}`. The `expectedVersion`
and `specVersion` call sites in the concurrency table send `expectedSeq`
from the entry's last-mutation seq as held when the form opened.
`capi/model.js` regenerates. The stream reader in `capi/capi.js` reloads
the page on a decode failure of the first message after connect.

`tree.test.js` fixtures move to mutations; the round-trip test (fold a
bootstrap, apply events, compare with the fold of every event since 0) is
added on the browser side too, against fixtures exported from the Go
replay test.

### Docs

- `docs/engineering/api.md`: the global state section becomes the event
  stream, the Commit section describes the returned event and the one
  time and author rule, the append-only tables section gains the seq
  index and the stream rule, the
  concurrency table and the reveal request change.
- `docs/engineering/frontend.md`: the state section.
- `docs/engineering/secrets.md`: reveal by reference, the browser strip.
- `webuihandler/agent_instructions.md` as above.
- `CLAUDE.md` index line for this plan.

### Steps

Each step builds and passes tests on its own. Steps 1 to 4 ship nothing to
the browser and land one at a time; step 5 is the browser move and lands
with 6 and 7 in one release.

1. **One clock per commit, restart as diff, asset no-op.** The per-row
   `time.Now()` calls become one read per `mutate` threaded through every
   insert, the seq indexes, and the `Scheduling.generation` restart diff
   with the unchanged-entity and unchanged-asset rejections. No wire
   change. Tests: every row of a multi-table commit carries one time and
   author; restart bumps generation and version; an identical deployment
   or asset write returns the current state and no seq.
2. **Builder, producers, and replay.** `state.Update` becomes
   `CoreWriteUpdate`, every producer converted through the builder, the
   legacy `CoreUpdate` shim in the old handler, the per-table row
   functions, `EventsSince`, `Bootstrap`, and the reference reducer in Go.
   Tests: for a seeded store with every entity type, folding
   `EventsSince(-1)` equals the live tables, folding `Bootstrap` equals
   folding `EventsSince(-1)` per entity type, and the events published
   while seeding equal `EventsSince` over the same range, which is what
   catches a `mutate` that omits a mutation on any path a test drives.
   `update_oracle_test.go` becomes that last comparison.
3. **Entity messages.** The payload changes in the wire section, the four
   new entity types, `browserEntity` and its completeness test, and
   `RevealByRef`. The shim keeps serving the old shapes.
4. **Event stream endpoint.** `PostV1GlobalEventStream`, the reset
   predicate over mutations, the debounce move, the sidecars, the one-shot
   agent variant, and the `expected_seq` checks with the old fields still
   accepted. Visibility tests run against this endpoint.
5. **Frontend fold.** `tree.js`, `derive.js`, `deployments.js`, the
   concurrency call sites, reveal, `reset`, reconnect with `after_seq`, and
   the reload on decode failure.
6. **Old surface removal.** `/v1/global/state-stream`, `/v1/global/snapshot`,
   `CoreUpdate`, `Snapshot`, `StateStreamMsg`, the event envelope messages,
   the list wrappers, `SystemConfigVersion`, the `deleted` flags, the
   `expected_version` fields, the read converters, the `*AtSeq` queries and
   the shim, `snapshotUpdate`, `updateSnapshot`, and `BuildSnapshot`.
   Regenerate both sides.
7. **Docs and agent instructions.**
8. **After rollout.** Fold the schema note into the `migrations.sql` history
   note. Remove the row-id reveal path if nothing internal uses it.

### Verification

- `state` replay tests as in step 2, including a space move, a deleted
  deployment with a draining instance, a restart, a stale observed report,
  a no-op write, a commit that calls two domain functions, and rows at
  seq 0.
- `browserEntity` test: construct every entity from a row with every
  column set and assert the private fields are zero after the strip, and
  that a keyslot mutation is dropped.
- Visibility tests in `webuihandler`: a viewer gains and loses a space and
  sees `reset`; a reconnect with `after_seq` before a grant change gets
  `reset`, one after it gets exact replay; a moved secret's whole history is
  visible from its new space and none of it from the old one; pinned
  versions of a deleted deployment reach a viewer who can see the instance;
  a session reaches only its owner.
- Concurrency tests: a newer row rejects on all five sites; an equal or
  older seq passes; zero passes; a legacy row at seq 0 accepts its first
  write.
- Frontend `tree.test.js` round trip against the Go fixtures, including a
  `reset` mid-stream.
- Manual on a dev primary: open two tabs, restart a deployment, move a
  secret between spaces, evict a node, edit settings, add a global rule,
  and watch both tabs converge; kill the connection and confirm the tab
  resumes from its seq without a bootstrap; leave a tab open across a
  restart of the primary with the new build and confirm it reloads.
- The e2e harness drives deployments and node flows through the SPA and
  covers those paths end to end; the value, authz, settings, and restart
  paths rely on the unit coverage above.

### Open items

- Decision 7 changes what a viewer with access to only the current space
  can see of a moved secret, config, or asset: the whole history instead of
  the rows written in that space. Confirm before step 4.
- A retention floor for observed status history. Nothing prunes today;
  when something does, the floor is the oldest retained seq and clients
  below it get the compacted form (decision 6). No work in this phase.
- Delta encoding (`changed_tags` plus a partial entity) as a transport
  option for large deployment specs. It composes with the strip only if
  applied after it, against the client's last stripped state, so it is a
  per-connection encoder in the handler and never a stored shape. Not in
  this phase.
- Moving the scheduler and netmap publisher onto the unstripped event
  stream instead of `CoreUpdate`-derived feeds, once the old surface is gone.
- `User` as a document with typed passkeys instead of the opaque blob, from
  the 2a open items; the entity message in step 3 is where it would land.
