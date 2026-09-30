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
- Write handlers go through `state.Service.Commit(ctx, preLockValidate, mutate)` (see [Commit](#commit)): every check-then-write sequence (deployments, secrets, configs, assets, cluster settings) re-reads its dependencies and performs its row writes on the transaction-bound `pq.Queries` inside `mutate`, and returns the `CoreWriteUpdate` the store publishes. Domain logic lives in `app/primary/domain/<name>` packages (`deployments`, `scheduledinstances`, `nodes`, `networkpolicies`, `assets`, `secrets`, `values`, `authz`, `user_event_log`, `agentsessions`, `systemconfig`); handlers call those functions or `pq.Queries` directly. Deployment endpoints validate in two layers: pure shape rules before the commit, and a per-operation validator inside `mutate` (`inLockValidateDeploymentCreate` / `Update` / `Delete` in `domain/deployments/validate_layers.go`). Authz and network I/O (nix source verification) run before the commit; the `expected_seq` check closes the gap. Lock order: the asset operation lock (where asset file operations are involved) precedes `Mu`; subsystem locks (secrets manager, config service) nest strictly inside `Mu`.

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
| POST | `/v1/access/rule-templates/create` | `AuthzRuleTemplateCreateRequest` | `AuthzRuleTemplateRecord` | ANY_OF default |
| POST | `/v1/access/rule-templates/update` | `AuthzRuleTemplateUpdateRequest` | `AuthzRuleTemplateRecord` | ANY_OF default |
| POST | `/v1/access/rule-templates/delete` | `AuthzRuleTemplateDeleteRequest` | — | ANY_OF default |
| POST | `/v1/access/grants/list` | — | `AuthzGrantList` | ANY_OF default |
| POST | `/v1/access/grants/create` | `AuthzGrantCreateRequest` | `AuthzGrantRecord` | ANY_OF default |
| POST | `/v1/access/grants/delete` | `AuthzGrantDeleteRequest` | — | ANY_OF default |
| POST | `/v1/access/global-rules/list` | — | `AuthzGlobalRuleList` | ANY_OF default |
| POST | `/v1/access/global-rules/create` | `AuthzGlobalRuleCreateRequest` | `AuthzGlobalRuleRecord` | ANY_OF default |
| POST | `/v1/access/global-rules/delete` | `AuthzGlobalRuleDeleteRequest` | — | ANY_OF default |

See [auth.md](auth.md) for the access-control model these routes manage.

### Global state
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/global/events` | `EventStreamRequest` | `EventStreamMsg` | ANY_OF default |
| POST | `/v1/global/event-stream` | `EventStreamRequest` | stream `EventStreamMsg` | ANY_OF default |
| POST | `/v1/global/exported-config` | — | `ExportedConfigBlob` | ANY_OF default |

`/v1/global/event-stream` is the browser's state feed. The request carries
`after_seq`, the last seq the client applied (0 for a fresh client). The
first message carries the sidecars (secrets status; backup status and ingress
diagnostics only for cluster viewers). The second is the opening message:
`synced`, `seq`, and `events`, with `reset` set when the server bootstrapped
instead of replaying. After that every commit with something visible arrives
as one message with its `events` and the seq the client is now at, and a
heartbeat message every five seconds carries `heartbeat` and the current
`seq`. Every message carries `seq`, heartbeats included, and the client
resumes from the last one it saw.

The server replays when it can: `after_seq` between 1 and the current seq,
at most 10,000 commits behind, and nothing written since `after_seq` that
could change what the viewer sees (`pq.VisibilityChangesSince`: a template,
global rule, space, node, or network policy row, a grant row for the user, or
a deployment, secret, config, or asset row that moved it to another space).
Otherwise it bootstraps: the events are the compacted history
(`pq.BootstrapMutations`) sent with `reset`, and the client drops what it
holds before folding them. Replay events are exactly the rows in
`(after_seq, seq]` grouped by commit (`pq.MutationsInRange`, `pq.Events`),
filtered per viewer.

`/v1/global/events` is the one-shot form for agents and scripts: the same
request, one `EventStreamMsg` holding the opening events and the sidecars
together.

Events are `CoreWriteUpdate{seq, time, actor, mutations}`: one per commit,
with `time` (epoch ms) and `actor` (0 system, negative for the agent of user
`-actor`) shared by every row of the commit. A `CoreMutation` is exactly one
of `create`, `update`, or `delete`, each with `entity_type` (`CoreEntityType`)
and `entity_id`; create and update carry the entity in a `CoreEntity`, whose
field numbers equal the type enum. Observed statuses are entities too
(`SCHEDULED_INSTANCE_STATUS`, `NODE_STATUS`) with `updated_at` as the
producer clock; there is no separate observed message. The compacted
bootstrap keeps every row of every live secret, config, and asset (the value
history), a deployment's newest row plus every version a retained instance
pins (and the delete of a deleted deployment while an instance still pins
it), non-final instances plus the newest final per ordinal without a live
one, and the newest live row of every other type, with the first retained
mutation per entity promoted to a create. The wire shape is the same for
every consumer class; the browser class strips `Secret.smk_version`,
`ciphertext`, and `nonce`, `User.credentials`, `AgentSession.token_hash`,
`UserSession.token_hash`, and `SystemConfig.master_password_hash`, and drops
`SECRET_KEYSLOT` mutations entirely (`browserEntity` in the handler).

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
through every insert so all rows of the commit share one `event_time`, owns
every database write including private payloads, stamps the candidate
sequence on the rows it writes, and returns the `*state.Update` (an
`apigen.CoreWriteUpdate`) that describes exactly those rows. The update is
built directly from the values the callback inserted, never by reading back:
one `pq.Mutation` per row through the per-entity converters
(`pq.DeploymentMutation`, `pq.NodeMutation`, `pq.SecretMutation`, ...),
assembled with `pq.NewUpdate` and `pq.AppendMutations`, which take `time` and
`actor` from the first mutation. Each mutate function is responsible for the
correctness of its update and code review is what keeps it so;
`statetest.AssertUpdateMatchesRows` holds a published update equal to
`pq.Events(MutationsInRange(seq-1, seq))` in tests. Caller-injected checks
are `func(*pq.Queries) error` parameters on the domain functions invoked
inside `mutate`. Registered `UpdateTrigger`s (the scheduler registers one) run
inside the same transaction and extend the update. If the final update has any
mutation the store sets its `seq`, appends the update to the write log
(`write_events` and `write_event_mutations`, see below), persists the
sequence, commits, and publishes that one update to every subscriber; an
empty update commits without consuming a sequence, writes no log row, and
publishes nothing. A failed callback rolls
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

#### Bootstrap and observed state

Space 0 values never reach a client: `authz.SystemSpaceAllows` refuses every
secret, config, and asset request in space 0, so the list filters and the
stream's `entityVisible` drop those rows for every user, and a deployment can
neither be created in space 0 nor reference a space 0 value. That is where
OpenDeploy keeps its own key material, as ordinary secret rows.

Pins are `ValueRef{id, version}` pairs of the stable entity id and
`value_version`; reveal and content download address the same pair. Every
value entity carries `value_version` and `created_time` as facts, and every
deployment carries `version`, `spec_version`, and `created_time`, so a
consumer needs nothing but the entity to count versions. Create, set, rename,
move, and upload return their appended event envelope; lists return latest
live events. A removal is a delete mutation on the stream and an
`event_type = 3` row in the log. Bootstrap retention is exactly the result of
folding the full event range through the same fold, including retention of
deleted deployment versions while instances still pin them; the `state` tests
hold the two folds equal.

Both observed collections use a nanosecond `updated_at` HLC and append to
`scheduled_instance_status` and `node_status_log`. An observed write goes
through `Commit` like any other. A report whose clock is not older than the
latest stored row is published: its row is stamped with the commit sequence
and the commit's `event_time`, and the commit consumes that sequence. A report older than the latest stored
row is kept for history with `global_seq = 0`, returns an empty update, and is
never published. Clients merge observed values by their own clock, not by
sequence, so the sequence on an observed row only records which commit
published it. All observation history is retained. An empty payload with a
fresh clock clears the visible status; clients retain its clock to reject
delayed older packets. The bootstrap includes the latest tombstones for the
same reason.

#### Sidecars, grants, and visibility

Backup, ingress diagnostics, and secrets status publish through their owning
components, without the core lock or sequence. Each sidecar message replaces
that component's value. The stream sends the sidecars in its first message,
before the opening events; a reset preserves the client's sidecars unless a
replacement is present.

Agent sessions and user sessions are core collections, not sidecars. Every
create, approval, token claim, status change, and revocation is a commit that
stamps the row's `global_seq` and publishes the session's latest document as
an `AGENT_SESSION` or `USER_SESSION` mutation whose entity id is the row id
of the session's first event; the bootstrap carries the newest row of every
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
(the 21 registered logs and the two observed-status tables) are materialised
views of this log: what each one retains is whatever its readers need (the
newest row, every version of a live value, the versions a live instance
pins) and it can be regenerated from the log. `asset_store` is not: it is
node-local placement state reconciled from the asset root and S3, and asset
content itself lives outside the database. `global_seq` is the counter the
log is keyed by. The log is never
updated, deleted from, or compacted. `pq.WriteEventsInRange(after, upTo)`
reads it back as events; `pq.Open` brings it level with the entity tables at
startup by replaying `MutationsInRange` over any sequences the log lacks
(genesis rows at seq 0 included when the log is empty), which is how
databases from before the log, or written by a build without it, catch up.
The index `(entity_type, entity_id, seq)` serves per-entity history reads.
The stream and bootstrap readers still read the entity tables; moving them
to the log is separate work.

#### Append-only tables behind the latest-only collections

Every table that feeds a latest-only collection is an append-only event
table: `space_event_log`, `user_event_log`, `value_directory_event_log`, `asset_directory_event_log`,
`system_config_event_log`, `nix_store_reset_event_log`, `agent_session_event_log`,
`user_session_event_log`, and `secret_keyslot_event_log` (which feeds no collection at all: it
is in the log so a replica can reproduce the store). Each row is one event
and carries `id` (autoincrement), `global_seq`, `event_time` (ms), `author`
(0 system, negative for the agent of user `-author`), the entity id
(`space_id`, `user_id`, `directory_id`, `session_id`, `repo`, the keyslot's
`(kind, node_id)`; the system config's own `id` is its wire version), an
`event_type` (`AuthzVerb` 1 create, 2 update, 3 delete), and the entity's full
document at that point. Nothing is ever `UPDATE`d or `DELETE`d: a rename is
an update row, a removal is a delete row, which the stream carries as a
delete mutation. The live state the bootstrap carries is the newest row per
entity id whose `event_type` is not delete, and entity ids are allocated as
`MAX(entity_id) + 1` under the commit lock so an id is never reused. Every
write is a read of the newest row followed by one insert inside `Commit`,
which is also where state-machine guards live (a pending agent session can be
approved once, an approved one can claim a token once). Merge-streaming these
tables with the entity event logs by `global_seq` reproduces the full event
stream; that is the point of keeping the whole document on every row.
Tables that predate this shape are rebuilt at startup by `pq.Open`: the
schema creates the `*_event_log` table beside the old one, the old rows are
copied in as seq 0 create events, and the old table is dropped
(`pq/migrate_event_tables.go`). Every append-only table carries the
`_event_log` suffix; the two observed-status tables, `asset_store`, and
`global_seq` do not, because they are not logs. Every log carries an index
`idx_<table>_seq (global_seq, id)` (the two status tables `(global_seq)`),
which is what `MutationsInRange` scans; the status tables also carry
`event_time` (ms, the commit time), added in v0.0.615 and backfilled from
`updated_at`. The 21 log tables and their columns are registered once in
`pq/mutation_tables.go`, which is where `MutationsInRange`,
`BootstrapMutations`, `LatestMutation`, and `VisibilityChangesSince` read.

Grants are `AUTHZ_GRANT` entities whose value (`AuthzGrantValue`) holds the
subject user, template, bindings or direct rule, author, and created time; the
bootstrap holds the latest live grants and a revocation is a delete mutation.
The store publishes `apigen.CoreWriteUpdate` directly, with no internal
routing wrapper.

The handler filters each update by the connection's current permissions
(`visibility.go`): it observes every mutation's identity (deployment to space,
instance to deployment, node to allowed spaces, value to space, policy, grant
to user) whether or not it is visible, forwards the creates and updates the
viewer may see, and forwards a delete only when it sent that entity (or, after
a reconnect, when the entity's latest row is one it could see). In an opening
(bootstrap or replay) an entity's rows stand or fall together: its newest
payload in the batch decides, so a value moved out of the viewer's space ships
none of its history and one moved in ships all of it. A grant
mutation for the connected user, any template, global rule, space, or node
allow-list change, an entity space move, a network policy whose visibility
flips, or a delete of a grant it never saw schedules a re-bootstrap with
`reset` after a 200 ms debounce; updates arriving during the debounce are
skipped because the bootstrap supersedes them. Overflow closes the browser
subscription and forces reconnect; internal typed adapters resubscribe and
reconcile from a fresh read, including missed deletes and finalized
placements.

### Deployments
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/deployments/get` | `DeploymentGetRequest` | `DeploymentGetResponse` | ANY_OF default |
| POST | `/v1/deployments/create` | `DeploymentCreateRequest` | `DeploymentEvent` | ANY_OF default |
| POST | `/v2/deployments/update` | `DeploymentUpdateRequestV2` | `DeploymentEvent` | ANY_OF default |
| POST | `/v1/deployments/delete` | `DeploymentDeleteRequest` | — | ANY_OF default |
| POST | `/v1/deployments/recently-deleted` | `RecentlyDeletedDeploymentsRequest` | `RecentlyDeletedDeployments` | ANY_OF default |
| POST | `/v1/deployments/history` | `DeploymentHistoryRequest` | `DeploymentHistory` | ANY_OF default |
| POST | `/v1/deployments/versions` | `DeploymentVersionsRequest` | `DeploymentVersions` | ANY_OF default |
| POST | `/v1/deployments/log-query` | `LogQueryRequest` | `LogQueryResponse` | ANY_OF default |
| POST | `/v1/deployments/run-report` | `DeploymentRunReportRequest` | `DeploymentRunReport` | ANY_OF default |
| POST | `/v1/deployments/prepare-output` | `PrepareOutputRequest` | stream `PrepareOutputChunk` | ANY_OF default |
| POST | `/v1/repos/validate` | `RepoValidateRequest` | `RepoValidateResponse` | ANY_OF default |

`/v2/deployments/update` applies exactly one kind of change per request — `version_only_update` (deploy a version, implies running), `running_only_update` (start/stop at the current version; stop preserves the version), `spec_update` (full spec replacement, workload state included), `assigned_space_update` (space move), or `restart_update` (replace the running placement with the definition unchanged; rejected for a stopped workload and for the opendeploy self-deployment) — guarded by `expected_seq`, the seq of the deployment's last mutation as the caller saw it: the write is rejected when the stored row's seq is greater, and 0 skips the check. The same token guards `/v1/deployments/delete`, `/v1/nodes/evict`, enrollment accept, and `/v1/network-policies/update`. An update that changes nothing (same spec, name, space, and scheduling) returns the current event and consumes no seq; a restart is a `scheduling.generation` bump and nothing else. cleanproto has no `oneof`, so the kinds are plain optional fields and the handler rejects anything but exactly one.

`/v1/deployments/log-query` is a one-shot structured log search over a single deployment's stored logs (parquet archive plus WAL tail): it returns the newest matching parsed records (capped at 10k), a per-level histogram over the full range, the total match count, and per-field sampled value stats (top-10 values, coverage, and an other bucket over the newest 5k matched records — this feeds the sidebar with no extra request), all in one response. The primary proxies the request over the cluster session to the node hosting the deployment; the node builds the complete response and sends it back as a single message. `deployment_id = 0` with `target_node_id` addresses a node's system log, which is the log of the opendeploy system deployment on that node and is authorized as that deployment's `view_logs` in the system space. The endpoint does not tail live output. See the "Search API sketch" section of `docs/future-work/logmanager-implementation-plan.md` for the design rationale.

`/v1/deployments/run-report` summarizes one run of a scheduled instance: placement identity (deployment version, node, instance ordinal), started/stopped times, the container's exit code, and the last 20 log lines. Runs have no stored entity — the report is reconstructed from the instance's append-only status history: a run's rows are those with `number_of_restarts = run - 1`, its start is `last_restart_at`, and its stop is the `updated_at` HLC of the run's first STOPPED/CRASHED row, which also carries `exit_code`. The exit code is absent when the runner never observed the exit (rows written before the field existed, pre-spawn failures, the internal systemd runner, or an exit during an agent restart); the stop time is the status-write time, so it lags the true exit in the agent-restart case. Log lines are fetched only for finished runs, via an internal log query bounded to the run's time window, filtered by run and instance ordinal, and routed to the scheduled instance's node (which can differ from the deployment config's node during a cross-node move). Log fetch failures and gaps are reported in `warnings` rather than failing the request. Authorized with the same view-logs verb as `/v1/deployments/log-query`.

`/v1/deployments/prepare-output` streams raw prepare/build output chunks for a deployment spec version. A request with `spec_version=0` resolves to the latest known prepare status and tails while preparation is still active.

`/v1/deployments/recently-deleted` lists the tombstone config of the most recently deleted deployments, newest deletion first, so the UI can seed a new deployment from one. Deletion writes a spec version rather than removing the row, so these are read from the event log. `limit` defaults to 25 and is clamped to 200 — deleted configs are never pruned, so the listing must stay bounded. Internal `opendeploy` deployments are omitted because they are recreated by the primary rather than through `/v1/deployments/create`.

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
| POST | `/v1/nodes/rename` | `NodeRenameRequest` | `NodeEvent` | ANY_OF default |
| POST | `/v1/nodes/allowed-spaces` | `NodeAllowedSpacesRequest` | `NodeEvent` | ANY_OF default |
| POST | `/v1/nodes/drain` | `NodeDrainRequest` | `NodeEvent` | ANY_OF default |
| POST | `/v1/nodes/evict` | `NodeEvictRequest` | `NodeEvent` | ANY_OF default |
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
| POST | `/v1/secrets/list` | — | `SecretEventList` | ANY_OF default |
| POST | `/v1/secrets/create` | `SecretCreateRequest` | `SecretEvent` | ANY_OF default |
| POST | `/v1/secrets/set` | `SecretSetRequest` | `SecretEvent` | ANY_OF default |
| POST | `/v1/secrets/generate` | `SecretGenerateRequest` | `SecretEvent` | ANY_OF default |
| POST | `/v1/secrets/rename` | `SecretRenameRequest` | `SecretEvent` | ANY_OF default |
| POST | `/v1/secrets/move` | `SecretMoveRequest` | `SecretEvent` | ANY_OF default |
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
| POST | `/v1/configs/list` | — | `ConfigEventList` | ANY_OF default |
| POST | `/v1/configs/create` | `ConfigCreateRequest` | `ConfigEvent` | ANY_OF default |
| POST | `/v1/configs/set` | `ConfigSetRequest` | `ConfigEvent` | ANY_OF default |
| POST | `/v1/configs/rename` | `ConfigRenameRequest` | `ConfigEvent` | ANY_OF default |
| POST | `/v1/configs/move` | `ConfigMoveRequest` | `ConfigEvent` | ANY_OF default |
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
| POST | `/v1/assets/list` | — | `AssetEventList` | ANY_OF default |
| GET | `/v1/assets/content` | `asset_id` and `version` query params | raw content bytes | ANY_OF default |
| POST | `/v1/assets/upload` | raw file body, `asset_id` or `key` plus `space_id`/`directory_id`/`unique_key` query params | `AssetEvent` | ANY_OF default |
| POST | `/v1/assets/rename` | `AssetRenameRequest` | `AssetEvent` | ANY_OF default |
| POST | `/v1/assets/move` | `AssetMoveRequest` | `AssetEvent` | ANY_OF default |
| POST | `/v1/assets/delete` | `AssetDeleteRequest` | — | ANY_OF default |

Assets are versioned file blobs stored as one append-only event log per asset (`asset_event_log`), surfaced as `AssetEvent` envelopes with `value.fs`, `value.space_id`, and content sha256/size. The stream's value history provides the space and content facet revisions. `/v1/assets/upload` is the single write path for content: `?asset_id=` appends the next content version of that asset, `?key=` creates a new asset. Rename appends an event without changing the content facet or existing pins and rejects an existing destination key. List and state stream APIs carry only `AssetEvent` metadata; content bytes are streamed exclusively by `GET /v1/assets/content?asset_id=N&version=V`, the same pair a spec pins. Every event also carries the content's `storage_key`, the name of its local file and S3 object. Content of every size uses local primary storage while Backup is disabled and S3 while Backup is enabled; changing Backup starts an asynchronous placement transition. Storage placement is transparent to these asset endpoints. Workers stream required asset blobs on demand over the mTLS cluster asset endpoint during preparation. See [Assets](assets.md) for storage modes, transition status, retention, restore, and compatibility.

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
