# API design

## Overview

The API is HTTP + binary protobuf v3. Each service has its own file — `api-contract/api_service.proto`, `cluster_service.proto`, and `enrollment_service.proto` — holding its RPC definitions and per-route access policies; model messages are split per entity into `api-contract/model/<entity>.proto` (data model shapes) and `api-contract/model_<entity>_operations.proto` (endpoint request/response shapes). The generator concatenates every file into one schema before running, so a message defined in any of them is visible to all. Go and JS code is generated from the proto schema using [cleanproto](https://github.com/jptrs93/cleanproto/blob/main/README.md).

The split follows the security boundary, not just size: each service is served on a different listener with a different notion of caller identity, so which file an RPC lives in decides what can reach it.

| File | Service | Listener | Caller identity |
|---|---|---|---|
| `api_service.proto` | `ApiServer` | public front-end | per-route policy: `full` or `bootstrap` session kind |
| `cluster_service.proto` | `OpsagentClusterV1` | cluster mTLS | client cert CN |
| `enrollment_service.proto` | `EnrollmentV1` | enrollment HTTPS | none — pre-certificate bootstrap |

## Code generation

Regenerate after changing the proto schema:
```sh
bash api-contract/proto_generate.sh
```

Key generated files:
- `backend/apigen/model.gen.go` — Go message structs with `Encode`/`Decode` methods.
- `backend/apigen/mux.gen.go` — HTTP mux route registration and request/response wiring.
- `frontend/src/capi/model.js` — JS typedefs and protobuf encode/decode functions.
- `frontend/src/capi/capi.js` — Typed JS API client class.

Decoders silently drop unknown fields, and a peer re-encodes what it decoded.
So for any message that a worker persists or relays (assignments and their
embedded configs above all), moving data to new field numbers destroys it in
the mixed-version rollout window: the old binary drops the new fields on
decode and writes blobs carrying the data in neither format. Keep old field
numbers, or dual-write old and new layouts across the transition and fold the
old layout into the new fields on decode, removing the dual-write only once
every cluster runs the new release. `DeploymentEvent.legacy_identity`
(v0.0.448, removed after fleet convergence — see the reserved field 3) is the
worked example: it exists because the v0.0.444 identity field move broke
worker address derivation and virtual networking for every cached workload.

## Mux and handler flow (Go)

- Routes use `http.NewServeMux()` with Go 1.22+ pattern syntax (e.g. `"POST /v1/auth/master"`).
- Each route decodes the request body, calls its generated service handler, and writes a binary response.
- Primary handlers are split by security surface, one per service file: `webuihandler.Handler` implements `ApiServer`, `clusterhandler.Handler` implements `OpsagentClusterV1`, and `enrollmenthandler.Handler` implements `EnrollmentV1`.
- Web UI auth is enforced by `webuihandler.Handler.VerifyAuth`; cluster peer identity comes from mTLS, while enrollment uses its dedicated request verifier.
- Static SPA assets are served from embedded `backend/web/dist`; unknown paths fall back to `index.html`.
- The frontend is built via `//go:generate` in `backend/main.go` before embedding.
- Write handlers go through `state.Service.Commit(ctx, preLockValidate, mutate)` (see [Commit](#commit)): every check-then-write sequence (deployments, secrets, configs, assets, cluster settings) re-reads its dependencies and performs its row writes on the transaction-bound `pq.Queries` inside `mutate`, and returns the `CoreWriteUpdate` the store publishes. Domain logic lives in `app/primary/domain/<name>` packages (`deployments`, `scheduledinstances`, `nodes`, `networkpolicies`, `assets`, `secrets`, `values`, `authz`, `users`, `agentsessions`, `systemconfig`); handlers call those functions or `pq.Queries` directly. Deployment endpoints validate in two layers: pure shape rules before the commit, and a per-operation validator inside `mutate` (`inLockValidateDeploymentCreate` / `Update` / `Delete` in `domain/deployments/validate_layers.go`). Authz and network I/O (nix source verification) run before the commit; the `expected_seq` check closes the gap. Lock order: the asset operation lock (where asset file operations are involved) precedes `Mu`; subsystem locks (secrets manager, config service) nest strictly inside `Mu`.

## Client flow (JavaScript)

- `frontend/src/capi/capi.js` is the typed API wrapper.
- `frontend/src/capi/err.js` decodes `ApiErr` responses and throws JS errors.
- Protobuf encoding/decoding uses generated local runtimes, with no frontend protobuf runtime package required.

## Error handling

- UI errors are `ApiErr` with a `display_err` and `code`.
- `HandleReqErr` logs and writes a binary error body.
- The JS client surfaces the display error via `handleErr()`.

## Endpoints

Every route below is generated from `api-contract/*_service.proto`.

### Root
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| GET | `/` | — | embedded SPA assets; unknown paths fall back to `index.html` | NO_AUTH |
| GET | `/v1/healthz` | — | — | NO_AUTH |

### Cluster settings
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/cluster-settings/get` | — | `ClusterSettings` | ANY_OF default |
| POST | `/v1/cluster-settings/update` | `ClusterSettings` | `ClusterSettings` | ANY_OF default |

### Auth
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/auth/master` | `MasterPasswordRequest` | `LoginResponse` | NO_AUTH |
| POST | `/v1/auth/master/password/save` | `MasterPasswordSaveRequest` | — | ANY_OF default |
| POST | `/v1/auth/master/password/verify` | `MasterPasswordVerifyRequest` | — | ANY_OF default |
| GET | `/v1/auth/current/session` | — | `LoginResponse` | ANY_OF passkey:create, default |
| POST | `/v1/auth/passkey/register/start` | — | `WebAuthNOptionsResponse` | ANY_OF passkey:create, default |
| POST | `/v1/auth/passkey/register/finish` | `WebAuthNFinishRequest` | `LoginResponse` | ANY_OF passkey:create, default |
| POST | `/v1/auth/passkey/login/start` | — | `WebAuthNOptionsResponse` | NO_AUTH |
| POST | `/v1/auth/passkey/login/finish` | `WebAuthNFinishRequest` | `LoginResponse` | NO_AUTH |

### Agent sessions
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| GET | `/v1/agent-sessions/instructions?user_id=` | query params | rendered markdown (HTML when `Accept` prefers it) | NO_AUTH |
| POST | `/v1/agent-sessions/request-start` | `AgentSessionRequestStartRequest` | `AgentSessionRequest` | NO_AUTH |
| POST | `/v1/agent-sessions/get-session` | `AgentSessionGetRequest` | `AgentSessionPickup` | NO_AUTH |
| POST | `/v1/agent-sessions/approve` | `AgentSessionApproveRequest` | `AgentSession` | ANY_OF default |
| POST | `/v1/agent-sessions/create` | — | `AgentSessionCreated` | ANY_OF default |
| POST | `/v1/agent-sessions/list` | — | `AgentSessionList` | ANY_OF default |
| POST | `/v1/agent-sessions/revoke` | `AgentSessionRevokeRequest` | — | ANY_OF default |

### User sessions
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/user-sessions/list` | — | `UserSessionList` | ANY_OF default |
| POST | `/v1/user-sessions/revoke` | `UserSessionRevokeRequest` | — | ANY_OF default |

### Access control
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/access/rule-templates/list` | — | `AuthzRuleTemplateList` | ANY_OF default |
| POST | `/v1/access/rule-templates/create` | `AuthzRuleTemplateCreateRequest` | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v1/access/rule-templates/update` | `AuthzRuleTemplateUpdateRequest` | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v1/access/rule-templates/delete` | `AuthzRuleTemplateDeleteRequest` | — | ANY_OF default |
| POST | `/v1/access/grants/create` | `AuthzGrantCreateRequest` | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v1/access/grants/delete` | `AuthzGrantDeleteRequest` | — | ANY_OF default |
| POST | `/v1/access/global-rules/list` | — | `AuthzGlobalRuleList` | ANY_OF default |
| POST | `/v1/access/global-rules/create` | `AuthzGlobalRuleCreateRequest` | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v1/access/global-rules/delete` | `AuthzGlobalRuleDeleteRequest` | — | ANY_OF default |

See [auth.md](auth.md) for the access-control model these routes manage.

### Global state
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/global/events` | `EventStreamRequest` | `EventStreamMsg` | ANY_OF default |
| POST | `/v1/global/event-stream` | `EventStreamRequest` | stream `EventStreamMsg` | ANY_OF default |
| POST | `/v1/global/exported-config` | — | `ExportedConfigBlob` | ANY_OF default |

`/v1/global/event-stream` is the browser's state feed. The request
(`EventStreamRequest`) carries nothing a client has to remember: the old
`after_seq` tag is reserved, and there is no replay. The first message
carries the sidecars (secrets status; backup status and ingress diagnostics
only for cluster viewers). The second is the opening message: `synced`,
`seq`, and `snapshot`, a `CoreSnapshot{seq, entities}` of everything the
viewer may see, which the client folds into an empty tree. After that every
commit with something visible arrives as one message with its `events` and
the seq the client is now at, and a heartbeat message every five seconds
carries `heartbeat` and the current `seq`. A reconnect, for any reason,
opens again with a fresh snapshot; history is served per entity by the
deployment and status history endpoints, not by the stream. The server
also sends a fresh snapshot on the open connection when the viewer's
visibility changes (below).

A snapshot entry is a `MaterialisedEntity{entity_type, entity_id, entity,
meta}`: one per live entity, one per retained version in version order for
deployments and for values (secrets, configs, assets), and one per pinned
version of a deleted deployment with `meta.deleted` set. It is read from the
materialised tables under the write lock (`pq.Snapshot`), so it is exactly
the state at `seq`.

`/v1/global/events` is the one-shot form for agents and scripts: the same
request, one `EventStreamMsg` holding the snapshot and the sidecars together.

Events are `CoreWriteUpdate{seq, time, actor, mutations}`: one per commit,
with `time` (epoch ms) and `actor` (0 system, negative for the agent of user
`-actor`) shared by every row of the commit. A `CoreMutation` is exactly one
of `create`, `update`, or `delete`, each with `entity_type` (`CoreEntityType`)
and `entity_id`; create and update carry the entity in a `CoreEntity`, whose
field numbers equal the type enum. Every identity payload carries its own
`id` (`Deployment`, `Node`, `Secret`, `Config`, `Asset`, `NetworkPolicy`,
`AuthzRuleTemplate`, `AuthzGrant`, `AuthzGlobalRule`, `ScheduledInstance`,
`Space`, `User`, the directories, and the sessions); the mutation's `entity_id` is a copy
of it kept for routing, since a delete has no payload, and the server stamps
the payload id from the row id wherever it builds or replays a mutation
(`pq.StampEntityID`), so the two always agree. The three authz entities
carry their `id` and their name or subject facts beside a
`spec` (`AuthzRuleTemplateSpec`, `AuthzGrantSpec`, `AuthzGlobalRuleSpec`)
holding the rule content, which the create and update requests take under
`spec`; who wrote them is the envelope `actor`, not a payload field. Observed statuses are entities too
(`SCHEDULED_INSTANCE_STATUS`, `NODE_STATUS`) with `updated_at` as the
producer clock; there is no separate observed message. The wire shape is the same for
every consumer class; the browser class strips `Secret.smk_version`,
`ciphertext`, and `nonce`, `User.credentials`, `AgentSession.token_hash`,
`UserSession.token_hash`, and `SystemConfig.master_password_hash`, and drops
`SECRET_KEYSLOT` mutations entirely (`browserEntity` in the handler).

#### Entity meta

Payloads carry authored facts only. What the server derives from the write
history travels beside the payload as `EntityMeta` (`created_time`,
`updated_time`, `updated_seq`, `updated_actor`, `version`, `spec_version`,
`value_version`, `deleted`) on every live create and update mutation and on
every snapshot entry; a delete mutation carries none. The old payload fields
(`created_time` and `author` on the values, nodes, policies, instances,
grants, and sessions, `version` and `spec_version` on `Deployment`,
`value_version` on `Secret`, `Config`, and `Asset`) are reserved tags. Pins
are unchanged: `ValueRef{id, version}` and the instance's deployment
version are authored references and stay in the payload.

Meta is never stored in the write log. The reducer stamps it from the
materialised rows after it has applied a commit (`pq.rowMeta`, `StampMeta`),
so a live mutation describes the row as it is after that commit: the first
four fields are always set; `version` and `spec_version` are set on
deployments, `value_version` on the three value types, and zero elsewhere;
`deleted` is set only on the snapshot entries of a deleted deployment's
pinned versions. The log replayed as events (`pq.WriteEventsInRange`) carries no meta,
since the log never had it. In a snapshot, the newest entry
of a value carries the identity row's latest write envelope (so after a
rename the newest version shows the rename's seq, time, and actor), while
older versions carry the envelope of the write that produced them; a
deployment version always carries its own write envelope. The fold oracle
in the `state` tests holds the snapshot of the live tables equal, meta
included, to the snapshot of tables rebuilt from the log.

#### Write responses

Every write endpoint that returns an entity returns a `CoreWriteUpdate`:
deployment create and update; node rename, allowed spaces, drain, and
evict; network policy create and update; secret create, set, generate,
rename, and move; config create, set, rename, and move; asset upload, rename,
and move; and the access control creates and update. The update carries the
mutation for the entity the call addressed under the `seq`, `time`, and
`actor` of the commit that last wrote it, filtered and stripped for the
caller the way the stream is (`Handler.written`). A client reads the new id
from `entity_id`, the token for its next `expected_seq` from `seq`, and
`value_version` or `spec_version` from the mutation's `meta`, which the
handler stamps from the row (`StampMeta`). A write that changes nothing returns the entity's current state under the seq
of the commit that last wrote it and consumes no sequence. The response is a
receipt, not a stream message: a client never folds it into its tree, because
the commit may carry further mutations (a scheduler trigger's instances, the
deployments a secret set rolled) that only the stream delivers in order. The
frontend reads responses with `written(update, type)` in `state/tree.js`.

The per-entity list endpoints that returned the retired envelope messages
(`/v1/nodes/list`, `/v1/network-policies/list`, `/v1/secrets/list`,
`/v1/configs/list`, `/v1/assets/list`, `/v1/access/grants/list`) are gone;
the stream is the read surface. `NodeEvent`, `SecretEvent`, `ConfigEvent`,
`AssetEvent`, `NetworkPolicyEvent`, `ScheduledInstanceEvent`, and
`AuthzGrantRecord` are no longer messages: the backend keeps them as row
views (`pq.NodeEvent` and siblings), envelope plus
payload, that never cross the wire. `DeploymentEvent` stays a message because
`ScheduledInstanceState.config` carries it over the cluster protocol.

#### Commit

Every primary writer, authored or observed, calls
`state.Service.Commit(ctx, preLockValidate, mutate)`. `preLockValidate` is an
advisory fast-fail check on the root `pq.Queries` before the lock; it returns
only an error and nothing depends on it for correctness. The store then takes
the write mutex, opens a transaction that reserves the SQLite writer before
reading, reads `global_seq`, and calls `mutate(q, seq)` with the transaction
queries and the candidate `global_seq + 1`. `mutate` re-reads and checks
everything the write depends on (entity and facet counters, never the global
sequence of the last change), reads the clock once and threads that `now`
through every insert so all rows of the commit share one `event_time`, builds
its rows in memory under the candidate sequence, and returns the
`*state.WriteUpdate` (an `apigen.CoreWriteUpdate`) that describes exactly
those rows. The update is built from the values the callback built, never by
reading back:
one `pq.Mutation` per row through the per-entity converters
(`pq.DeploymentMutation`, `pq.NodeMutation`, `pq.SecretMutation`, ...),
assembled with `pq.NewUpdate` and `pq.AppendMutations`, which take `time` and
`actor` from the first mutation. `mutate` returns mutations only and `Commit`
materialises them through `pq.ReduceUpdate` (see The write log);
`statetest.AssertUpdateMatchesRows` checks in tests that the rows agree with
`WriteEventsInRange(seq-1, seq)`. Caller-injected checks
are `func(*pq.Queries) error` parameters on the domain functions invoked
inside `mutate`. `Commit` materialises the writer's mutations
(`q.ReduceUpdate`) before the registered `UpdateTrigger`s run (the scheduler
registers one), so a trigger reads the rows the writer's mutations describe;
a trigger extends the same update and either appends with
`pq.AppendMutations` and lets `Commit` reduce the tail after it returns, or
calls `q.Apply(ctx, update, mutations...)` to append and materialise at once
when its own later reads depend on the write (the scheduler's `publish`).
The transaction's `Queries` counts the mutations it has reduced per update,
so a grown update is reduced once. If the final update has any mutation the
store appends the update to the write log (`write_events` and
`write_event_mutations`, see below), persists the sequence, commits, and
publishes that one update to every subscriber; an empty update commits
without consuming a sequence, writes no log row, and publishes nothing. A failed callback rolls
back, consumes no ids or sequence, and publishes nothing. Callbacks are not
retried. The store holds no in-memory state and `pq` has no caches.

Subscriptions are `state.Subscribe(store, read, project)`: `read` runs under
the write mutex and returns whatever the subscriber starts from (the stream
handler reads its opening events there, so no commit can fall between the
read and the first live update); `project(update) (T, bool)` runs after each
commit under the same mutex and returns what to send. A subscriber whose
channel is full is closed and dropped, and the consumer resubscribes from a
fresh read. The scheduled-instance feed delivers one
`[]ScheduledInstanceState` batch per commit.

The scheduler reads current desired, target and observed rows through the same
transaction and appends immediate target changes there. Startup recovery is
synchronous; acknowledgements and drain deadlines trigger later transactions.
Drain waits derive from persisted event sequences and times, with a fresh
conservative timeout for drains found at startup. Rendering and network delivery
remain outside the transaction. Sidecars retain independent locks and streams.

#### Snapshot and observed state

Space 0 values never reach a client: `authz.SystemSpaceAllows` refuses every
secret, config, and asset request in space 0, so the list filters and the
stream's `entityVisible` drop those rows for every user, and a deployment can
neither be created in space 0 nor reference a space 0 value. That is where
OpenDeploy keeps its own key material, as ordinary secret rows.

Pins are `ValueRef{id, version}` pairs of the stable entity id and
`value_version`; reveal and content download address the same pair. A value
mutation's `meta.value_version` counts the content writes of that entity
and a deployment mutation's `meta.version` and `meta.spec_version` count
its writes and its spec changes, so a consumer needs nothing but the
mutation to count versions. Create, set, rename,
move, and upload return their appended event envelope; lists return latest
live events. A removal is a delete mutation on the stream and an
`event_type = 3` row in the log. The snapshot's retention (every version of
a live value, every retained version of a deployment, including those of a
deleted deployment while instances still pin them) is a function of the
tables, which the log reproduces; the `state` tests hold the live snapshot
equal to the snapshot of a rebuild from the log.

Both observed collections use a nanosecond `updated_at` HLC and hold one
row per parent in `scheduled_instance_status` and `node_status`. An
observed write goes through `Commit` like any other. A report whose clock
is not older than the stored row is published and the reducer upserts the
row (`ON CONFLICT ... WHERE excluded.updated_at >= updated_at`, the HLC
merge); its row carries the commit sequence and the commit's `event_time`.
A report older than the stored row returns an empty update: it is neither
logged nor published, and nothing remembers it. Clients merge observed
values by their own clock, not by sequence, so the sequence on an observed
row only records which commit published it. Observation history is the
log: `ListScheduledInstanceStatusHistorySince` and
`ListNodeStatusHistorySince` read the entity's logged payloads, and the
per-deployment history (`ListScheduledInstanceStatusHistoryForDeployment`)
finds the deployment's instances through their logged creates. An empty
payload with a fresh clock clears the visible status; clients retain its
clock to reject delayed older packets. The snapshot includes the latest
tombstones for the same reason. A status row follows its instance: pruning
an instance deletes its status, and a late report for a pruned instance is
logged but leaves no row (`deleteOrphanScheduledInstanceStatus`).

#### Sidecars, grants, and visibility

Backup, ingress diagnostics, and secrets status publish through their owning
components, without the core lock or sequence. Each sidecar message replaces
that component's value. The stream sends the sidecars in its first message,
before the snapshot; a fresh snapshot on an open connection preserves the
client's sidecars unless a replacement is present.

Agent sessions and user sessions are core collections, not sidecars. Every
create, approval, token claim, status change, and revocation is a commit that
stamps the row's `global_seq` and publishes the session's latest document as
an `AGENT_SESSION` or `USER_SESSION` mutation whose entity id is the row id
of the session's first event; the snapshot carries the newest row of every
session. Both collections are owner-filtered: a session reaches only the
browser of the user who holds it, and `visibleUpdate` drops the rest. The
token hash is stripped by `browserEntity`. Sessions are never deleted, so the
collections are latest-only entities and grow unbounded, deliberately.

#### The write log

`write_events (seq, time, actor)` and `write_event_mutations (seq, idx,
entity_type, entity_id, op, payload)` hold every committed
`CoreWriteUpdate`: one envelope row per consumed `global_seq` and one row per
mutation in publication order, `op` the `AuthzVerb`, `payload` the encoded
`CoreEntity` (NULL for a delete). `Commit` appends them in the same
transaction as the entity rows, from the update it is about to publish, so
the log row and the stream message are the same bytes. The entity tables
are materialised views of this log: what each one retains is whatever its
readers need (the newest row, every version of a live value, the versions a
live instance pins) and it can be regenerated from the log. `asset_store` is
not: it is node-local placement state reconciled from the asset root and S3,
and asset content itself lives outside the database. The `entity_ids`
counters are maintained by the reducer too. `global_seq` is the
counter the log is keyed by. The log is never updated, deleted from, or
compacted. Seq 0 holds the two spaces the schema seeds, written by `pq.Open`
on a fresh database. The log is the only durable truth: `pq.Open` refuses a
database whose log stops short of `global_seq` (a database from before
v0.0.614 must start on that release once, whose backfill filled the log).
The check is the first thing `pq.Open` does, before the schema or any
migration, so a refused database goes back to the release it came from
unchanged.
`pq.WriteEventsInRange(after, upTo)` reads the log back as events for the
rebuild, and the per-entity history readers below read their entity's
logged payloads; `LatestMutation` reads one entity's newest row through the
`(entity_type, entity_id, seq)` index. The snapshot and every other read
come from the entity tables.

Every entity type is a **reduced type**: its tables are maintained by
`pq.Reduce` (`pq/materialise.go` dispatching to a `reduce<Type>` per table
beside its reader), the one place that maps a payload to columns. Their
writers return mutations only (`pq.SecretMutation(meta, id, secret)`,
`pq.SpaceMutation(meta, space)`, `pq.UserMutation(meta, user)`,
`pq.NetworkPolicyMutation(meta, id, policy)`,
`pq.AuthzRuleTemplateMutation(meta, record)`, `pq.AuthzGrantMutation(meta,
id, value)`, `pq.AuthzGlobalRuleMutation(meta, record)`,
`pq.DeleteMutation(meta, type, id)`, ...), allocate ids through
`pq.NextEntityID` (`entity_ids (entity_type, next)`, which the reducer also
moves forward so an id is never reused; builtin template ids 1 and 2 are
fixed and seeded first), and `Commit` runs `ReduceUpdate` before appending
the log row. Shape for the values: an identity row per live entity
(`secrets`, `configs`, `assets` with `space_id`, `directory_id`, `name` or
`key`, the newest `value_version`, the envelope `seq`, `event_time`,
`author` of the last write, and `created_time`, the time of the create,
which the reducer keeps across every later write), a version row per value
write (`secret_versions`, `config_versions`, `asset_versions`, primary key
`(entity id, value_version)`, each with its own envelope; an existing pair
is never rewritten), one row per directory (`value_directories`,
`asset_directories`), and two namespace tables (`value_names`, `asset_keys`:
primary key `(space_id, parent_id, name|key)`, unique `(kind, id)`) that
make sibling uniqueness across secrets, configs, and directories a
constraint. A delete removes the identity, its versions, and its name;
pinned references to a deleted value no longer resolve, which the delete
guards (reference-in-use checks) already prevent. `RebuildFromLog` truncates
every materialised table and replays the whole log in pages, and the state
tests assert it reproduces the live tables; `opendeploy
primary rebuild-tables` runs it as an operator repair while the primary is
stopped. The snapshot emits a value as one entry per version row in
version order carrying the identity's current name, directory, and space
with that version's value; the newest entry's meta carries the identity
row's envelope and `created_time`, older entries the envelope of their
version row. Shape for the latest-only
reduced types: one row per live entity keyed by its id (`spaces`, `users`,
`network_policies`, `authz_rule_templates`, `authz_grants`,
`authz_global_rules`) holding the payload's facts as columns (a document
blob where the payload nests one: the `InternalUser`, the `NetworkPolicy`,
the `AuthzRuleTemplateSpec`, `AuthzGrantSpec`,
`AuthzGlobalRuleSpec`) plus `created_time` and the envelope `seq`,
`event_time`, `author` of the last write; the payloads carry no author, so
the row's `author` is the envelope actor of the write that produced it. The
snapshot emits each as one entry. `GetSpace`, `GetUserRow`, `GetNetworkPolicy`,
`GetAuthzRuleTemplate`, `GetAuthzGrant`, and `GetAuthzGlobalRule` return
`sql.ErrNoRows` for a deleted entity; a writer that needs the history
(`seedGlobalRule`, which must not re-seed a rule an operator deleted) asks
the log (`AuthzGlobalRuleNameEverLogged`). `pq.NetworkPolicyEvent` is the
backend's row view of a live policy, the envelope of its last write (`seq`,
`author`, `event_time`, `created_time`) plus the payload; `AuthzGrantEvent`
is gone.

#### Sessions, resets, keyslots, and the system config

Sessions, Nix store resets, keyslots, and the system config are reduced
rows too: `agent_sessions` and `user_sessions` keyed by an id from
`entity_ids` with `session_id` unique (the id inside the token), one row per
session whose `status` or `revoked_at` the writer rewrites in place under
`Commit`, where the state-machine guards live (a pending agent session is
approved once, an approved one claims its token once, a revoked one stays
revoked); `nix_store_resets` keyed by an id with `repo` unique, a repeat
request an update of the same row; `secret_keyslots` keyed by
`node_id * 256 + kind` (`SecretKeyslotEntityID`), with node eviction
emitting a delete per slot of the node (`NodeSecretKeyslotDeletes`);
`system_config`, a single row with id 1 (`SystemConfigEntityID`) whose
version on the wire is the `seq` of its last write. None of these rows is
garbage collected. Every mutation keeps the whole document, so the write
log alone replays each entity's history.

#### Nodes, deployments, and instances

`nodes` is one row per node keyed by its entity id (`NextEntityID`), with
the operator and reported facts as columns (`roles`, `allowed_spaces`, and
`host_addresses` as JSON lists, `underlay_address`, `wg_public_key`,
`host_addresses_unknown`, `enrollment_requested_at`, `enrolled_time`,
`created_time`) and `identifier` unique. `CurrentNode` is the row joined
with `node_status`. Writers build the next row in memory
(`pq.NewNode` allocates the id and grants every space;
`nodes.appendNodeVersion` applies a change to a `CurrentNode`) and return
`pq.NodeMutation(verb, event)`; nothing is written until `Commit` reduces
it, so a writer that needs the new row after building it uses the value it
built rather than re-reading. `NextEnrollmentRequestedAt` reads the node's
logged payloads so a fresh request stays above every earlier one.
`pq.NodeEvent` is the backend's row view (envelope plus `Node`); a
node's version is the count of its logged writes, which only tests want.
`node_status` now persists `runtime_versions`.

`deployments` is the current row of a live deployment (`space_id`, `name`,
`version`, `spec_version`, `created_time`, envelope) and
`deployment_versions` holds one row per retained version
(`(deployment_id, version)` primary key, `spec_version`, the `Deployment`
blob, each row with its own envelope, never rewritten). The reducer derives
`version` and `spec_version` itself when it folds a deployment write
(`deploymentVersionFacts`: a new row is version 1, every later write
bumps `version`, and `spec_version` bumps when the spec differs from the
previous version's by `DeploymentSpecsEqual`), so the log never carries
them. A version is
retained while it is the current one or a retained scheduled instance pins
it; a deleted deployment has no current row and keeps only its pinned
versions. Every read goes through `deployment_versions`:
`GetDeploymentEventByVersion` for a pin, `GetLatestDeploymentEvent` and
`ListActiveDeployments` joined with `deployments` (both return nothing for
a deleted deployment, which callers already treated as absent), and
`scanDeploymentEvent` sets `event_type` create for version 1 and update
otherwise. History comes from the log: `ListDeploymentEvents` is the
deployment's logged payloads in order with a delete rendered as the
previous document under `event_type` delete, and `ListDeletedDeploymentEvents`
is one such tombstone per deleted deployment, newest deletion first.
Writers are builders: `pq.DeploymentCreateEvent` (pure),
`q.DeploymentUpdateEvent` (reads the current row, `ErrDeploymentUnchanged`
when no facet differs),
`q.DeploymentDeleteEvent` (the current row under the delete envelope, whose
mutation is a delete); `BuildDeploymentUpdateEvent` serves the reference
rewrite. `DeploymentEvent` reserves the facet counters and `event_id` (tags
2 to 5 and 8 to 12, 14, 20, 21); the row's `version` and `spec_version`
are meta on the wire, and space, name, and scheduling changes bump
`version` alone.

`scheduled_instances` is one row per retained instance (`deployment_id`,
`deployment_version`, `deployment_spec_version`, `node_id`,
`instance_ordinal`, `space_id`, `state`, `created_time`, envelope) with the
`(deployment_id, instance_ordinal, id)` index. Retention runs inside
`ReduceUpdate` after the mutations of an update are reduced, once per
deployment the update touched (`retainDeployment`): a finalized instance
goes when its deployment has no current row, when a non-final instance
exists at its ordinal, or when a newer final exists at its ordinal; a pruned
instance takes its status row with it; then every version of the
deployment that is neither current nor pinned goes. Retention is a function
of table state alone, so the order of mutations within a commit does not
matter and the snapshot equals the retained fold of the full log.
Builders are `pq.NewScheduledInstanceEvent(seq, inst, state, at)` (a create;
the id comes from `NextScheduledInstanceID`) and
`pq.ScheduledInstanceTransition(seq, current, state, at)`, published as
`pq.ScheduledInstanceMutation(verb, event)`. `ListRetainedScheduledInstances`
is every retained row and replaces the old per-ordinal computation; a
transition of an instance retention already pruned has nothing to read
(`GetScheduledInstance` returns `sql.ErrNoRows`), which cannot happen on the
live path because the scheduler only transitions non-final rows it read in
the same commit. A commit that finalizes an instance while a newer one
holds its ordinal (a rollover, a deployment delete) prunes the finalized
row before any subscriber reads it, so the scheduled-instance subscription
(`MustFetchScheduledSnapshotAndSubscribe`, the feed behind the cluster
sessions) falls back to `PrunedScheduledInstanceState`: the instance from
the commit's own payload, its pinned version from the tables while another
instance pins it and from the write log otherwise, and the status from the
commit or the log. Without it a worker never hears that the instance ended
and keeps its ingress routes alive. `pq.ScheduledInstanceEvent` is the backend's row view (envelope plus
`ScheduledInstance`); the message of that name is gone.

The snapshot (`pq.Snapshot`, `pq/snapshot.go`) reads every type from its
tables in a fixed type order: a deployment as one entry per retained
version row in version order, each with that row's envelope and the
version facts, and with `meta.deleted` set on the pinned versions of a
deleted deployment, whose `updated_*` is then the log's delete row; a value
as one entry per version row; an instance, node, status, and every
latest-only type as one entry with the row's envelope. Six tables that had
no create time (`spaces`, `system_config`, `nix_store_resets`,
`secret_keyslots`, `node_status`, `scheduled_instance_status`) carry a
`created_time` column, which the rebuild below fills from each entity's
first logged write (the rebuild also moves the session `created_at` columns
from seconds to milliseconds). No table is an append-only event log any
more: the last five (`deployment_event_log`,
`scheduled_instance_event_log`, `node_event_log`, `node_status_log`, and
the old `scheduled_instance_status` history) were replaced on 2026-10-01.
On the first start of a v0.0.614 database `pq.Open` runs, in this order:
the write-log completeness check above; `backupLegacyDatabase`, which
copies the file to `<db>.pre-materialise` with `VACUUM INTO` while the old
tables are present and keeps an existing copy rather than overwrite it; the
rename of the old status history to `scheduled_instance_status_log`, since
the new table takes its name; the schema; and `materialiseLegacyTables`
(`pq/migrate_materialise.go`), which in one transaction rebuilds the
materialised tables from the log, checks them against the old tables'
rows, and drops the old tables. A mismatch refuses to start, naming the
copy. The copy is the way back to v0.0.614 (restore it over the database
file; writes made on v0.0.615 are lost), and the operator deletes it once
the cluster is staying.

Grants are `AUTHZ_GRANT` entities whose payload (`AuthzGrant`) holds the id,
subject user, template, and bindings or direct rule (`spec`),
with the granting operator as the envelope `actor`; the
snapshot holds the latest live grants and a revocation is a delete mutation.
The store publishes `apigen.CoreWriteUpdate` directly, with no internal
routing wrapper.

The handler filters each update by the connection's current permissions
(`visibility.go`): it observes every mutation's identity (deployment to space,
instance to deployment, node to allowed spaces, value to space, policy, grant
to user) whether or not it is visible, forwards the creates and updates the
viewer may see, and forwards a delete only when it sent that entity. In a
snapshot an entity's entries stand or fall together: its newest entry
decides, so a value moved out of the viewer's space ships
none of its history and one moved in ships all of it, and the deleted
entries of a pinned deployment count as never sent. A grant
mutation for the connected user, any template, global rule, space, or node
allow-list change, an entity space move, a network policy whose visibility
flips, or a delete of a grant it never saw schedules a fresh snapshot on
the open connection after a 200 ms debounce; updates arriving during the
debounce are skipped because the snapshot supersedes them. Overflow closes the browser
subscription and forces reconnect; internal typed adapters resubscribe and
reconcile from a fresh read, including missed deletes and finalized
placements.

### Deployments
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/deployments/get` | `DeploymentGetRequest` | `DeploymentGetResponse` | ANY_OF default |
| POST | `/v1/deployments/create` | `DeploymentCreateRequest` | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v2/deployments/update` | `DeploymentUpdateRequestV2` | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v1/deployments/delete` | `DeploymentDeleteRequest` | — | ANY_OF default |
| POST | `/v1/deployments/recently-deleted` | `RecentlyDeletedDeploymentsRequest` | `RecentlyDeletedDeployments` | ANY_OF default |
| POST | `/v1/deployments/history` | `DeploymentHistoryRequest` | `DeploymentHistory` | ANY_OF default |
| POST | `/v1/deployments/versions` | `DeploymentVersionsRequest` | `DeploymentVersions` | ANY_OF default |
| POST | `/v1/deployments/log-query` | `LogQueryRequest` | `LogQueryResponse` | ANY_OF default |
| POST | `/v1/deployments/run-report` | `DeploymentRunReportRequest` | `DeploymentRunReport` | ANY_OF default |
| POST | `/v1/deployments/prepare-output` | `PrepareOutputRequest` | stream `PrepareOutputChunk` | ANY_OF default |
| POST | `/v1/repos/validate` | `RepoValidateRequest` | `RepoValidateResponse` | ANY_OF default |

`/v1/deployments/get`, `/v1/deployments/history`, and
`/v1/deployments/recently-deleted` return deployments as
`DeploymentRecord{deployment, meta}`: the `Deployment` payload beside the
`EntityMeta` the stream would attach to that version (`pq.DeploymentRecord`
over the backend's `DeploymentEvent` row view). A history entry is either a
record or a status, newest first; the delete of a deployment is the last
version's record with `meta.deleted` set and `updated_*` the delete's
envelope. The browser folds a record into the same entry shape as a stream
mutation (`deploymentFromRecord` in `state/tree.js`). `DeploymentEvent` is
no longer on the REST surface; it remains the cluster wire's pinned
version in `ScheduledInstanceState.config`.

`/v2/deployments/update` applies exactly one kind of change per request — `version_only_update` (deploy a version, implies running), `running_only_update` (start/stop at the current version; stop preserves the version), `spec_update` (full spec replacement, workload state included), `assigned_space_update` (space move), or `restart_update` (replace the running placement with the definition unchanged; rejected for a stopped workload and for the opendeploy self-deployment) — guarded by `expected_seq`, the seq of the deployment's last mutation as the caller saw it: the write is rejected when the stored row's seq is greater, and 0 skips the check. The same token guards `/v1/deployments/delete`, `/v1/nodes/evict`, enrollment accept, and `/v1/network-policies/update`. An update that changes nothing (same spec, name, space, and scheduling) returns the current event and consumes no seq; a restart is a `scheduling.generation` bump and nothing else. cleanproto has no `oneof`, so the kinds are plain optional fields and the handler rejects anything but exactly one.

`/v1/deployments/log-query` is a one-shot structured log search over a single deployment's stored logs (parquet archive plus WAL tail): it returns the newest matching parsed records (capped at 10k), a per-level histogram over the full range, the total match count, and per-field sampled value stats (top-10 values, coverage, and an other bucket over the newest 5k matched records — this feeds the sidebar with no extra request), all in one response. The primary proxies the request over the cluster session to the node hosting the deployment; the node builds the complete response and sends it back as a single message. `deployment_id = 0` with `target_node_id` addresses a node's system log, which is the log of the opendeploy system deployment on that node and is authorized as that deployment's `view_logs` in the system space. The endpoint does not tail live output. See the "Search API sketch" section of `docs/future-work/logmanager-implementation-plan.md` for the design rationale.

`/v1/deployments/run-report` summarizes one run of a scheduled instance: placement identity (deployment version, node, instance ordinal), started/stopped times, the container's exit code, and the last 20 log lines. Runs have no stored entity — the report is reconstructed from the instance's append-only status history: a run's rows are those with `number_of_restarts = run - 1`, its start is `last_restart_at`, and its stop is the `updated_at` HLC of the run's first STOPPED/CRASHED row, which also carries `exit_code`. The exit code is absent when the runner never observed the exit (rows written before the field existed, pre-spawn failures, the internal systemd runner, or an exit during an agent restart); the stop time is the status-write time, so it lags the true exit in the agent-restart case. Log lines are fetched only for finished runs, via an internal log query bounded to the run's time window, filtered by run and instance ordinal, and routed to the scheduled instance's node (which can differ from the deployment config's node during a cross-node move). Log fetch failures and gaps are reported in `warnings` rather than failing the request. Authorized with the same view-logs verb as `/v1/deployments/log-query`.

`/v1/deployments/prepare-output` streams raw prepare/build output chunks for a deployment spec version. A request with `spec_version=0` resolves to the latest known prepare status and tails while preparation is still active.

`/v1/deployments/recently-deleted` lists the final version of the most recently deleted deployments as records with `meta.deleted` set, newest deletion first, so the UI can seed a new deployment from one. Deletion is a delete mutation in the log, so these are read from the write log. `limit` defaults to 25 and is clamped to 200 — the log is never pruned, so the listing must stay bounded. Internal `opendeploy` deployments are omitted because they are recreated by the primary rather than through `/v1/deployments/create`.

OpenDeploy self-updates go through plain `/v2/deployments/update` calls, one per node. The Web UI's system-deployment upgrade overlay orchestrates the rollout client-side: it updates secondaries one at a time, waits for each node's runner to report the new version, upgrades the primary last, and halts on the first failure. There is no server-side bulk-upgrade endpoint.

### Metrics
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/metrics/query` | `MetricsQueryRequest` | `MetricsQueryResponse` | ANY_OF default |
| POST | `/v1/metrics/latest` | `MetricsLatestRequest` | `MetricsLatestResponse` | ANY_OF default |

`/v1/metrics/query` returns one deployment's container resource metrics rolled up onto an aligned time grid. The request carries the deployment, a `[time_start, time_end)` range (default the last hour, capped at 92 days), an optional `step_ms` (the server picks a step from a 10 s to 24 h ladder targeting about 300 buckets when it is 0 or below the 10 s sampling interval), optional `scheduled_instance_id` / `spec_version` / `run` scope, and the metric names wanted (empty = all 55). The response is a `MetricsSeries` per (run, metric): counters (`cpu_*_usec`, `io_*`, `net_*`, `mem_oom*`, `psi_*_total_usec`) come back as per-second rates, differenced between consecutive raw samples of the same run and averaged into each bucket weighted by overlap, so a counter reset drops one pair rather than producing a negative rate; gauges and kernel averages are the bucket mean; a bucket with no data is `NaN` on the protobuf wire and `null` in the JSON rendering (`encoding/json` rejects NaN, so `MetricsSeries` carries a hand-written marshaller in `apigen/metrics_ext.go` that maps NaN to `null` and back). The primary pins the step, fans the request out to every node that holds a scheduled instance of the deployment (the configured node plus any node still carrying one during a move), and concatenates the series; a node that fails becomes a warning unless it was the only one. Authorized with the same view-logs verb as `/v1/deployments/log-query`. Storage and the raw sample schema are described in `docs/future-work/container-metrics-implementation-plan.md`.

`/v1/metrics/latest` is the live overview: the last sample per running container on every connected node, each with the per-second rate of every counter against the previous sample of that run. The primary fans out to all connected workers with a 5 s budget, folds a failed node into `warnings`, and filters entries to deployments the caller may view logs for; a container whose deployment is unknown (deleted) is dropped.

### Nodes
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/nodes/rename` | `NodeRenameRequest` | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v1/nodes/allowed-spaces` | `NodeAllowedSpacesRequest` | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v1/nodes/drain` | `NodeDrainRequest` | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v1/nodes/evict` | `NodeEvictRequest` | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v1/nodes/exposure` | `NodeExposureRequest` | `NodeExposure` | ANY_OF default |
| GET | `/v1/nodes/enrollments/info` | — | `NodeEnrollmentInfo` | ANY_OF default |
| POST | `/v1/nodes/enrollments/list` | — | `EnrollmentRequestList` | ANY_OF default |
| POST | `/v1/nodes/enrollments/accept` | `EnrollmentAcceptRequest` | `EnrollmentRequestStatus` | ANY_OF default |

Deployment placement is constrained by each node's allowed-spaces list; `/v1/nodes/allowed-spaces` replaces it wholesale and is rejected if it would strip a space out from under deployments already running on that node. See [Deployments](../product/deployments.md) for the policy.

`/v1/nodes/drain` (`node:update`) toggles a secondary between `NORMAL` and `DRAINING`. Draining is a placement cordon only: the node stays a member, its workloads keep running, and creating a deployment on it or moving a running workload onto it is rejected with `node_draining` (`409 Conflict`). Updates to a deployment already on the node that leave its workload stopped still go through.

`/v1/nodes/evict` (`node:delete`) removes a secondary from the cluster in one commit: the node moves straight from any member status to `EVICTED`, every non-final scheduled instance on it is finalized with a status tombstone, its per-node system deployments (`opendeploy`, `opendeploy-net`) are deleted, and its observed status is tombstoned. The request carries the node version the operator reviewed (`node_version_changed` on mismatch). User deployments pinned to the node are not moved or deleted; without `force` their presence rejects the eviction with `node_has_deployments` (`409 Conflict`), with `force` they stop and stay pinned until the operator retargets them. The primary cannot be drained or evicted (`node_is_primary`). The scheduler never places on an evicted node, the netmap omits it, and every cluster endpoint authorizes only member statuses, so an evicted node's still-valid client certificate buys it nothing. Eviction is irreversible: the identifier is refused at enrollment forever (`enrollment_identifier_evicted`, `409 Conflict`), and the only way back is a reinstall, which generates a new key and therefore a new identifier.

`/v1/nodes/exposure` (`node:delete`) reports what an evicted or member node has ever been given, computed from its scheduled-instance history: the deployments that ran on it, the secret and config versions their env referenced, the deployments whose issued TLS material it held, the ACME hostnames whose certificates it served, and whether it was handed the GitHub token. It is the rotation checklist for a machine that is no longer trusted.

Node events split lifecycle (`value.status`, `enrollment_requested_at`),
operator fields (`value.operator`), and reported facts (`value.reported`).
`NodeStatus` contains connection information, remote address, binary version,
and a monotonic nanosecond `updated_at`. Both enrollment and cluster hellos send the
same `NodeReported` bundle. `host_addresses_unknown` preserves the previous
inventory when enumeration fails; a successful empty list clears it. Identical
reports append nothing. Acceptance checks
`expected_seq`, writes one event, and clears the pending timestamp;
first cluster hello therefore adds no trailing node events. An unaccepted
session disconnect cancels its request, and a session expires after ten minutes.
For admitted nodes either outcome clears the request without changing membership.
The flat hello fields and the legacy `node_statuses` table were removed after the
v0.0.587 rollout; a hello without `reported` is rejected.

Workers use `EnrollmentV1` only when local cluster CA/cert/key material is missing. The enrollment listener is HTTPS using the primary server certificate. Because workers do not yet have a trust root, secondary installs pin the enrollment listener's `sha256:` SPKI fingerprint from authenticated `GET /v1/nodes/enrollments/info`; the worker verifies the presented TLS certificate matches that fingerprint before sending its CSR. In production, the public enrollment listener also applies the same generated-mux middleware approach as the web UI: per-client-IP request admission is limited to a burst of 5 and a refill rate of 0.2 requests/second. Before the first hello the worker generates its TLS private key, writes it to `/var/lib/opendeploy/tls/node.key`, and derives its `requesting_machine_id` from it: the lowercase hex SHA-256 of the key's SubjectPublicKeyInfo, 64 characters, which also fits the X.509 CN length bound. Every retry and restart of a pending enrollment therefore presents the same key and identifier, and the worker logs the identifier when enrollment starts so an operator can compare it against the pending request in the UI. The worker sends the identifier plus a PEM CSR whose CN is the identifier, then keeps the stream open until an operator accepts the request. The primary verifies the CSR signature, that the CN equals the reported identifier, and that the identifier equals the SHA-256 of the CSR's public key; a hello that fails any of these is rejected with `enrollment_invalid_csr`. Because the identifier is bound to the key, nobody without the private key can produce a valid hello for it, a different key is a different identifier and lands on its own request, and the primary keeps no server-side pin. A hello that reconnects with the same identifier replaces the earlier stream for that request. A hello whose identifier already belongs to an enrolled node is rejected with `enrollment_identifier_enrolled` (`409 Conflict`) and logged with the peer address; because `ApiErr.internal_err` never crosses the wire the worker treats any 409 on the hello stream as a permanent rejection and exits instead of retrying. The requesting address shown to the operator is the TCP peer address; forwarded headers are ignored. There is no reset or re-enrollment path for a member; a member that loses its TLS material stays stranded, and an evicted member is refused with `enrollment_identifier_evicted` (`409 Conflict`) forever, so in both cases the machine is reinstalled as a fresh secondary and enrolls under the new key's identifier. Nodes enrolled before identifiers were key-derived keep their UUID identifiers and certificates; they keep working but can never re-enrol. The CSR CN, worker certificate CN, and `NodeEvent.value.reported.identifier` are that stable identifier. Deployment placement, authorization, lookup, and duplicate detection use `node_id`; the operator-selected worker name is mutable display metadata only. Acceptance signs the CSR with the primary's internally stored cluster CA key and returns only the CA certificate and worker certificate; the private key never leaves the worker. By default the worker writes them to `/var/lib/opendeploy/tls/ca.crt`, `/var/lib/opendeploy/tls/node.crt`, and `/var/lib/opendeploy/tls/node.key`, then reconnects to `OpsagentClusterV1` over mTLS. The cert files are written `0644`; the private key is written `0600`.

### Spaces
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/spaces/create` | `SpaceSetRequest` | `Space` | ANY_OF default |
| POST | `/v1/spaces/update` | `SpaceSetRequest` | `Space` | ANY_OF default |
| POST | `/v1/spaces/delete` | `SpaceDeleteRequest` | — | ANY_OF default |

### Secrets
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/secrets/create` | `SecretCreateRequest` | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v1/secrets/set` | `SecretSetRequest` | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v1/secrets/generate` | `SecretGenerateRequest` | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v1/secrets/rename` | `SecretRenameRequest` | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v1/secrets/move` | `SecretMoveRequest` | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v1/secrets/reveal` | `SecretRevealRequest` | `SecretRevealResponse` | ANY_OF default |
| POST | `/v1/secrets/delete` | `SecretDeleteRequest` | — | ANY_OF default |
| POST | `/v1/secrets/status` | — | `SecretsStatusResponse` | ANY_OF default |
| POST | `/v1/secrets/rotate-recovery-code` | — | `SecretRecoveryCodeResponse` | ANY_OF default |
| POST | `/v1/secrets/unlock` | `SecretUnlockRequest` | `SecretsStatusResponse` | ANY_OF default |

User-managed configs and encrypted secrets are immutable versioned rows. Setting an existing secret appends value version `vN`. Setting a config appends `vN` when the value differs from the current one; setting a config to its current value is a no-op that returns the current event and consumes no global seq; settings refs and deployment env refs pin exact values with `ValueRef{id, version}` pairs (stable entity id plus value version) in `ConfigRef.ref`, `SecretRef.ref`, `EnvVarValue.config`, and `EnvVarValue.secret`. Rename appends an event with the new display name and unchanged value facet. Delete soft-deletes the whole group and is rejected while any settings or deployment config still references the entity.

`SecretSetRequest` and `ConfigSetRequest` can atomically roll deployment env refs to the new immutable row. With `update_referencing_deployments`, `referencing_deployments` lists every referencing deployment as `DeploymentExpectedSeq{deployment_id, expected_seq}`. The backend derives the references from current stored specs, rejects stale, duplicate, missing, or extra entries, then commits the new value row and all deployment config/history versions in one transaction. A deployment already pinned to the resulting version is left unchanged, so a no-op config set with the flag still repoints deployments pinned to older versions and writes nothing else.

`POST /v1/secrets/reveal` is the only user-facing API that returns decrypted secret plaintext. It takes `SecretRevealRequest{secret_id, version}`, the same pair a reference pins, for exact-version reveal; list/state APIs return metadata only.

`POST /v1/secrets/generate` writes a secret value the caller never sees. It supplies a name and a generator specification, never a value, and receives only metadata, so a caller holding `secret : create` and nothing more — an agent session, under the builtin templates — can wire a fresh credential into a deployment without the plaintext reaching anywhere it can observe. It is create-only: an existing name is rejected, so it can never bury a value the caller cannot read back.

### Configs
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/configs/create` | `ConfigCreateRequest` | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v1/configs/set` | `ConfigSetRequest` | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v1/configs/rename` | `ConfigRenameRequest` | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v1/configs/move` | `ConfigMoveRequest` | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v1/configs/delete` | `ConfigDeleteRequest` | — | ANY_OF default |

### Value directories
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/value-directories/list` | — | `ValueDirectoryList` | ANY_OF default |
| POST | `/v1/value-directories/create` | `ValueDirectoryCreateRequest` | `ValueDirectory` | ANY_OF default |
| POST | `/v1/value-directories/move` | `ValueDirectoryMoveRequest` | `ValueDirectory` | ANY_OF default |
| POST | `/v1/value-directories/rename` | `ValueDirectoryRenameRequest` | `ValueDirectory` | ANY_OF default |
| POST | `/v1/value-directories/delete` | `ValueDirectoryDeleteRequest` | — | ANY_OF default |

The shared secrets/configs folder tree; see [secrets.md](secrets.md).

### Assets
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| GET | `/v1/assets/content` | `asset_id` and `version` query params | raw content bytes | ANY_OF default |
| POST | `/v1/assets/upload` | raw file body, `asset_id` or `key` plus `space_id`/`directory_id`/`unique_key` query params | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v1/assets/rename` | `AssetRenameRequest` | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v1/assets/move` | `AssetMoveRequest` | `CoreWriteUpdate` | ANY_OF default |
| POST | `/v1/assets/delete` | `AssetDeleteRequest` | — | ANY_OF default |

Assets are versioned file blobs stored as an identity row plus one row per content version (`assets`, `asset_versions`), surfaced on the stream as `Asset` payloads with `fs`, `space_id`, and content sha256/size. The stream's value history provides the space and content facet revisions. `/v1/assets/upload` is the single write path for content: `?asset_id=` appends the next content version of that asset, `?key=` creates a new asset. Rename appends an event without changing the content facet or existing pins and rejects an existing destination key. List and state stream APIs carry only `AssetEvent` metadata; content bytes are streamed exclusively by `GET /v1/assets/content?asset_id=N&version=V`, the same pair a spec pins. Every event also carries the content's `storage_key`, the name of its local file and S3 object. Content of every size uses local primary storage while Backup is disabled and S3 while Backup is enabled; changing Backup starts an asynchronous placement transition. Storage placement is transparent to these asset endpoints. Workers stream required asset blobs on demand over the mTLS cluster asset endpoint during preparation. See [Assets](assets.md) for storage modes, transition status, retention, restore, and compatibility.

### Asset directories
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/asset-directories/list` | — | `AssetDirectoryList` | ANY_OF default |
| POST | `/v1/asset-directories/create` | `AssetDirectoryCreateRequest` | `AssetDirectory` | ANY_OF default |
| POST | `/v1/asset-directories/move` | `AssetDirectoryMoveRequest` | `AssetDirectory` | ANY_OF default |
| POST | `/v1/asset-directories/rename` | `AssetDirectoryRenameRequest` | `AssetDirectory` | ANY_OF default |
| POST | `/v1/asset-directories/delete` | `AssetDirectoryDeleteRequest` | — | ANY_OF default |

The per-space asset folder tree; see [Assets](assets.md).

### Cluster transport (mTLS listener)
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| GET | `/v1/cluster/github-credentials` | — | `GithubCredentials` | NO_AUTH |
| GET | `/v1/cluster/asset?asset_id=<id>&version=<n>` | query params | raw asset bytes with `X-Opsagent-Asset-*` headers | NO_AUTH |
| GET | `/v1/cluster/secrets` | `ClusterSecretsRequest` | `ClusterSecretsResponse` | NO_AUTH |
| GET | `/v1/cluster/configs` | `ClusterConfigsRequest` | `ClusterConfigsResponse` | NO_AUTH |
| GET | `/v1/cluster/issued-tls` | `ClusterIssuedTLSRequest` | `ClusterIssuedTLSResponse` | NO_AUTH |
| GET | `/v1/cluster/renew-certificate` | — | `ClusterRenewCertificateResponse` | NO_AUTH |
| POST | `/v1/cluster/connect` | stream `MsgToPrimary` | stream `MsgToSecondary` | NO_AUTH |

Cluster secrets/configs requests carry `ValueRef{id, version}` pairs. The primary authorizes those pairs against the deployment refs allowed for the requesting worker, decrypts/fetches only those values, and the worker keeps the plaintext values in memory and in its encrypted `local_runtime_inputs` cache.

`/v1/cluster/connect` is the long-lived bidirectional worker session. HTTP/2
request and response bodies contain unsigned-varint-length-prefixed protobuf
frames. The primary sends the cluster network info, the latest targeted
`ClusterNetMap`, and the deployment snapshot at session start. Later complete
network maps use latest-value coalescing rather than queueing obsolete versions.
Workers send durable `NetMapStatus` acknowledgements on the request stream.

Eviction reaches a connected worker as a final `MsgToSecondary` with `evicted`
set, after which the primary ends the stream; a worker that reconnects later, or
that was offline when it was evicted, is rejected on `/v1/cluster/connect` with
`node_evicted` (`410 Gone`) before the first frame. Only that status carries the
meaning (`ApiErr.internal_err` never crosses the wire), so the worker treats an
`evicted` frame or a 410 on connect, and nothing else, as eviction: it writes an
`evicted` marker in its data directory, finalizes every cached scheduled
instance so the operator tears the workloads down, force-removes any container
still running after a grace period, then deletes its cached database, machine
key, cluster TLS material, issued TLS material, WireGuard key and rendered net
state, and exits. Volumes and logs stay on disk. With the marker present the
secondary refuses to start until it is reinstalled. Any other failure, including
a plain 403, keeps the normal reconnect loop and never wipes anything.

Cluster sessions use a single protocol version, `apigen.ClusterProtocolVersion`
(bumped on any wire-incompatible change). Workers require the primary's
version marker before applying state, and the primary cancels sessions whose
worker hello reports a different version. A mismatch retries after the normal
reconnect delay.

### Enrollment bootstrap (enrollment listener)
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/enrollment/request` | stream `EnrollmentSecondaryMsg` | stream `EnrollmentPrimaryMsg` | NO_AUTH |

## Adding new endpoints

1. Add the RPC to the `api-contract/*_service.proto` file for the listener it belongs on. Request/response message types go in the entity's `api-contract/model_<entity>_operations.proto`; if the endpoint returns a clean data model shape directly, define it in `api-contract/model/<entity>.proto` instead.
2. Run `bash api-contract/proto_generate.sh`.
3. Implement the handler method in the matching package under `backend/app/primary`: `webuihandler`, `clusterhandler`, or `enrollmenthandler`.
4. The JS client method is generated automatically in `frontend/src/capi/capi.js`.
