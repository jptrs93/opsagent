# API design

## Overview

The API is HTTP + binary protobuf v3. Each service has its own file — `api-contract/api_service.proto`, `cluster_service.proto`, and `enrollment_service.proto` — holding its RPC definitions and per-route access policies; model messages are split per entity into `api-contract/model/<entity>.proto` (data model shapes) and `api-contract/model_<entity>_operations.proto` (endpoint request/response shapes). The generator concatenates every file into one schema before running, so a message defined in any of them is visible to all. Go and JS code is generated from the proto schema using [cleanproto](https://github.com/jptrs93/cleanproto/blob/main/README.md).

The split follows the security boundary, not just size: each service is served on a different listener with a different notion of caller identity, so which file an RPC lives in decides what can reach it.

| File | Service | Listener | Caller identity |
|---|---|---|---|
| `api_service.proto` | `ApiServer` | public front-end | JWT scopes, per-route policy |
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
- Write handlers go through `state.Service.Commit(ctx, inlockValidate, mutate)`: every check-then-write sequence (deployments, secrets, configs, assets, cluster settings) runs its in-lock validator and its row writes on the transaction-bound `pq.Queries` inside one commit, and returns the `CoreUpdate` the store publishes. Domain logic lives in `app/primary/domain/<name>` packages (`deployments`, `scheduledinstances`, `nodes`, `networkpolicies`, `assets`, `secrets`, `values`, `authz`, `users`, `agentsessions`, `systemconfig`); handlers call those functions or `pq.Queries` directly. Deployment endpoints validate in two layers: `preLockDeploymentValidate` (pure shape rules, before the commit) and a per-operation in-lock validator against `nodes.LiveState` (`inLockValidateDeploymentCreate` / `Update` / `SpaceMove` / `Delete`; the v2 update handler picks `SpaceMove` when the update carries a space change). Authz and network I/O (nix source verification) run before the commit; the version CAS closes the gap. Lock order: the asset operation lock (where asset file operations are involved) precedes `Mu`; subsystem locks (secrets manager, config service) nest strictly inside `Mu`.

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

### Personal sessions
| Method | Path | Request | Response | Policy |
|--------|------|---------|----------|--------|
| POST | `/v1/personal-sessions/list` | — | `PersonalSessionList` | ANY_OF default |
| POST | `/v1/personal-sessions/revoke` | `PersonalSessionRevokeRequest` | — | ANY_OF default |

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
| GET | `/v1/global/snapshot` | — | `Snapshot` | ANY_OF default |
| POST | `/v1/global/state-stream` | — | stream `StateStreamMsg` | ANY_OF default |
| POST | `/v1/global/exported-config` | — | `ExportedConfigBlob` | ANY_OF default |

`/v1/global/state-stream` sends `StateStreamMsg`: a `snapshot` on connect,
then `core`, `observed`, and sidecar messages, plus five-second heartbeats.
`/v1/global/snapshot` returns the same filtered bootstrap. Core collections
are arrays of event envelopes with entity id, version, seq, event_id, author,
event type, timestamps, facet counters where relevant, and `value`.

Every authored and observed primary writer calls `commitLocked(ctx, mutate)`
under the store mutex. The callback receives transaction queries and a candidate
`global_seq + 1`, and returns `Update{Core, Observed}`. It checks expected entity
or facet counters before mutation, owns all database writes (including private
payloads), and sets the candidate sequence on authored rows, `CoreUpdate.Seq`,
and every core event's `Seq`. Events use normal read-converter projections of
the written rows. Version checks never compare the last-change global sequence.

The store reserves the SQLite writer before reading, runs the mutation and one
scheduler reconciliation phase in the same transaction, and persists the
sequence only if the final core update is non-nil. Observations keep their HLCs;
history-only writes may return an empty update. After commit, the store installs
the final caches and publishes core first, then observed, without repairing the
returned sequence fields. Failed writes change no caches, consume no ids or
sequence, and publish nothing. Callbacks are not retried. `*AtSeq` queries remain
a test oracle only.

The scheduler reads current desired, target and observed rows through the same
transaction and appends immediate target changes there. Startup recovery is
synchronous; acknowledgements and drain deadlines trigger later transactions.
Drain waits derive from persisted event sequences and times, with a fresh
conservative timeout for drains found at startup. Rendering and network delivery
remain outside the transaction. Sidecars retain independent locks and streams.

Snapshots contain latest live deployments plus pinned historical versions,
non-final instances plus the latest final per ordinal without a live instance,
and latest observed statuses. Value arrays contain full histories for live
secrets, configs, and assets. Pins remain log row ids: take `event_id` only
when `value_version` changes. Create/set/rename/move/upload return their
appended event; lists return latest live events. Updates include deletes:
`event_type = 3`, or `deleted` on spaces and directories. Snapshot retention
is exactly the result of replaying updates through the reducers, including
retention of deleted deployment versions while instances still pin them.

Both observed collections use a nanosecond `updated_at` HLC. They append to
`scheduled_instance_status` and `node_status_log` and carry no global sequence.
All observation history is retained. An empty payload with a fresh clock
clears the visible status; clients retain its clock to reject delayed older
packets. Snapshots include the latest tombstones for the same reason.

Backup, ingress diagnostics, secrets status, and agent sessions publish through
their owning components, without the core lock or sequence. Each sidecar
message replaces that component's value. Agent sessions use an `AgentSessionList`
wrapper so an empty replacement can clear the list, and are sent only to their
owner. The handler adds sidecars to the store snapshot; core resets preserve
sidecars unless an explicit replacement is present.

Grants arrive as `AuthzGrantEvent` arrays in both snapshots and core updates.
Their values contain the subject user, template, and bindings or direct rule;
delete events retain these fields. Snapshots hold the latest live grant events,
and updates carry each written event, including deletes. The store publishes
`apigen.CoreUpdate` directly, with no internal routing wrapper.

Only `CoreUpdate` passes the sequence gate. Visibility filters each transaction
by current parent permissions. Grant events identify affected users through
`value.user_id` and reset those users; template or
global-rule changes reset all. Node allow-list and entity-space changes also
reset affected views, with a 200 ms debounce. The system config is redacted,
including `master_password_hash`. Overflow closes the browser subscription and
forces reconnect; internal typed adapters resubscribe and reconcile from a
fresh snapshot, including missed deletes and finalized placements.

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

`/v2/deployments/update` applies exactly one kind of change per request — `version_only_update` (deploy a version, implies running), `running_only_update` (start/stop at the current version; stop preserves the version), `spec_update` (full spec replacement, workload state included), `assigned_space_update` (space move), or `restart_update` (replace the running placement with the definition unchanged; rejected for a stopped workload and for the opendeploy self-deployment) — guarded by the root `expected_version`, which must equal the deployment's top-level version + 1. The top-level version bumps on every deployment event, so the one guard covers every kind. cleanproto has no `oneof`, so the kinds are plain optional fields and the handler rejects anything but exactly one.

`/v1/deployments/log-query` is a one-shot structured log search over a single deployment's stored logs (parquet archive plus WAL tail): it returns the newest matching parsed records (capped at 10k), a per-level histogram over the full range, the total match count, and per-field sampled value stats (top-10 values, coverage, and an other bucket over the newest 5k matched records — this feeds the sidebar with no extra request), all in one response. The primary proxies the request over the cluster session to the node hosting the deployment; the node builds the complete response and sends it back as a single message. `deployment_id = 0` with `target_node_id` addresses a node's system log and is authorized against the node instead. The endpoint does not tail live output. See the "Search API sketch" section of `docs/future-work/logmanager-implementation-plan.md` for the design rationale.

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
| GET | `/v1/nodes/enrollments/info` | — | `NodeEnrollmentInfo` | ANY_OF default |
| POST | `/v1/nodes/enrollments/list` | — | `EnrollmentRequestList` | ANY_OF default |
| POST | `/v1/nodes/enrollments/accept` | `EnrollmentAcceptRequest` | `EnrollmentRequestStatus` | ANY_OF default |

Deployment placement is constrained by each node's allowed-spaces list; `/v1/nodes/allowed-spaces` replaces it wholesale and is rejected if it would strip a space out from under deployments already running on that node. See [Deployments](../product/deployments.md) for the policy.

Node events split lifecycle (`value.status`, `enrollment_requested_at`),
operator fields (`value.operator`), and reported facts (`value.reported`).
`NodeStatus` contains connection information, remote address, binary version,
and a monotonic nanosecond `updated_at`. Both enrollment and cluster hellos send the
same `NodeReported` bundle. `host_addresses_unknown` preserves the previous
inventory when enumeration fails; a successful empty list clears it. Identical
reports append nothing. Acceptance checks
`expected_version`, writes one event, and clears the pending timestamp;
first cluster hello therefore adds no trailing node events. An unaccepted
session disconnect cancels its request, and a session expires after ten minutes.
For admitted nodes either outcome clears the request without changing membership.
The flat hello fields and the legacy `node_statuses` table were removed after the
v0.0.587 rollout; a hello without `reported` is rejected.

Workers use `EnrollmentV1` only when local cluster CA/cert/key material is missing. The enrollment listener is HTTPS using the primary server certificate. Because workers do not yet have a trust root, secondary installs pin the enrollment listener's `sha256:` SPKI fingerprint from authenticated `GET /v1/nodes/enrollments/info`; the worker verifies the presented TLS certificate matches that fingerprint before sending its CSR. In production, the public enrollment listener also applies the same generated-mux middleware approach as the web UI: per-client-IP request admission is limited to a burst of 5 and a refill rate of 0.2 requests/second. Before the first hello the worker generates its TLS private key, writes it to `/var/lib/opendeploy/tls/node.key`, and derives its `requesting_machine_id` from it: the lowercase hex SHA-256 of the key's SubjectPublicKeyInfo, 64 characters, which also fits the X.509 CN length bound. Every retry and restart of a pending enrollment therefore presents the same key and identifier, and the worker logs the identifier when enrollment starts so an operator can compare it against the pending request in the UI. The worker sends the identifier plus a PEM CSR whose CN is the identifier, then keeps the stream open until an operator accepts the request. The primary verifies the CSR signature, that the CN equals the reported identifier, and that the identifier equals the SHA-256 of the CSR's public key; a hello that fails any of these is rejected with `enrollment_invalid_csr`. Because the identifier is bound to the key, nobody without the private key can produce a valid hello for it, a different key is a different identifier and lands on its own request, and the primary keeps no server-side pin. A hello that reconnects with the same identifier replaces the earlier stream for that request. A hello whose identifier already belongs to an enrolled node is rejected with `enrollment_identifier_enrolled` (`409 Conflict`) and logged with the peer address; because `ApiErr.internal_err` never crosses the wire the worker treats any 409 on the hello stream as a permanent rejection and exits instead of retrying. The requesting address shown to the operator is the TCP peer address; forwarded headers are ignored. There is no reset or re-enrollment path for a member yet; a member that loses its TLS material stays stranded until one is added. Nodes enrolled before identifiers were key-derived keep their UUID identifiers and certificates; they keep working but can never re-enrol. The CSR CN, worker certificate CN, and `NodeEvent.value.reported.identifier` are that stable identifier. Deployment placement, authorization, lookup, and duplicate detection use `node_id`; the operator-selected worker name is mutable display metadata only. Acceptance signs the CSR with the primary's internally stored cluster CA key and returns only the CA certificate and worker certificate; the private key never leaves the worker. By default the worker writes them to `/var/lib/opendeploy/tls/ca.crt`, `/var/lib/opendeploy/tls/node.crt`, and `/var/lib/opendeploy/tls/node.key`, then reconnects to `OpsagentClusterV1` over mTLS. The cert files are written `0644`; the private key is written `0600`.

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

User-managed configs and encrypted secrets are immutable versioned rows. Setting an existing secret/config appends version `vN` with a new numeric version row ID; settings refs and deployment env refs pin exact rows with `ConfigRef.version_id`, `SecretRef.version_id`, `EnvVarValue.configVersionId`, and `EnvVarValue.secretVersionId`. Rename appends an event with the new display name and unchanged value facet. Delete soft-deletes the whole group and is rejected while any settings or deployment config still references one of its row IDs.

`SecretSetRequest` and `ConfigSetRequest` can atomically roll deployment env refs to the new immutable row. With `update_referencing_deployments`, the request supplies every referencing deployment's current config ID/version. The backend derives the references from current stored specs, rejects stale, duplicate, missing, or extra entries, then commits the new value row and all deployment config/history versions in one transaction.

`POST /v1/secrets/reveal` is the only user-facing API that returns decrypted secret plaintext. It accepts `SecretRevealRequest.id` for exact-version reveal; list/state APIs return metadata only.

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
| GET | `/v1/assets/content` | `content_version_id` query param | raw content bytes | ANY_OF default |
| POST | `/v1/assets/upload` | raw file body, `asset_id` or `key` plus `space_id`/`directory_id`/`unique_key` query params | `AssetEvent` | ANY_OF default |
| POST | `/v1/assets/rename` | `AssetRenameRequest` | `AssetEvent` | ANY_OF default |
| POST | `/v1/assets/move` | `AssetMoveRequest` | `AssetEvent` | ANY_OF default |
| POST | `/v1/assets/delete` | `AssetDeleteRequest` | — | ANY_OF default |

Assets are versioned file blobs stored as one append-only event log per asset (`asset_event_log`), surfaced as `AssetEvent` envelopes with `value.fs`, `value.space_id`, and content sha256/size. Snapshot history provides the space and content facet revisions. `/v1/assets/upload` is the single write path for content: `?asset_id=` appends the next content version of that asset, `?key=` creates a new asset. Rename appends an event without changing the content facet or existing pins and rejects an existing destination key. List and state stream APIs carry only `AssetEvent` metadata; content bytes are streamed exclusively by `GET /v1/assets/content?content_version_id=N`. Blobs up to and including 10 MiB are stored inline. Larger blobs use local primary storage while Backup is disabled and S3 while Backup is enabled; changing Backup starts an asynchronous placement transition. Storage placement is transparent to these asset endpoints. Workers stream required asset blobs on demand over the mTLS cluster asset endpoint during preparation. See [Assets](assets.md) for storage modes, transition status, retention, restore, and compatibility.

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
| GET | `/v1/cluster/asset?asset_version_id=<id>` | query params | raw asset bytes with `X-Opsagent-Asset-*` headers | NO_AUTH |
| GET | `/v1/cluster/secrets` | `ClusterSecretsRequest` | `ClusterSecretsResponse` | NO_AUTH |
| GET | `/v1/cluster/configs` | `ClusterConfigsRequest` | `ClusterConfigsResponse` | NO_AUTH |
| GET | `/v1/cluster/issued-tls` | `ClusterIssuedTLSRequest` | `ClusterIssuedTLSResponse` | NO_AUTH |
| GET | `/v1/cluster/renew-certificate` | — | `ClusterRenewCertificateResponse` | NO_AUTH |
| POST | `/v1/cluster/connect` | stream `MsgToPrimary` | stream `MsgToSecondary` | NO_AUTH |

Cluster secrets/configs requests carry immutable row IDs. The primary authorizes those IDs against the deployment refs allowed for the requesting worker, decrypts/fetches only those rows, and the worker keeps the plaintext values in memory.

`/v1/cluster/connect` is the long-lived bidirectional worker session. HTTP/2
request and response bodies contain unsigned-varint-length-prefixed protobuf
frames. The primary sends the cluster network info, the latest targeted
`ClusterNetMap`, and the deployment snapshot at session start. Later complete
network maps use latest-value coalescing rather than queueing obsolete versions.
Workers send durable `NetMapStatus` acknowledgements on the request stream.

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
