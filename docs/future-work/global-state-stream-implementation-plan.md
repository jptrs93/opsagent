# Global State Stream Implementation Plan

Status: Parts A and B, including the merged commit and transactional scheduler
refinement, implemented and validated (2026-09-09). The full Go suite, relevant
race tests, 138 frontend tests, and frontend build pass. The latest full E2E
passed in 22m1s: browser flow 18m7s, WireGuard transport passed, and kernel
enforcement 2m16s. The Part A E2E gate passed in 20m57s before Part B started.
The Part A sections below record the original implementation; Part B
supersedes its collector and wire shape. The latest refinement merges primary
writes into one transaction boundary while preserving separate core and
observed publications and independently owned sidecars.

Scope: the primary's state store publish path, the primary → browser state
stream, the frontend's state model, and the node model. The primary → secondary
stream (`MsgToSecondary`) keeps its current shape; the design anticipates moving
it onto the same `Update` message later but that is a separate plan.

## Context

### The model

The cluster's live state is one mutable tree. Collections of uniformly typed
entities hang off the root, keyed by integer id:

```
live_state {
  global_seq
  cluster            { ula_prefix, settings, keyslots, system_secrets }
  spaces             { <id>: ... }
  nodes              { <id>: ... }
  deployments        { <id>: { <version>: DeploymentEvent } }
  scheduled_instances{ <id>: ... }
  secrets, configs, assets, value_directories, asset_directories
  network_policies
  access             { users, rule_templates, grants, global_rules }
  observed           { instance_status { <instance_id> }, node_status { <node_id> } }
}
```

The tree evolves as a sequenced log. `global_seq` is `version(live_state)`:
every state-changing transaction allocates the next value and stamps the rows
it writes. `version(path)` for any sub-tree is the number of transactions that
changed that sub-tree; the per-entity `version` column and the facet columns
(`spec_version`, `space_version`, `name_version`, `value_version`) are this
function for their paths. A transaction is `(mutate, filter)`: read
`version(filter)`, stage the mutation, and commit under the writer freeze only
if `version(filter)` is unchanged.

The primary implements this already in `backend/storage/primarydb`: one
`global_seq` counter, one append-only `<entity>_event_log` per collection with
`(entity_id, version)` unique and `event_type` create/update/delete, delete as a
terminal tombstone row. See `docs/engineering/api.md` and
`docs/future-work/deployment-def-split-implementation-plan.md` for the
deployment envelope/value shape, which every other collection's event
follows under the naming standard below.

### What is misaligned

The store's publish path and the browser stream predate the log and do not
reflect it.

1. `State` (`api-contract/model_global_operations.proto`) is a hand-rolled
   oneof of 36 fields: a snapshot field and an update field per collection.
   `GlobalState` is a second snapshot shape for the same data.
2. Each collection has its own `pubsubu.PubSub` in `state.Service` (17 fields,
   16 subscribe methods, 34 notify sites). The stream handler
   (`backend/app/primary/webuihandler/state_stream.go`, 396 lines) selects
   over one channel per collection. Ordering across collections is not
   preserved: a deployment update and the instance rescheduling from the same
   transaction can reach the browser in either order. The handler's `depSpace`
   side map exists to join instance updates, which carry no space, to whatever
   deployment state the handler holds.
3. No message carries `global_seq`. The browser cannot state what version its
   view is at, cannot resume, and cannot detect a dropped message. `pubsubu`
   drops on a full channel with a log warning.
4. Three delivery regimes: per-item updates; whole-collection re-emit on any
   change (network policies, authz collections, ingress diagnostics, secrets
   status, system config); a full rebuild of the filtered state on any authz
   change.
5. The scheduled instance publish unit is `ScheduledInstanceState`: the
   instance, its full pinned `Deployment`, and its status. Every status tick
   from a worker republishes the pinned spec to every subscriber. The frontend
   joins instances to deployments itself and computes `pinnedConfig` from the
   embedded copy; no component reads it.
6. The frontend re-implements store reducers. The "latest finalized incarnation
   per ordinal, superseded by any newer instance" rule exists in Go
   (`retainFinalizedLocked`) and in JavaScript (`applyScheduledInstanceUpdate`).
   The Go side is `latestFinalCache`, a memo of the
   `ListLatestScheduledInstancePerOrdinal` query with its own eviction rules,
   held next to the live instance map although nothing that reconciles reads
   it. Its readers are the browser snapshot, run report, prepare output and
   log requests, delete validation, and a late-status-write patch path.
7. Four tombstone conventions: `event_type = DELETE` (deployment), `deleted_at`
   (secret, config, asset), `deleted` bool (space, directories, network policy,
   authz template), `FINALIZED` target (instance).
8. Nodes are on the wire as three shapes: `ClusterNode` (the authored row
   without its envelope, no version), `ClusterNodeStatus` (observed), and
   `EnrollmentRequestStatus` (a join of both). `enrollment_pending` lives in
   `node_statuses` but is a lifecycle fact; the row conversion overrides a
   member's status when it is set. `host_addresses` arrives in the cluster
   hello and stays in `node_statuses`, unversioned, yet the ingress plan reads
   it, so the plan at seq N is not a function of state at N. `wg_public_key`
   arrives in the same hello and is promoted to a node event.
9. `scheduled_instances.go` allocates a `global_seq` per row inside one
   transaction, so seq does not identify a transaction.

## Goals

- One publish path: a transaction commits and publishes one `Update` carrying
  every event it appended, stamped with the transaction's seq. Multi-object
  transactions are expressed directly.
- One wire contract for the browser: a `Snapshot` at seq S, then `Update`
  messages in seq order. Both are built from the same per-collection event
  messages so the frontend has one reducer per collection.
- The frontend holds the state tree and derives every view from it. No
  business rule is duplicated between Go and JavaScript.
- Visibility is a filter applied per event, plus one resync primitive.
- The node model is one event shape with the value split by author, and the
  enrollment flow produces no trailing events after accept.
- Reported node facts that feed derived artifacts are in the log.

## Non-goals

- Moving spaces, directories, cluster settings, users, or key material into
  seq-stamped event logs. They are carried with replace semantics and no
  version. Follow-on work.
- Resume from seq (`from_seq` replay from the event logs). The contract leaves
  room for it; the first implementation always sends a `Snapshot` on connect.
  When built, resume replays authored events and observation rows with
  `seq > from_seq`; every observation row is stamped with the global seq of
  the commit that published it.
- Coalescing updates for slow subscribers.
- Changing `MsgToSecondary` or `local_scheduled_instance_cache`. The worker
  keeps receiving `ScheduledInstanceState` as its assignment record.
- Changing value refs (`secret_version_id`, `config_version_id`,
  `asset_version_id`) from log row ids to `(entity_id, value_version)`.
  `event_id` carries the row id on every event so the current refs keep
  working.
- Facet-level visibility on the wire (hiding `secrets[id].value` while showing
  `secrets[id].fs`). The secret wire shape carries metadata only today, which
  is sufficient.

## Treatment of `state.Service`

The plan reworks the read and publish side of the store and the node model,
and keeps the write side's structure with one new seam.

Reworked: the publish path (17 pubsubs and 34 notify sites become one
`Update` stream fed by a per-transaction collector); the in-memory instance
state (`latestFinalCache` removed, `Scheduled` is the live map only, the
retained-final view is a query); the node model end to end; the read model
(`BuildSnapshot` is the one place that materialises the tree at a seq); the
seq-per-transaction invariant; pubsub overflow behaviour.

Kept: the concurrency model (`Mu` as the writer freeze, reads under `Mu`,
`q.Tx` for multi-statement writes); every per-collection write method, which
changes only from calling notify to adding rows to the collector; the conv
layer per collection, extended to populate `seq` and `event_id`; the nine
internal subscribers, through typed adapters.

Since removed (September 2026): the primary's `instancecache` embedding and
`deploymentCache`, the `*Locked` writer variants and `GlobalLock`, the
store-owned scheduler hook, and every domain method on `state.Service`. Every
writer is one `Commit(ctx, inlockValidate, mutate)` call: `Commit` takes `Mu`,
opens the write transaction, runs the optional `pq.Validator` and the mutate
callback on the transaction-bound `Queries`, then runs every registered
`UpdateTrigger` (the scheduler registers one) on the resulting update before
saving the sequence and publishing. Work that used to happen under the lock,
such as reference-locality checks and secret sealing, happens inside those
callbacks. `state.Service` now exposes only `Commit`, `RegisterUpdateTrigger`,
`Queries`, `SubscribeUpdates`, the `Subscribe`/`Project` helpers,
`BuildSnapshot`, and the scheduled-instance projection the `OperatorStore`
interface requires. Domain logic lives in `app/primary/domain/<name>` packages
(`deployments`, `nodes`, `scheduler`, `assets`, `secrets`, `values`,
`authz`, `config`, `pki`, `acmeissue`) as functions over the store, and
handlers read through `pq.Queries` directly. Nothing under `lib/` imports the
primary store, so the secondary links none of it. The store holds no in-memory
state and `pq` has no caches (the sqlc-generated layer was folded into
hand-written per-entity files); the scheduled-instance view is the joined
instance, pinned deployment and latest-status query
(`ListLiveScheduledInstanceStates`, `GetScheduledInstanceState`). Observed
writes consume the global seq like authored writes, and the store publishes one
`CoreUpdate` per commit carrying authored events and observed statuses.
The store has one subscription primitive, `Subscribe(store, read, project)`:
`read` runs under the write lock and returns the snapshot, and
`project(update) (T, bool)` runs after every commit, still under the lock, and
returns the value to send and whether to send it. The store passes no queries;
a projection that needs them captures `store.Queries()`. Sends are non-blocking;
a subscriber whose channel is full is closed and dropped, and consumers
resubscribe and refetch. The raw update feed sends the `CoreUpdate` itself and
the scheduled-instance feed sends one `[]ScheduledInstanceState` batch per
commit, which consumers iterate. The secondary's in-memory store keeps an
equivalent private subscriber list delivering single-element batches.

Deferred: the transaction formalisation, `(mutate, filter)` with
`version(filter)` checked at commit. The freeze makes internal conflicts
impossible today and expected-version checks exist at four API edges. The
collector is the seam a generic write API would hang off later: check the
expected versions under `Mu`, run the mutation, publish what the transaction
appended. The one commitment this plan makes on the write side is that commit
and publish are the same act, and every write reaches it through the
collector.

## Target design

### Wire contract

```proto
// Sent once on connect, and again whenever the subscriber must resync.
message Snapshot {
  int64 seq = 1;
  repeated DeploymentEvent deployment_events = 2;          // see "Snapshot contents"
  repeated ScheduledInstanceEvent scheduled_instance_events = 3;
  repeated ScheduledInstanceStatus instance_statuses = 4;  // observed
  repeated NodeEvent node_events = 5;
  repeated NodeStatus node_statuses = 6;                    // observed
  repeated SecretEvent secret_events = 7;                  // full history per live secret
  repeated ConfigEvent config_events = 8;
  repeated AssetEvent asset_events = 9;
  repeated ValueDirectory value_directories = 10;
  repeated AssetDirectory asset_directories = 11;
  repeated Space spaces = 12;
  repeated NetworkPolicyEvent network_policy_events = 13;
  repeated User users = 14;
  repeated AgentSession agent_sessions = 15;
  repeated AuthzRuleTemplateRecord authz_rule_templates = 16;
  repeated AuthzGrantRecord authz_grants = 17;
  repeated AuthzGlobalRuleRecord authz_global_rules = 18;
  SecretsStatusResponse secrets_status = 19;
  BackupStatus backup_status = 20;
  SystemConfigVersion system_config = 21;
  IngressDiagnosticList ingress_diagnostics = 22;
}

// One transaction, or one batch of observed changes.
message Update {
  int64 seq = 1;
  bool heartbeat = 2;
  repeated DeploymentEvent deployment_events = 3;
  repeated ScheduledInstanceEvent scheduled_instance_events = 4;
  repeated ScheduledInstanceStatus instance_statuses = 5;  // observed
  repeated NodeEvent node_events = 6;
  repeated NodeStatus node_statuses = 7;                    // observed
  repeated SecretEvent secret_events = 8;
  repeated ConfigEvent config_events = 9;
  repeated AssetEvent asset_events = 10;
  repeated ValueDirectory value_directories = 11;
  repeated AssetDirectory asset_directories = 12;
  repeated Space spaces = 13;
  repeated NetworkPolicyEvent network_policy_events = 14;
  repeated User users = 15;
  repeated AgentSession agent_sessions = 16;
  // Replace-whole-collection fields. Present only when changed.
  AuthzRuleTemplateList authz_rule_templates = 17;
  AuthzGrantList authz_grants = 18;
  AuthzGlobalRuleList authz_global_rules = 19;
  SecretsStatusResponse secrets_status = 20;
  BackupStatus backup_status = 21;
  SystemConfigVersion system_config = 22;
  IngressDiagnosticList ingress_diagnostics = 23;
}

message StateStreamMsg {
  Snapshot snapshot = 1;
  Update update = 2;
}
```

`PostV1GlobalStateStream` returns `stream StateStreamMsg`. The first message
is a `Snapshot`. `GetV1GlobalState` becomes `GetV1GlobalSnapshot` at
`/v1/global/snapshot` and returns the same `Snapshot`, filtered by the same
visibility function. `State` and `GlobalState` are deleted. Agents and the
browser read one contract; the agent instructions
(`webuihandler/agent_instructions.md`) and `agent_sessions_test.go` are
rewritten against it.

Field numbering is not load-bearing. The frontend is embedded in the same
binary, so the old messages are deleted rather than bridged.

### Naming and envelope standard

Every event-logged collection is carried as one event message with one shape:
an envelope of log metadata and a `value` holding the entity as its author
writes it. The entity type is named for the entity (`Deployment`, `Node`,
`Secret`); the event type is the entity name plus `Event`. The storage rows
are flat columns; that is an implementation detail the conv layer hides.

```proto
enum EventType {
  EVENT_TYPE_UNSPECIFIED = 0;
  EVENT_TYPE_CREATE = 1;   // stored ints match today's AuthzVerb values
  EVENT_TYPE_UPDATE = 2;
  EVENT_TYPE_DELETE = 3;
}

message DeploymentEvent {
  int32 deployment_id = 1;     // entity id; never the row id
  int32 version = 13;          // version(deployments[id]); bumps on every event
  int64 seq = 19;              // global_seq of the transaction that wrote it
  int64 event_id = 20;         // log row id; the pin target for value refs
  int32 author = 6;
  EventType event_type = 15;
  int64 created_time = 16;     // first event's event_time, copied forward
  int64 event_time = 17;
  int32 spec_version = 7;      // facet versions: version(value.<facet>)
  int32 space_version = 12;
  int32 name_version = 14;
  Deployment value = 18;
}
```

Rules:

- Envelope fields, in this order on every event: `<entity>_id`, `version`,
  `seq`, `event_id`, `author`, `event_type`, `created_time`, `event_time`,
  then the facet versions, then `value`.
- `<entity>_id` is always the entity id. `event_id` is the log row id. Both
  are on every event; `event_id` is what value refs pin today
  (`secret_version_id`, `config_version_id`, `asset_version_id` are row ids
  of value-changing events). Changing those refs to pin
  `(entity_id, value_version)` is follow-on work.
- `seq` on the wire, `global_seq` in SQL. `seq` matches `Update.seq` and
  `Snapshot.seq`; `max(seq)` over any set of events is that set's version.
- Facet versions are named after the path they version inside `value`:
  `spec_version`, `name_version`, `space_version`, `value_version`. The
  deployment table's `space_assignment_version` column keeps its name and
  maps to `space_version`.
- One shared `EventType`. The per-collection enums (`DeploymentEventType`)
  are removed.
- No `*_changed` flags on the wire. A client holding history derives which
  facet an event changed by comparing facet versions with the previous event.
- `value` is the visible projection of the entity. `SecretEvent.value` is
  `Secret { fs, space_id }` with no ciphertext.

Renames this implies, all binary-compatible because field numbers do not
change: `Deployment` → `DeploymentEvent`, `DeploymentDef` → `Deployment`,
`Deployment.def` → `value`, `Deployment.id` → `deployment_id`,
`DeploymentEventType` → `EventType`. `seq` and `event_id` are new numbers. The
worker's persisted `ScheduledInstanceState` blobs and the `MsgToSecondary`
push decode unchanged. Agent-facing JSON field names change
(`def.spec` → `value.spec`); the agent instructions and their test update in
the same commit, and again in Phase 3 when `GlobalState` gives way to
`Snapshot`.

| Collection | Event | `value` | Facet versions |
|---|---|---|---|
| deployments | `DeploymentEvent` | `Deployment { node_id, space_id, name, spec }` | `spec_version`, `space_version`, `name_version` |
| scheduled_instances | `ScheduledInstanceEvent` | `ScheduledInstance { deployment_id, deployment_version, deployment_spec_version, node_id, instance_ordinal, space_id, state }` | none; add `version`, `event_time` to the row projection |
| nodes | `NodeEvent` | `Node`; see "Node model" | none |
| secrets | `SecretEvent` | `Secret { fs, space_id }` | `value_version`, `space_version` |
| configs | `ConfigEvent` | `Config { fs, space_id, value }` | `value_version`, `space_version` |
| assets | `AssetEvent` | `Asset { fs, space_id, sha256, size_bytes }` | `value_version`, `space_version` |
| network_policies | `NetworkPolicyEvent` | `NetworkPolicy { action, source, destination, ports }` | none; `deleted` is replaced by `event_type` |

The bare names `Secret`, `Config`, and `Asset` are free for the value types
because the aggregates are deleted (see "Wire contract"). The agent rule for
history is the browser's derive: the latest state of an entity is its last
event, and the pinnable version ids are the `event_id` of each event whose
`value_version` differs from the previous event's.

The aggregate `Secret`, `Config`, and `Asset` messages, each carrying the
entity's `versions` array, are deleted. The value endpoints return events
instead: list returns the latest event per entity, and create, set, generate,
rename, move, and upload return the event they appended, so a caller has the
new entity or facet version for its next `expected_version` without a second
read. A caller wanting history reads it from `Snapshot`. The browser uses
returned entity and event ids for selection and version pins; the stream
updates its shared state.

Collections without an event log (`Space`, `ValueDirectory`, `AssetDirectory`,
`User`, `AgentSession`) keep their current message and their current `deleted`
flag as the tombstone. The frontend reducer for these treats `deleted == true`
as delete. Converting them to logs is follow-on work and does not change the
stream contract.

Tombstone rule on the browser wire: an event with `event_type = DELETE` or an
item with `deleted = true` removes the entity from the client's map. The
recently-deleted deployments list uses its own endpoint and is unaffected.

### Snapshot contents

Updates carry every committed event, including tombstones. A snapshot is an
optimisation of replay: after applying the frontend reducers and retention
rules, it must equal the tree obtained by folding all intervening updates into
an earlier snapshot. Latest-event and full-history queries exclude entities
whose latest event is a delete. Deployment handling is the reducer's pinned
retention case: retain a deleted parent's pinned versions and tombstone only
while a live instance still references them. Spaces and directories use hard
deletes, so list queries already omit deleted flag-collection items.

- `deployment_events`: the latest event of every live deployment, plus every
  `(id, version)` pinned by any scheduled instance in the snapshot. A deleted
  deployment appears only through a pinned version and its tombstone while an
  instance still references it.
- `scheduled_instance_events`: every non-final instance from the live map,
  plus the latest finalized instance per ordinal that has no live instance,
  read by `ListLatestScheduledInstancePerOrdinal` at build time. There is no
  retained-final cache; see "Store: live map and the latest-final query".
- `instance_statuses`: the latest status per instance in the snapshot.
- `node_events`: the latest event per node, every lifecycle status. Enrollment
  requests are nodes.
- `secret_events`, `config_events`, `asset_events`: every event of every live
  entity, newest last. This is the version history the pickers and settings
  pages read today from `Secret.versions`, `Config.value_versions`, and
  `Asset.content_versions`.
- Singletons and replace-collections: current value.

### Sequence semantics

- `Update.seq` is the seq the transaction allocated. One transaction allocates
  exactly one seq. This is an invariant the store enforces (Phase 0).
- An `Update` carrying only observed fields (`instance_statuses`,
  `node_statuses`) does not advance seq. Its `seq` is the current `global_seq`
  at publish time.
- `Snapshot.seq` is `global_seq` read under the store lock while the snapshot
  is built.
- Seq is not contiguous per subscriber: filtering removes whole updates. Gap
  detection is the server's responsibility, not the client's.
- Client rule: keep `tree.seq`. Apply a `CoreUpdate` only if
  `update.seq > tree.seq`: replace authored entities by identity, merge its
  observed values by clock, then set `tree.seq = update.seq`. Apply a
  `Snapshot` unconditionally, replacing every collection, and set
  `tree.seq`.
- Observed rule: every observed value carries its own clock (`updated_at` HLC
  for instance statuses, `observed_at` for node statuses) and the reducer is
  last-writer-wins on that clock. Observed fields are applied regardless of
  `seq`. This makes the observed collections idempotent under duplication and
  reordering, so no per-message ordinal is needed: within a connection
  HTTP/2 ordering and close-on-overflow cover delivery, and across
  connections a `Snapshot`, or a future resume, carries the full observed
  set. `node_statuses` gains an `observed_at` column stamped by the primary.
- Client rule: apply a whole `Update` or `Snapshot` before recomputing any
  derived value.

### Store: transaction collector and one pubsub

`state.Service` gains one `pubsubu.PubSub[Update]`. Every write path that runs
`q.Tx` collects the rows it appends into an `Update` for the seq it allocated
and publishes it after commit, still under `Mu`. Observed writes
(`MustWriteScheduledInstanceStatus`, node connection and hello handling)
publish an `Update` with only observed fields.

The 17 per-collection pubsubs and their subscribe methods are removed. Internal
consumers that subscribe today (`netmappublisher`, `scheduler`, `clusterhandler`,
`logmanager`, `engine/operator.go`, `engine/runner/sweep.go`, `runtime.go`,
`netproxy/netstate.go`) keep their typed channels through thin adapters that
subscribe to the `Update` stream and project one collection. The adapters live
in `state` next to the pubsub. This keeps those nine files out of the change.

`pubsubu.Notify` must not drop. On a full channel it closes the subscriber's
channel and unsubscribes it. The stream handler treats a closed channel as
"resync required" and ends the HTTP stream; the browser reconnects and receives
a `Snapshot`. The change is in `goutil/pubsubu` (v0.22.0 pinned in
`backend/go.mod`) and needs a goutil release.

### Store: live map and the latest-final query

The live instance set (non-final instances, the tree's `scheduled_instances`
collection) is the `ListLiveScheduledInstanceStates` query; the primary keeps
no in-memory instance map. `latestFinalCache` and its maintenance
(`retainFinalizedLocked`, the eviction on new instance, the finalized branch
of the status write path) are removed. "The last incarnation of an ordinal" is
a read model, answered by query:

- `BuildSnapshot` runs `ListLatestScheduledInstancePerOrdinal`, keeps rows
  whose latest event is FINALIZED and whose ordinal has no live instance, and
  includes them with their latest status and pinned deployment version.
- After the snapshot the client needs nothing further: a finalization is an
  instance event, the client keeps the finalized instance until a newer one
  for the ordinal arrives, and the row derive picks live over final. A late
  status write for a finalized instance is persisted and published as an
  observed update; a client holding the instance applies it by clock.
- Run report, prepare output, and log requests resolve an instance id with
  `GetScheduledInstanceByID` (latest event row), so any instance that ever
  existed resolves, not only the ordinal's last one.
- Delete validation uses live instance statuses plus the desired running
  flag. A finalized instance's termination is acknowledged by definition.

`FetchScheduledSnapshotWithLatestFinal`, its subscribe variant,
`instanceSnapshotWithLatestFinalLocked`, and
`InstanceStatusesForDeploymentLocked` are deleted with the cache.

### Visibility and reset

The stream handler holds one subscriber and one filter. For each `Update` it
applies the per-collection visibility rule to each event and drops what the
user may not see. The rules are the existing `filter*`, `nodeVisible`,
`spaceVisible`, and `canAccess` helpers in `webuihandler`, moved into one
`visibleUpdate(ctx, *Update) *Update` function with one case per field. An
`Update` with nothing visible is not sent. Heartbeats are unchanged.

Observed fields are filtered by their parent: an instance status is visible if
the instance's deployment is; a node status if the node is. The handler keeps a
map of instance id → deployment id and deployment id → space id from the events
it has forwarded, replacing `depSpace`.

Reset: when an `Update` carries authz collections that affect this user, the
handler builds a fresh `Snapshot` under the store lock and sends it in-band.
Grant events name a user; only that user's subscribers reset. Template and
global-rule changes reset every subscriber. Node `allowed_spaces` changes and
space creation also reset, since they change what a user may see without
touching the hidden entities. Resets are debounced per subscriber (200 ms) so a
burst of grant edits produces one snapshot. Policy scope changes and changes to referenced deployments reset clients
whose policy visibility changes. There is no hide event; a hide is a reset.

### Node model

`Node` is split by author. `status` sits outside both facets because three
actors write it: the node on request, the operator on accept and evict, the
health loop on unhealthy and missing.

```proto
message NodeReported {              // the node's facts about itself
  string identifier = 1;            // machine id
  string underlay_address = 2;
  string wg_public_key = 3;
  repeated string host_addresses = 4;
}

message NodeOperator {
  string name = 1;
  repeated int32 roles = 2;
  repeated int32 allowed_spaces = 3;
  int64 enrolled_time = 4;          // 0 until accept
}

message Node {
  NodeLifecycleStatus status = 1;
  int64 enrollment_requested_at = 2;   // 0 = no pending request
  NodeOperator operator = 3;
  NodeReported reported = 4;
}

message NodeEvent {                 // standard envelope
  int32 node_id = 1;
  int32 version = 2;
  int64 seq = 3;
  int64 event_id = 4;
  int32 author = 5;                 // 0 = system (node-reported or health loop)
  EventType event_type = 6;
  int64 created_time = 7;
  int64 event_time = 8;
  Node value = 9;
}

message NodeStatus {                // observed; replaces ClusterNodeStatus
  int32 node_id = 1;
  int64 observed_at = 2;            // primary wall clock at write; the LWW clock
  bool is_connected = 3;
  int64 last_connected_at = 4;
  string remote_address = 5;
  string opendeploy_version = 6;
}
```

`ClusterNode`, `ClusterNodeStatus`, and `EnrollmentRequestStatus` are removed
from the browser wire. `EnrollmentRequestStatus` remains in
`EnrollmentPrimaryMsg` for the secondary-facing enrollment stream, built from
the node event and its observed status.

The rule: a fact a node reports is in the log if any render, plan, or
scheduling decision reads it, and in the observed table otherwise. Under this
rule `identifier`, `underlay_address`, `wg_public_key`, and `host_addresses`
are log; `is_connected`, `last_connected_at`, `remote_address`, and
`opendeploy_version` are observed. If the self-upgrade path is found to compare
`opendeploy_version` against a target, it moves to `reported`.

Both hellos carry `NodeReported`:

```proto
message EnrollmentHello {
  NodeReported reported = 6;
  bytes secondary_certificate_request = 2;
  string opendeploy_version = 3;
  // 1, 4, 5 retained for one release; see compatibility.
}
message ClusterHello {
  NodeReported reported = 5;
  int32 cluster_protocol_version = 2;
  // 1, 3, 4 retained for one release; see compatibility.
}
```

One store method serves both: `ReportNode(identifier, NodeReported)` appends a
reported event if the bundle differs from the current value, otherwise appends
nothing. This replaces `MustSetNodeWGPublicKey`, `MustSetNodeAddresses`, and
`SetNodeHostAddresses`. The primary's own addresses from
`runtime.go` go through the same method.

Enrollment flow:

1. Request. The secondary sends `EnrollmentHello`. Unknown identifier: create
   event, status `ENROLLMENT_REQUESTED`, `enrollment_requested_at` now,
   `reported` as sent. Known node, any status: one event setting
   `enrollment_requested_at` and `reported`, or no event if both match.
   Observed row updated with connection state, remote address, version. The
   CSR stays on the session.
2. Review. The enrollment list is a derive: `enrollment_requested_at != 0` or a
   non-member status. The UI holds the node version it rendered.
3. Accept. `EnrollmentAcceptRequest` carries `id`, `node_name`,
   `expected_version`. One event: status `MEMBER_NORMAL`, `operator.name`,
   `operator.enrolled_time` if zero, `enrollment_requested_at` cleared. The
   primary signs the CSR from the open session and replies `EnrollmentAccepted`
   unchanged. If the node re-reported between render and accept, the version
   check fails and the operator re-reviews.
4. First cluster hello. Same bundle through `ReportNode`, a no-op unless
   something changed in the interval.
5. Cancel and expiry clear `enrollment_requested_at`; for a never-admitted node
   they set `ENROLLMENT_CANCELLED` or `ENROLLMENT_REQUEST_EXPIRED`.

Schema:

```sql
-- node_event_log: additive
ALTER TABLE node_event_log ADD COLUMN host_addresses TEXT NOT NULL DEFAULT '[]';
ALTER TABLE node_event_log ADD COLUMN enrollment_requested_at INTEGER NOT NULL DEFAULT 0;
-- addresses stays; reported.underlay_address = addresses[0].
-- node_statuses: enrollment_pending and host_addresses stop being written now
-- and are dropped one release later.
ALTER TABLE node_statuses ADD COLUMN observed_at INTEGER NOT NULL DEFAULT 0;
```

Migrations follow the `migrations.sql` convention (idempotent, tolerate
duplicate column, no semicolons in comments). No data migration: every session
reconnects when the primary restarts, so `host_addresses` re-arrives through
both hellos and a pending re-enrollment is re-requested by the secondary's
retry loop. The enrollment list may be empty for the seconds between restart
and the first retry.

Primary ↔ secondary compatibility for the hello change. The primary upgrades
first and then pushes the worker upgrade, so for one release the primary must
accept both hello shapes: if `reported` is set use it, otherwise fold the flat
fields (`underlay_address`, `wg_public_key`, `host_addresses`,
`requesting_machine_id`) into a `NodeReported`. New secondaries send only
`reported`. The flat fields and the fold are removed the release after every
cluster has rolled, per the pattern in the deployment def-split plan.

### Scheduled instances on the browser wire

`ScheduledInstanceEvent` replaces `ScheduledInstanceState` in `Snapshot` and
`Update`. The pinned deployment is not embedded; the browser resolves it as
`deployments[instance.deployment_id][instance.deployment_version]`.
`RunnerStatus.running_version`, which `WithRunningVersion` decorates from the
pinned spec at publish time, is kept so the status is self-describing.
`ScheduledInstanceEvent.value` is the current `ScheduledInstance` message.

The scheduler, the netmap publisher, and the worker push read the pinned
`Config` through the joined instance query; pinned deployment rows are
immutable and cached inside `pq.Queries`. Only the browser wire changes.

### Frontend state

The reducers define snapshot/replay equivalence; the builder is an optimised
read of that same state, not a separate deletion policy. A store integration
test compares snapshot rebuilding with replay through creation, update,
deletion, pinned deletion, finalisation, and superseding a finalized run.

`frontend/src/state/deployments.js` becomes a tree plus reducers plus derives.

```js
// tree (module-private, replaced wholesale on Snapshot, updated per Update)
{
  seq,
  deployments:        Map<id, Map<version, DeploymentEvent>>,
  scheduledInstances: Map<id, ScheduledInstanceEvent>,
  instanceStatuses:   Map<instanceId, ScheduledInstanceStatus>,
  nodes:              Map<id, NodeEvent>,
  nodeStatuses:       Map<nodeId, NodeStatus>,
  secrets:            Map<id, SecretEvent[]>,      // history, newest last
  configs:            Map<id, ConfigEvent[]>,
  assets:             Map<id, AssetEvent[]>,
  valueDirectories, assetDirectories, spaces, networkPolicies, users, agentSessions: Map<id, item>,
  authzTemplates, authzGrants, authzGlobalRules, secretsStatus, backupStatus, systemConfig, ingressDiagnostics
}
```

Reducers, one per collection, are the only code that writes the tree:

- Versioned collections: `event_type == DELETE ? map.delete(id) : map.set(id, event)`.
  Deployments insert by `(id, version)`; delete removes the id and its
  versions once no held instance pins one.
- History collections (secrets, configs, assets): append the event to the
  id's array; delete removes the id. Pickers select events whose
  `value_version` differs from the previous event's and pin by `event_id`.
- Flag collections: `deleted ? map.delete(id) : map.set(id, item)`.
- Observed: `map.set(id, status)` only if `status.clock > existing.clock`
  (`updatedAt` for instances, `observedAt` for nodes).
- Replace collections: assign.

`applyUpdate(tree, update)` runs every reducer for the fields present, then
publishes the changed VanJS states once. `applySnapshot(snapshot)` clears the
tree and runs the same reducers over the snapshot's arrays.

Derives replace today's exported states and keep their names and shapes so
pages do not change: `deploymentsS` rows of
`{config, instance, status, scheduledInstances, pinnedConfig}` computed from
the maps (the latest-final-per-ordinal rule lives here, as a read, not a
reducer); `nodesS` and `nodeStatusesS` from the node maps; `enrollmentsS` from
nodes with a pending request or non-member status joined to `nodeStatuses`;
`secretMetasS`, `userConfigsS`, `assetMetasS` as today's view models built
from the history arrays; `spacesS`, `valueDirectoriesS`, and the rest as
sorted arrays.

`deploymentMerge.js` is reduced to the row derive, which is now the only
implementation of the live-over-final rule. The transport code
(reconnect, inactivity timeout, abort) is unchanged. Pruning of unreferenced
deployment versions runs in the instance reducer after a delete or
finalization.

## Implementation order

Nodes go first. The node event shape is what the stream carries, and doing the
stream first would mean writing `NodeEvent` twice. The node work is additive on
the primary ↔ secondary wire and ships on its own. Phases 2 to 4 are one
change to the browser contract and ship together; Phase 5 is cleanup.

### Phase 0: invariants

0. Host address enumeration: `EnumerateHostAddresses` admits RFC 4941
   temporary IPv6 addresses because the standard library does not expose
   address flags. Add a Linux implementation over `vishvananda/netlink`
   (already a dependency of `lib/network`) that skips addresses flagged
   temporary, deprecated, or tentative, and keep the current code as the
   non-Linux fallback. This is a standalone fix: today it lets the ingress
   plan publish on a rotating address; under this plan it would also append a
   node event per rotation.
1. `scheduled_instances.go` batch write: allocate one seq before the loop and
   stamp every row with it. Verify one seq per transaction through the commit
   publication oracle and snapshot/replay tests.
2. Audit every `q.Tx` in `state` for a single `NextGlobalSeq` call per
   transaction. Add a test helper that wraps `Tx` and counts allocations, used
   by the existing state tests.
3. `pubsubu`: replace drop-on-full with close-and-unsubscribe. Release goutil,
   bump `backend/go.mod`. Every current subscriber loop already exits on a
   closed channel (`if !ok { return }`); verify the stream handler ends the
   HTTP response so the browser reconnects.
4. Deployment renames, three commits so names never collide:
   `Deployment` → `DeploymentEvent` and `DeploymentEventType` → shared
   `EventType`; then `DeploymentDef` → `Deployment`; then `def` → `value` and
   `id` → `deployment_id`. Add `seq` and `event_id` to `DeploymentEvent`,
   populated by `deploymentFromRow`. Update `agent_instructions.md` and
   `agent_sessions_test.go` for the JSON names, `user-docs` data model pages,
   and `docs/engineering/api.md`. Field numbers do not change; verify with
   the existing worker round-trip tests that an old-format
   `ScheduledInstanceState` blob decodes.

Files: `backend/storage/primarydb/state/scheduled_instances.go`,
`global_seq_test.go`, goutil `pubsubu`, `api-contract/model/deployments.proto`,
`backend/storage/primarydb/state/deployment_conv.go`, the generated
`apigen` and `frontend/src/capi`, and every Go and JS site that names the
two types.

### Phase 1: node model

1. Proto: add `NodeReported`, `NodeOperator`, `Node`, `NodeEvent`,
   `NodeStatus` to `api-contract/model/nodes.proto`. Add
   `reported` to `EnrollmentHello` and `ClusterHello`; keep the flat fields.
   Add `expected_version` to `EnrollmentAcceptRequest`. Regenerate.
2. Schema: the two `node_event_log` columns above in `schema_nodes.sql` and
   `migrations.sql`.
3. Store: `nodeEventSpec` gains `HostAddressesJSON` and
   `EnrollmentRequestedAt`; the internal `Node` struct becomes the value
   shape (drop the mixed `HostAddresses` from `node_statuses`); `nodeToAPI`
   produces `NodeEvent`.
   Add `ReportNode`; delete `MustSetNodeWGPublicKey`, `MustSetNodeAddresses`,
   `SetNodeHostAddresses`. Enrollment request path appends the request event
   as described; remove `EnrollmentPending` from `UpsertNodeObservedMeta` and
   delete `ClearNodeEnrollmentPending`. Accept takes `expected_version` and
   appends the single accept event. `enrollmentRequestFromRow` reads
   `enrollment_requested_at` instead of the flag.
4. Handlers: `clusterhandler/session.go` hello handling becomes one
   `ReportNode` call with the fold for old secondaries;
   `enrollmenthandler` same; `runtime.go` host address enumeration calls
   `ReportNode`. `ingress_plan.go` and `netmappublisher` read
   `host_addresses` from the node value.
5. Secondary: `secondary/enrollment.go` and `secondary/cluster_session.go`
   send `reported`. `currentHostAddresses` is collected at enrollment time as
   well as at connect.
6. Browser API: `ClusterNodeList` carries `NodeEvent`; rename and allowed-space
   endpoints unchanged in behaviour. Frontend `cluster.js` reads the new
   shape (this is the one frontend change in this phase, ahead of the tree
   rewrite).

Tests: `node_name_uniqueness_test.go`, `node_allowed_spaces_test.go`,
enrollment tests in `state`, `enrollmenthandler` tests, a new test that a
re-enrollment of a member produces exactly one event and accept exactly one
more, a test that an unchanged hello appends nothing, and the e2e enrollment
path (`e2e/`, see the harness notes: VMs are wiped by default and a run takes
about 25 minutes).

Verification: enrol a fresh worker against a new primary and count node event
rows (2). Reconnect the worker and confirm no new row. Change an address on the
worker and confirm one row. Check the ingress plan reflects it without a
restart.

### Phase 2: store publish path and snapshot builder

1. Proto: `Snapshot`, `Update`, `StateStreamMsg`, `ScheduledInstanceEvent`
   (standard envelope over `ScheduledInstance`), `SecretEvent`,
   `ConfigEvent`, `AssetEvent`, `NetworkPolicyEvent`, each with the standard
   envelope and its `value` message. Change
   `PostV1GlobalStateStream` to return `stream StateStreamMsg`; replace
   `GetV1GlobalState` with `GetV1GlobalSnapshot` returning `Snapshot`.
   Regenerate.
2. `state.Service`: add `updates *pubsubu.PubSub[Update]` and a per-transaction
   collector (`txUpdate` with `add*` methods, published by a `commit` helper
   that every `q.Tx` caller uses). Convert the 34 notify sites to collector
   adds. Observed writers publish observed-only updates.
3. Typed adapters: `SubscribeDeployments`, `SubscribeScheduledInstances`,
   `SubscribeNodes`, etc., implemented over the `Update` stream, keeping the
   channel types the nine internal consumers use. `instancecache.Subs` becomes
   one of these adapters; `NotifyInstanceLocked` feeds the collector.
4. `BuildSnapshot(ctx)` in `state`: reads under `Mu`, applies the snapshot
   contents rules, stamps `seq`. The pinned-version rule reuses
   `configForVersionLocked`; the latest-final rule uses
   `ListLatestScheduledInstancePerOrdinal` plus the latest status per
   instance.
4a. Remove `latestFinalCache` and everything listed under "Store: live map and
   the latest-final query". Add `GetScheduledInstanceByID` (latest event row)
   for the handlers. `retainFinalizedLocked`'s ordering rule (a finalization
   of an incarnation an existing live instance already replaced must not win)
   moves into the snapshot query's filter and the FE derive.
5. Remove the per-collection pubsub fields and subscribe methods once no
   caller remains.

Tests: `latest_final_instance_test.go` is retargeted at `BuildSnapshot`
(finalized instance present for a stopped deployment, absent once an ordinal
has a live instance, per-ordinal independence, survives restart because it is
a query, predicate applied); a store test that a secret rotation with
deployment rewrites publishes
one `Update` with one secret event and N deployment events under one seq; a
test that a status write publishes an observed-only update whose seq equals
`global_seq`; snapshot tests for the pinned-version rule during a rollover
(serving and standby versions both present, superseded version absent after
finalization) and for the deleted-with-draining-instance case.

### Phase 3: stream handler

1. Rewrite `state_stream.go`: subscribe once, send `BuildSnapshot` filtered,
   then loop over `Update`s through `visibleUpdate`. Keep the heartbeat.
2. Move the per-collection visibility helpers into `visibility.go` and
   implement `visibleUpdate` and `visibleSnapshot`.
3. Reset on authz and allowed-space changes, targeted and debounced.
4. `global_state.go` becomes `GetV1GlobalSnapshot` returning
   `visibleSnapshot(BuildSnapshot())`.
5. Value endpoints: `/v1/secrets/*`, `/v1/configs/*`, `/v1/assets/*` return
   `SecretEvent`, `ConfigEvent`, `AssetEvent` (list: latest per entity;
   writes: the appended event). `/v1/deployments/get` returns the
   `DeploymentEvent`, its `ScheduledInstanceEvent`s, and their statuses,
   replacing `DeploymentState`'s embedded `ScheduledInstanceState`.
6. Rewrite `agent_instructions.md` sections 4 onward against `Snapshot`: the
   envelope, `value`, `expected_version`, the history rule, and the event
   returns from writes. Update `agent_sessions_test.go` to assert the new
   names (`deployment_events`, `value.spec`, `event_id`).
7. `run_report.go`, `deployments.go` (`deploymentStatuses`), and
   `deployment_validation_layers.go` stop reading the retained-final view:
   instance lookup by id, statuses of live instances per deployment, and live
   statuses plus the desired flag for delete.
8. Delete `applyEnrollmentUpdate`, `depSpace`, `findConfigByID` if unused.

Tests: `access_enforce_test.go` re-targeted at `visibleUpdate` and
`visibleSnapshot`; a test that a grant for user A resets A's stream and not
B's; a test that an update with no visible events is not sent; a test that a
closed subscriber channel ends the stream with no panic.

### Phase 4: frontend state

1. `capi` regeneration for the new messages.
2. `frontend/src/state/tree.js`: the tree, reducers, `applySnapshot`,
   `applyUpdate`, the seq gate, version pruning.
3. `frontend/src/state/derive.js`: every exported VanJS state as a derive
   over the tree, keeping the existing names and row shapes.
4. `deployments.js` keeps the transport and calls the two apply functions.
   `deploymentMerge.js` shrinks to the row derive.
5. Pages: `cluster.js` (enrollments derive replaces `enrollmentsS` items;
   accept sends `expected_version`), `status.js`, `runReportOverlay.js`,
   `openDeployGroupUpdateOverlay.js`, `logs.js` compile-check against the
   unchanged row shape. `secrets.js`, `assets.js`, and the env pickers read
   view models built from history arrays; the view model functions
   (`secretViewModel`, `configViewModel`, `assetViewModel`) are rewritten to
   take the array.
6. Delete `applyItemUpdate`, `applySpaceUpdate`, `applyAssetUpdate`,
   `applyScheduledInstanceUpdate`.

Tests: `deploymentMerge.test.js` becomes `tree.test.js` (reducers, seq gate,
snapshot replace, pruning) and `derive.test.js` (row derive incl. the
latest-final rule, enrollments derive). Manual: open two browsers, rotate a
secret referenced by a deployment, confirm both render the new deployment
version and the new secret version in one paint; edit a grant for one user and
confirm only that user's page resyncs.

### Phase 5: cleanup

- Delete `State`, `GlobalState`, `DeploymentSnapshot`,
  `ScheduledInstanceSnapshot`, `DeploymentState`, `ClusterNode`,
  `ClusterNodeStatus`, `ClusterNodeList`, `ClusterNodeStatusList`, the
  aggregate `Secret`/`Config`/`Asset` messages and their `*Version` and
  `*SpaceVersion` sub-messages, `SecretList`/`ConfigList`/`AssetList`,
  `NetworkPolicy.deleted`.
- `ScheduledInstanceSnapshot` remains solely on `MsgToSecondary`; retaining its
  worker assignment shape takes precedence over its removal from the browser API.
- Done after the v0.0.587 rollout: `node_statuses` dropped, the flat hello
  fields reserved, and the one-time observation copy removed from
  `migrations.sql`.
- Update `docs/engineering/api.md` (stream contract, node model),
  `docs/engineering/networking.md` (host addresses source), and the
  `CLAUDE.md` index.

## Resolved questions

Each was checked against the code on 2026-09-08.

- **`opendeploy_version` is observed.** The enrollment handler stores it in
  `node_statuses` and nothing reads it back. The self-upgrade decision is made
  on the node in `engine/operator.go` by comparing the deployment's workload
  version with the running binary's compiled-in version; the primary never
  compares reported versions.
- **The aggregate `Secret`, `Config`, and `Asset` messages are deleted.**
  The endpoints return events and agents read history from `Snapshot`, so
  the bare names go to the value types. Browser mutation consumers use
  entity ids and `event_id` from the returned envelope; pickers read the
  snapshot history.
- **`GetV1GlobalState` is an agent contract, and the agent contract moves to
  `Snapshot`.** The agent instructions call it the starting point for
  everything and `agent_sessions_test.go` asserts its shape; both are served
  from the same binary and are rewritten in Phase 3. `GlobalState` is
  deleted and `/v1/global/snapshot` returns the browser's `Snapshot`.
- **Temporary IPv6 addresses are included today.** `eligibleHostAddress`
  cannot see address flags. Fixed in Phase 0 step 0 through netlink on Linux.
- **No per-stream ordinal for observed updates.** Within a connection HTTP/2
  delivers in order and Phase 0 makes a full channel close the subscriber, so
  there is no undetected gap. Across connections a `Snapshot` carries the full
  observed set, and resume will do the same. An ordinal could only signal
  "resync", which a closed stream already forces. What makes observed state
  safe is the reducer: last-writer-wins on the value's own clock, which
  required adding `observed_at` to `NodeStatus`. If the observed set ever
  grows too large to send whole, the path is an `observed_seq` on the
  append-only status rows.

## Open questions

None.

---

# Part B: bounded contexts and the commit API

Status: implemented and validated. B0–B5 are complete, including unlimited
observed history, sidecar ownership, the callback commit API, writer-owned version checks, grant event envelopes,
publication oracles, and documentation. Full E2E passed after both parts.
Part B reshapes what
Part A left as its seam: the transaction collector and the single `Update`
message that carries every kind of state.

## Context

Part A made the store the one place every change flows through, and in doing
so poured everything into one message and one publish path. Three things that
are not part of the core state model now ride the store's transaction:

- **Sidecar contexts.** `BackupStatus`, `IngressDiagnosticList`,
  `SecretsStatusResponse` and `AgentSession` are in `apigen.Update` only
  because the browser wants one connection and the old `State` oneof carried
  them. They take the store mutex, and most still allocate a global seq
  (`writeReplacementLocked`) for data the event log never sees. The Part A
  E2E exposed a replication feedback loop for backup status, so that status
  already publishes without a database write and bypasses the browser seq
  gate. Its ownership and wire type still need the Part B split.
- **Observed overlay.** Instance statuses and node statuses are merged by their
  own clocks, never by `seq`, yet the collector attaches a node status to node
  events (`updates.go`, the node-status collection branch) and the stream
  handler has to strip authored fields from pre-snapshot updates to keep
  observed values flowing.
- **The collector itself.** `commitUpdateLocked` lets `mutate` write rows
  through `*pq.Queries`, then re-reads every log table at the allocated seq
  (nine `*AtSeq` queries) and converts rows back to protos. Replacement
  collections cannot be collected, so they overwrite `tx.Update` by hand. The
  DB is the truth, but the truth is reconstructed after the fact.

Part B separates publication contexts. Each writer persists its changes and
returns core facts and/or observations; the scheduler extends the same
transaction. The store owns conditional sequence allocation, cache installation,
commit and publication. Writers own version checks and sequence fields.

## Goals

- The primary owns one `CoreUpdate` pubsub. Each commit with content publishes
  once, carrying the commit seq, authored events and observed statuses in one
  message. Sidecar contexts own their own pubsub and never touch the store
  mutex or `global_seq`.
- Observed writes consume the global seq like authored writes; the statuses in
  a `CoreUpdate` still merge by their own clocks.
- One commit API: the writer performs every database write inside the
  transaction and returns core and/or observed projections of its written rows.
  The callback sets the candidate seq on authored rows, core update, and events.
  A single scheduler phase extends the transaction before commit. The store
  saves the sequence only when the final update contains core facts, then
  installs caches and publishes each part without rewriting it.
- Each writer checks expected entity or facet versions using the transaction
  queries before mutation, rejecting stale requests with its domain error.
- Code deletion: the collector and production `*AtSeq` re-reads go away.
  The queries remain as a test oracle. Existing per-entity write helpers stay;
  no IDs interface, applier, proto → row layer or store-side facet diffing is added.

## Non-goals

- Dropping the global mutex. Writers still run under `Mu`.
- Changing the event envelope, the snapshot contents or the frontend tree.
- Primary → secondary wire changes.

## Target design

### Three kinds of state on the wire

```proto
// Sequenced. Published only by commit.
message CoreUpdate {
  int64 seq = 1;
  repeated DeploymentEvent deployment_events = 2;
  repeated ScheduledInstanceEvent scheduled_instance_events = 3;
  repeated NodeEvent node_events = 4;
  repeated SecretEvent secret_events = 5;
  repeated ConfigEvent config_events = 6;
  repeated AssetEvent asset_events = 7;
  repeated NetworkPolicyEvent network_policy_events = 8;
  repeated Space spaces = 9;
  repeated User users = 10;
  repeated ValueDirectory value_directories = 11;
  repeated AssetDirectory asset_directories = 12;
  AuthzRuleTemplateList authz_rule_templates = 13;   // replace when present
  repeated AuthzGrantEvent authz_grant_events = 14;
  AuthzGlobalRuleList authz_global_rules = 15;
  SystemConfigVersion system_config = 16;
  // Observed statuses share the commit seq; values merge by their own clock.
  repeated ScheduledInstanceStatus instance_statuses = 17;
  repeated NodeStatus node_statuses = 18;
}

message StateStreamMsg {
  Snapshot snapshot = 1;
  CoreUpdate core = 2;
  bool heartbeat = 4;
  // Sidecar contexts. Each is a whole-value replacement with no seq.
  BackupStatus backup_status = 5;
  IngressDiagnosticList ingress_diagnostics = 6;
  SecretsStatusResponse secrets_status = 7;
  AgentSessionList agent_sessions = 8; // wrapper permits an empty replacement
}
```

`Snapshot` bootstraps all three kinds. Its grant collection is
`repeated AuthzGrantEvent authz_grant_events`, matching the core update.
The core pubsub carries `apigen.CoreUpdate` directly; the old `apigen.Update`
and grant-routing wrapper are removed. The internal transaction result is now
`state.Update{Core, Observed}`; neither field changes the stream wire format.

Frontend: the reducer gains one function per kind. `core` goes through the
seq gate; `observed` never does; each sidecar replaces its own slice. The
seq gate and `needsReset` inspect `CoreUpdate` only, which is what they
already do in substance.

### Grant events and visibility

Grants use the same envelope pattern as other logged entities:
`authz_grant_id`, `version`, `seq`, `event_id`, `author`, `event_type`,
`created_time`, `event_time`, and `value`. The value contains `user_id`,
`template_id`, and the grant bindings or direct rule. Delete events retain
that value. Inserts return their persisted event id, and writers and snapshots
use the same row → proto converter.

Each update carries the grant events written in that transaction, including
deletes. A snapshot contains the latest event per live grant, using the existing
SQL filter. The frontend stores grants by identity and removes them on delete;
its user-page records derive from those events. Database history is retained.

The stream derives affected users from `event.value.user_id`, including on
revocation, and sends those users a fresh filtered snapshot. This removes
objects they can no longer see and includes newly visible ones. Other users
receive only grant events they may view, without a reset. Template and global
rule changes still reset all streams. No separate `GrantUsers` metadata is
needed. The grant CRUD endpoints continue to expose their existing records.

### Sidecar ownership

Each sidecar publishes from the component that produces it, as before Part A:

| Context | Owner | Publish |
|---|---|---|
| backup status | `backup` | its own `pubsubu.PubSub[apigen.BackupStatus]`, diff-gated |
| ingress diagnostics | `netmappublisher` | existing `DiagnosticsSnapshotAndSubscribe` |
| secrets status | `webuihandler` secrets manager | own pubsub |
| agent sessions | agent session service (extracted from `state.Service`) | own pubsub, filtered per user by the stream |

The store loses `NotifyBackupStatusUpdate`, `CurrentBackupStatus`,
`NotifySecretsStatusUpdate`, `NotifyIngressDiagnostics` and the
`mutateAgentSession` path. `BuildSnapshot` takes the sidecar values as
arguments (or the stream handler fills them in after the store snapshot; the
values have no ordering relation to `seq`, so either is correct). The stream
handler fans the four sidecar channels into `StateStreamMsg` alongside the
core subscription.

Agent sessions stay in sqlite and keep their queries; only the publish and the
ownership move. The store's `agent_sessions` table becomes a table the agent
session service owns through `pq`, the same way asset store rows and personal
sessions already are.

### Observed state: one pattern for both collections

Observed state is what nodes report, not what the primary decides. Two
collections: `ScheduledInstanceStatus` (runner and preparer state reported by
a worker) and `NodeStatus` (connection, remote address, worker version, seen
by the cluster session). Neither has a meaningful version and both arrive out
of order, so neither can use `seq`. Each value carries its own clock and the
rule everywhere is last-writer-wins on that clock.

Part A left the two collections on different mechanics:

| | instance status | node status |
|---|---|---|
| storage | append-only `scheduled_instance_status`, latest per id by query | one row per node, upserted in place |
| clock | `updated_at`, hybrid logical clock set by the writer | `observed_at`, stamped `MAX(prev + 1, now)` by the primary |
| history | kept, feeds run reports | none |
| clearing | not expressible | not expressible |

Part B moves node status onto the instance pattern:

- **`node_status_log`**, append-only. Each write inserts a row; the latest per
  node is a query at snapshot time (`ListLatestNodeStatuses`), the same shape
  as `ListLatestScheduledInstanceStatuses`. `node_statuses` was dropped
  after the v0.0.587 rollout, like the columns Part A retired.
- **`updated_at`** replaces `observed_at`, with the same HLC type the instance
  status uses. The primary is the only writer of node status, so the merge
  behaviour the instance HLC has for two writers is not exercised, but one
  type means one reducer, one conversion and one comparison serve both.
- **History** comes for free: when a node dropped and came back is a query,
  which the cluster page cannot show today. Keep all history in both observed
  logs for now; snapshots still query only the latest row per entity.
- **Clearing is a tombstone row.** An empty status with a fresh clock is a
  normal write, so last-writer-wins clears it on every client without a
  snapshot. Clients retain the tombstone clock while its parent is retained,
  and reconnect snapshots include the latest tombstones, so delayed observations
  cannot resurrect a cleared entry. `InvalidateNodeRuntimeState` appends tombstones instead of
  deleting rows and publishing nothing.

Observed rows are never stamped with `global_seq`. Their HLCs remain authoritative
even when a report and the scheduling changes it triggers share a transaction.

### Observed publish path

Observed updates travel in the same `Update` as the core update of their
commit and carry the commit seq. The stream handler consumes the single
stream, emits `Core` then `Observed` for one commit, and drops both at or
below the snapshot seq. Historically the observed path was a separate
unsequenced pubsub and the handler subscribed to both; that text follows.
The stream handler subscribed to both and no
longer needed the `update.Seq <= seq` rewrite: core updates at or below the
snapshot seq are dropped whole, observed updates always pass. Internal
subscribers that want instance state with status (`SubscribeScheduledInstanceUpdates`)
project from both channels.

The collector's node-status branch is deleted. Enrollment returns both its
authored event and observed metadata from the same mutation. There is no
writer-owned publication or cache assignment after the commit returns.

### No status ordinal

Correctness for observed data comes entirely from the per-entity clock. A
global `status_seq` would serve two purposes and neither applies:

- **Detecting a missed update.** A gap would tell the client to resync, but a
  full channel already closes the subscription and forces a snapshot, and a
  missed update for one entity is repaired by that entity's next report.
- **Resuming without a full snapshot.** Worth having only if the observed set
  were too large to resend on reconnect. It is one row per live instance plus
  one per node. If that changes, both logs are append-only, so the row id is
  already a monotonic cursor per table and `WHERE rowid > ?` gives
  resume-since without a new counter.

### The commit API

```go
type Update = apigen.CoreUpdate

// Commit holds Mu. mutate checks expected versions before mutation,
// performs every write, and returns read-converter projections of those rows
// (nil when nothing was written). It sets seq on authored rows and events;
// Commit stamps Update.Seq, runs the registered triggers, and publishes.
func (s *Service) Commit(ctx context.Context, inlockValidate pq.Validator,
    mutate func(q *pq.Queries, seq int64) (*Update, error)) error
```

Deployment create, update and delete each expose one method with an optional
`inlockValidate func() error`. `commit` acquires `Mu`; the callback runs before
any mutation while the SQLite writer reservation is held. Callers check expected
entity versions and validate current database state there. Validation reads use
`Queries()` and freshly assembled deployment, node and pinned-instance state,
not the runtime caches. The callback must not take `Mu`, write, or commit.
Compound writers that still acquire `Mu` around a broader operation enter the
same transaction/publication implementation through `commitAndReconcileLocked`.

The store begins an IMMEDIATE SQLite write transaction, reads `global_seq + 1`
as a candidate, runs `mutate`, then runs one scheduler reconciliation phase on
that transaction. It saves the candidate whenever the final update has core
or observed content. Empty updates consume no sequence. Both fields may be
nil when retaining a delayed historical report that does not change the
latest observation; that row is stamped with seq 0.

No caches are installed. Historically cache changes were staged before commit, then installed from the final update
after commit succeeds. Core publication follows, then observed publication,
all under `Mu`. There is no atomic browser message across the two channels;
snapshots and database readers see the committed transaction as a whole.
Neither callback is retried, and the store neither rewrites nor repairs returned
sequence fields. Failed transactions publish nothing, change no shared caches,
and consume neither ids nor a sequence.

Primary database transactions acquire the SQLite writer reservation before
reading (`_txlock=immediate`). The session sidecar has an independent mutex and
shares the connection pool: a deferred read-then-write transaction could fail
with `SQLITE_BUSY_SNAPSHOT` if that sidecar commits between the read and write.
Writer reservation prevents that upgrade race without moving sidecars onto `Mu`.

`mutate` is not a protected operation. Each writer is responsible for its
correctness, checks expected versions before mutation, allocates its own ids
in the transaction, and stamps author, times, entity versions and facet versions
through the existing per-entity event helpers. It may write anything else the
transaction needs, including sealed secret bytes and internal user blobs.

Every returned event is built from a row written in that transaction,
converted with the same row → proto function reads use, never hand-assembled.
The writer persists the supplied seq on each event row so the converter carries
it into the event, and explicitly sets `CoreUpdate.Seq` to that same seq.
Replacement collections return their persisted public projections. A composed
operation, such as secret rotation plus deployment reference updates, writes
and collects all of its projections in one callback at one sequence.

There is no `Expect` type, path dispatcher, `IDs` interface, applier, proto → row
write layer, or store-side facet diffing. Existing write helpers and parameter
structs remain useful. The collector, `txUpdate`, `commitUpdateLocked`,
`commitMustLocked` and `writeReplacementLocked` are removed after all writers
use the single call. The `*AtSeq` queries survive only as a test oracle asserting
that the published update matches a re-read at its seq, never as production
collection. Tests also compare `CoreUpdate.Seq` to the committed database sequence.

### Transactional scheduling

`Scheduler.Start` synchronously installs the single scheduler hook and performs
startup recovery under the primary writer freeze. Ordinary deployment, target,
and current instance-status updates identify affected deployment ids. The hook
reads latest desired configs, pinned configs, targets and observations using
its transactional `q`; incoming payloads and shared caches are not its source
of current state. Delayed reports are retained without publishing a current
observation or triggering scheduling.

For each affected deployment the scheduler reconciles to immediate stability.
The iteration bound follows the number of placements and their finite target
transitions. It allocates new ids and appends target rows in that same
transaction, extending the returned core update through normal row converters.
Readiness promotion, draining superseded placements, stopped-instance
finalization and RECREATE replacement creation commit with their triggering
write at one sequence. No recursive commit or lock-taking public store call
is allowed inside the hook.

Rendering, network delivery and waiting for acknowledgements stay outside the
transaction. `Scheduler.Run` handles acknowledgement wakes and drain timeout
ticks as explicit reconciliation transactions. Ticks scan currently draining
deployments; startup scans all desired deployments for durable recovery. A
sweep that changes nothing consumes no sequence and publishes nothing.

Drain waits derive their decision sequence and deadline from the persisted
RUN_DRAINING event (`global_seq`, `event_time`). Repeated triggers cannot reset
the deadline. A drain written at or before the startup sequence receives one
fresh timeout from startup; with a configured barrier it ignores the new,
potentially empty acknowledgement map. New drains cannot clear the barrier
before their own transaction has committed. The scheduler has no mutable
per-instance drain map to roll back or rebuild.

Primary scheduled-instance and deployment caches have one post-commit owner.
The shared worker cache retains its own status write policy; the primary's
status method uses the merged boundary so a triggering status writer cannot
overwrite a final target or resurrect a finalized instance.

### Writer-owned version checks

Expected versions are typed arguments to the domain writer. Inside `mutate`,
the writer reads the current row using `q`, checks the relevant entity or facet
counter, and builds the new event from that checked row. These counters are
never the last-change global seq. Failed checks return the writer's existing
domain error and roll back the allocated sequence without publication.

Deployments translate their existing next-version request into the current
entity counter; policies and enrollment acceptance also check entity counters.
Rotations check every referencing deployment's spec counter and the exact set
of live references before writing or sealing the new value. A rename can
advance the entity counter without invalidating a spec expectation, and the
rotation preserves the latest name by building from the transactional row.

Reads captured under `Mu` remain current until commit. Versions supplied by an
API caller can already be stale when the lock is acquired and must be checked.
Node allowed-space requests have no expected-version field, so their redundant
read-and-recheck under `Mu` is removed. No history-derived facet counters are
needed for that operation. All retained event history remains intact.

## Implementation order

All phases below are complete. The full Go suite and frontend tests pass,
as do race checks for storage, handlers, scheduler, backup, and network-map
publication. `testing-vms/run.sh` with `FLOWS=global-state-stream` passed the
complete browser flow, WireGuard checks, and kernel enforcement (20m34s).

### Phase B0: prerequisites from the Part A review

Fix before or with Part B; they are independent of it.

1. Internal subscribers must survive a closed channel. A full `pubsubu`
   channel now closes rather than drops; scheduler, netmap publisher,
   netproxy state writer, log manager alignment and `operator.RunAll` all
   return silently on `!ok`. Each resubscribes and re-reads its snapshot, or
   the adapter in `projectSubscription` does it for them and logs at error.
2. `EndEnrollmentRequest` guards on the request timestamp, not the node
   version, so an interleaved node event cannot make cancel or expiry a
   no-op.
3. A failed host-address enumeration reports "unknown", not "none", and
   `ReportNode` skips the field.
4. Frontend call sites of changed REST responses: deployment create,
   asset upload and editor, settings secret picker, explorer selection.

### Phase B1: sidecar split

1. Proto: `CoreUpdate` (authored events plus observed statuses), new
   `StateStreamMsg`; delete `Update`. Regenerate.
2. Backup, secrets status and ingress diagnostics publish through their own
   pubsubs; delete the store's `Notify*` for them. `BuildSnapshot` no longer
   reads them; the stream handler and `GetV1GlobalSnapshot` fill them in.
3. Extract the agent session service from `state.Service` with its own
   pubsub. The stream filters to the connected user as today.
4. Stream handler fans in the sidecar channels. Frontend reducer gains a
   function per kind; sidecar slices replace whole.
5. Tests: a backup status update allocates no seq and takes no store lock;
   a sidecar value survives a core reset; agent session updates reach only
   their owner.

### Phase B2: observed path

1. Schema: `node_status_log` with `updated_at` (HLC, same type as the
   instance status), `ListLatestNodeStatuses`, `ListNodeStatusHistorySince`.
   `NodeStatus.observed_at` becomes `updated_at` on the wire. `node_statuses`
   stops being written; dropped after the v0.0.587 rollout.
2. Writers append rows: `SetNodeStatusByIdentifier`, `UpsertNodeObservedMeta`
   (enrollment and hello), disconnect. `InvalidateNodeRuntimeState` appends
   tombstone rows for instance and node statuses instead of deleting.
3. Keep all history in both observed logs; do not add age-based pruning.
4. Local and replicated instance status writers, node status writers and
   enrollment return observations through the merged commit boundary as the
   status fields of the same `CoreUpdate`.
5. Delete the collector's node-status branch and the stream handler's
   pre-snapshot rewrite.
6. `SubscribeScheduledInstanceUpdates` projects from both channels.
7. Frontend: one observed reducer for both collections, keyed by entity id,
   last-writer-wins on `updatedAt`; a tombstone clears the entry.
8. Tests: an observed update carries the commit seq and is dropped at or
   below the snapshot seq; node status clock is monotonic across append (retarget
   `TestNodeObservedClockMonotonic`); a tombstone clears a status on a
   client that missed no update and on one that did; node history lists a
   disconnect between two connects; snapshots select the latest row while
   preserving every historical row.

### Phase B3: commit API

1. Add the single `commit(ctx, inlockValidate, mutate)` API. Its callback receives
   `*pq.Queries` and the candidate seq, checks expected versions, writes the
   rows, and returns `Update{Core, Observed}` with the supplied sequence set
   on any core update and every core event.
2. Port writers one entity at a time: deployments, scheduled instances
   (including the multi-row flip), nodes and enrollment, secrets, configs,
   assets and asset migrations, network policies, authz, spaces, directories,
   users, and system config. Reuse existing event-stamping helpers and
   row → proto converters for every returned event.
3. Delete the collector and grant-routing wrapper. Keep `*AtSeq` queries
   solely for tests that compare publication to persisted rows.
4. Tests: rollback consumes no id or seq and publishes nothing; the writer
   returns correct seq fields and the store publishes the update unchanged;
   published events equal rows re-read at their seq;
   existing facet-version tests and the atomic rotation test
   (`TestTransactionUpdateIncludesRotationAndAllDeploymentEvents`) pass
   through the new API, including private payload writes. Grant create/delete
   events round-trip through the wire and snapshot replay, preserve the subject
   on tombstones, and reset only the affected user on revocation.

### Phase B4: writer-owned version checks

1. Deployment update/delete, policy update, and enrollment acceptance check
   entity versions using the transaction queries inside their callbacks.
2. Rotations check the exact live reference set and deployment spec versions
   before mutation. Remove the redundant allowed-space counter recheck and
   the generic expectation path dispatcher and history-counting infrastructure.
3. Tests: stale requests reject without writes, sequence consumption, or
   publication; rotations check all spec expectations before sealing and
   preserve a rename that leaves the spec counter unchanged.

### Merged commit and scheduler refinement

- Route authored and observed primary writers through the same transaction;
  acquire the writer before reading and persist the sequence for any commit
  with content.
- Remove the store caches; remove writer follow-up assignments.
- Install one transactional scheduler hook, and remove ordinary deployment and
  instance subscriptions from the scheduler. Keep startup, ack and timer triggers.
- Derive drain waits from persisted events and retain the conservative startup
  timeout. Keep all authored and observed history.
- Tests cover core-only, observed-only and empty updates; rollback of rows,
  history, ids and caches; no callback retries; concurrent session writes;
  atomic desired/target writes, promotion and finalization; stale report retention;
  fixed drain deadlines; and recovery after restart. Run the full Go suite,
  relevant race tests, frontend checks and full E2E after integration.

### Phase B5: cleanup

- `deploymentFilter`, the orphaned latest-final comments in `store.go`, and
  the stale `deploymentStatuses` comment.
- Docs: `docs/engineering/api.md` stream section; `user-docs/data-model`
  `ClusterNode` references; `ingress-listen-implementation-plan.md` host
  address location.

## Resolved questions

- **Why keep the callback with `*pq.Queries`?** It lets a writer make all of
  its changes atomically, including private payloads and references to rows
  just inserted. Correctness belongs to each writer; the common convention
  is to return read-converter projections of rows written in the transaction.
- **Why do ids stay in the transaction?** So a rollback consumes nothing.
- **Who stamps versions and sequences?** The existing per-entity event helpers
  stamp versions. The writer sets the store-allocated seq on its rows and
  returned core update; normal read converters carry it into every event. The
  scheduler follows the same convention when extending the transaction. The
  store commits and publishes the final core and observed parts unchanged.
- **Why retain `*AtSeq` queries?** They provide an independent test oracle
  for the writer's returned update, with no production re-read cost.
- **Which versions do writers compare?** Entity and facet counters, never
  the global seq of the last change. Each check uses the transaction queries
  inside `mutate` before mutation.
- **Are users and spaces core?** Yes. Authors and space assignments are
  referenced by every event, so their changes must be sequenced with the
  events that reference them.
- **Are instance and node statuses core?** They are in the tree, but they are
  observed, so they are sequenced by their own clocks. Splitting the message
  makes that explicit instead of stripping fields in the handler.
- **Why move node status to an append-only log?** So both observed collections
  share one storage shape, one clock type, one reducer and one way to clear.
  History and tombstones are the two things the in-place row could not give.
- **How long is observed history kept?** All history is retained for now,
  for both node and instance statuses. No age-based pruning is added.
- **Why no `status_seq`?** The per-entity clock is what makes merges correct;
  a global ordinal could only signal resync, which a closed channel already
  forces. The append-only row id is the cursor if resume-since is ever
  needed.

## Open questions

None.
