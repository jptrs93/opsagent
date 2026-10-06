# OpenDeploy API instructions

You are talking to OpenDeploy, a deployment orchestration platform. This
document is everything you need to read and change its state over HTTP.

Base URL for this server: `{{.BaseURL}}`

## 1. Get a session

You have no credential yet. Ask for one, show the operator the approval code,
and wait for them to approve it in their browser.

```sh
curl -sS -X POST '{{.BaseURL}}/v1/agent-sessions/request-start' \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -d '{"user_id": {{.UserID}}}'
```

The response is `{"session_id": "...", "approval_code": "K7M-4QP2", "status": 1,
"request_expires_at": "..."}`. If that user already has a request waiting,
the call is `409 agent_session_request_pending`: ask the operator to approve
or reject the earlier one first.

- **`approval_code` is meant to be shown.** Print it and tell the operator to
  approve the request on the Sessions page in OpenDeploy, checking the code
  matches. Without that check they cannot tell your request from anyone else's.
- **`session_id` is a secret.** It is how you collect the token. Do not print it, log
  it, or write it anywhere the operator's screen or your transcript will show.

Then poll every 5 seconds:

```sh
curl -sS -X POST '{{.BaseURL}}/v1/agent-sessions/get-session' \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -d '{"session_id": "<the session_id>"}'
```

`status` is `1` pending, `2` approved, `3` rejected, `4` revoked. Stop polling
on anything but `1`. The approved response carries `token` and `expires_at` —
**once**. Later calls return the status without the token.

A request expires unapproved after 10 minutes, and an approved one expires
uncollected after 15. Both come back as status `3`; start over with a fresh
`request-start`.

Store the token and the base URL yourself, however you normally persist state.
The token is valid for 6 hours and is not recoverable. Never echo it into
output, commit it, or write it into a file the operator did not ask for.

## 2. Making requests

Every authenticated call needs three headers:

```
Authorization: Bearer <token>
Content-Type: application/json
Accept: application/json
```

**`Accept: application/json` is mandatory.** Without it the server replies with
binary protobuf, which will look to you like a corrupted response.

Everything below is `POST` with a JSON body unless marked otherwise. JSON field
names are `snake_case`, matching the examples exactly. Timestamp fields are
RFC 3339 strings (`"2026-09-04T05:00:00Z"`); both observed status types use an
`updated_at` timestamp with nanosecond precision. Times named `*_time` and
the `time` of a commit are integer Unix milliseconds. Errors come back as
`{"code": 403, "display_err": "Access denied"}` — `code` repeats the HTTP
status. Do not retry a `4xx`; it will fail again. Retry `5xx` and connection
errors.

## 3. What your session may do

Your token carries the rights of the operator who approved it, filtered
through the access rules the administrator has configured for this cluster.
Those rules — not this document — decide what you can call: the live API is
authoritative, and a `403` or a success from it always outranks anything
written here.

Under the builtin grant templates, a delegated agent session gets everything
in the approving operator's spaces except:

- **Logs.** Deployment logs, build output, run reports, and container metrics
  are withheld by default, because a running workload can echo a secret value
  into its output.
- **Secret values.** You may see secret metadata and create new secrets. By
  default you may not read, overwrite, rename, move, or delete one.
- **Deployments with host access.** Creating or updating a deployment with
  custom host mounts requires `use_host_mounts`; host networking requires
  `use_host_network`. Neither is inherited by agents under the builtin roles.
  Every update is covered, including version changes, start/stop, removing
  host access, and space moves. An administrator can explicitly delegate these
  permissions. Managed volumes and asset mounts do not require `use_host_mounts`.
- **The cluster itself.** Node management, enrollment, cluster settings,
  access rules and grants, config export, Nix store resets, and OpenDeploy's
  own internal deployments all live at the cluster level (space `0`) and
  default to human-only — either invisible to you or `403`. Nodes are the
  exception on the read side: the state snapshot (section 4) includes every
  node that hosts a space you can see, so you can place deployments.

An administrator writing custom rules is free to decide otherwise, in either
direction: your session may hold more than this list (including log access) or
less. When it matters, just try the call — or check your grants with
`GET /v1/auth/current/session` (section 11).

A denial is `403 Access denied`. Where you cannot even see the entity you get
`404` instead, so a `404` on something the operator says exists means it is
outside your session, not missing. Neither is a bug and neither has a
workaround: ask the operator to do that step in the browser, or to widen your
access if they meant to.

Separately, anything that touches the operator's own credentials or sessions is
closed to agent tokens whatever their grants say, and answers
`403 delegation_not_permitted`: saving or verifying the master password,
passkey registration, `/v1/user-sessions/*`, and creating, approving, or
listing agent sessions. There is no grant that opens these, so do not ask for
one. The one you keep is `/v1/agent-sessions/revoke` for your own session_id.

## 4. Reading state

`POST /v1/global/events` with an empty body (`{}`) is the starting point. It
returns one message filtered to your access: `seq` (the cluster's current
sequence), `synced` (`true`), and `snapshot`, which holds `seq` and
`entities`. Each entry in `entities` is one materialised entity:

```json
{"entity_type": 1, "entity_id": 24,
 "entity": {"value": {"deployment": {"id": 24, "name": "api", "space_id": 2, "spec": {...},
                                     "scheduling": {"running": true, "dedicated_nodes": {"nodes": [1]}}}}},
 "meta": {"created_time": 1756900000000, "updated_time": 1756990000000,
          "updated_seq": 3120, "updated_actor": 3, "version": 8, "spec_version": 4}}
```

Each entry carries `entity_type`, `entity_id`, `entity`, and `meta`.
`entity.value` carries exactly one field, named after the type. Entity types are
numbers in JSON: 1 `deployment`, 2 `scheduled_instance`, 3 `node`,
4 `secret`, 5 `config`, 6 `asset`, 7 `network_policy`, 8 `space`, 9 `user`,
10 `value_directory`, 11 `asset_directory`, 12 `authz_grant_template`,
13 `authz_grant`, 14 `authz_global_rule`, 15 `system_config`,
16 `scheduled_instance_status`, 17 `node_status`, 18 `agent_session`,
19 `user_session`, 20 `nix_store_reset`. Empty arrays and zero values may be
omitted in JSON; treat missing fields as empty.

`meta` holds the facts the server derives, never the payload: `created_time`
and `updated_time` (epoch ms), `updated_seq`, `updated_actor` (0 = the
system, negative = the agent of user `-actor`), and the counters —
`version` and `spec_version` for a deployment, `value_version` for a secret,
config, or asset. Use `updated_seq` as the `expected_seq` of your next write
to that entity.

There is no incremental replay over JSON. To re-read later, call
`/v1/global/events` again and replace everything you hold with the new
snapshot. The write receipts in section 5 onward carry the same entity and
`meta` shape, so you can patch your copy from a response without re-reading.
(`/v1/global/event-stream` does stream live commits, but it is protobuf-only.)

**Deployments.** The current state of a deployment is the entry with the
highest `meta.version` for its `entity_id`. Older versions of a deployment are
included while an instance still pins them, and a retained version of a
deleted deployment has `meta.deleted` true. The editable spec is `spec`;
`name`, `space_id`, and `scheduling` sit beside it. Each `scheduled_instance`
carries `node_id`, `instance_ordinal`, `state`, `space_id`, and
`deployment` (`deployment_id` and the pinned `version`); resolve that
`version` against the deployment entries, and join its observed
status (entity type 16, whose `entity_id` is the instance id). Status
`updated_at` is independent of `seq`. The snapshot includes live instances
and the last finalized run for an ordinal without a live placement.

**Values.** Every value version of a live secret, config, or asset is its own
entry with the same `entity_id` and a different `meta.value_version`. Group by
`entity_id`; the highest `value_version` is current, and the pinnable values
are the distinct `value_version`s. A reference is
`{"id": <entity_id>, "version": <value_version>}`. A rename or space move
does not bump `value_version`, so it is not a new value pin. A secret's
`entity` is metadata only: `{"id", "fs": {"key", "directory_id"}, "space_id"}`.
A config adds `value` in plaintext; an asset has `fs.key`, `sha256`,
`size_bytes`, and `storage_key`.

**Nodes** are entity type 3: `id`, `status` (4 is a normal member),
`operator` (`name`, `roles`, `allowed_spaces`, `enrolled_time`), and
`reported` (`identifier`, `underlay_address`, `wg_public_key`,
`host_addresses`). Placement uses the node's `entity_id`, not the display
name. Enrollment acceptance (operator-only) sends the `updated_seq` of the node
entry you reviewed as `expected_seq` (0 skips the check).

Per deployment:

- `POST /v1/deployments/get` `{"id": <id>}` returns `deployment` (a record
  with `deployment` and `meta`), `scheduled_instances`, and
  `instance_statuses`. Join statuses to instances by `scheduled_instance_id`;
  inspect `preparer.inputs`, `preparer.image`, and `runner.status` to find
  the failing stage. Build output and logs explain why.
- `POST /v1/deployments/history` `{"deployment_id": <id>}` returns `entries`,
  newest first, each holding either a `deployment` record (a config write) or
  an observed `status`.
- `POST /v1/deployments/versions` `{"deployment_id": <id>}` returns deployable
  git commits, release tags, or image tags for `target_version`, under the
  key matching the spec's source kind.
- `POST /v1/deployments/recently-deleted` `{"limit": 25}` returns `items`,
  tombstone records with specs intact for creating a separate deployment.

The listing endpoints that remain (`/v1/value-directories/list`,
`/v1/asset-directories/list`, the access and session lists) return
`{"items": [...]}`.

## 5. Deployments

### Creating

```sh
curl -sS -X POST '{{.BaseURL}}/v1/deployments/create' \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -d '{"name": "api", "space_id": 2,
       "scheduling": {"running": false, "dedicated_nodes": {"nodes": [1]}},
       "spec": { ... }}'
```

`scheduling.dedicated_nodes.nodes` must name exactly one node, and the node
must allow the space. `name` is unique per (name, space, node) — a clash is
`409 duplicate_deployment`. Create it stopped, then deploy a version with
`version_only_update` below.

Write the `spec` by copying a working deployment's spec out of the snapshot
(or a tombstone from `recently-deleted`) and editing it. It is a large
validated shape and inventing one field-by-field mostly produces `400`s. The
workload lives in `spec.workload.value.container` (`source`, `runtime`,
`version`, `upgrade_strategy`).

Before pointing a spec at a repo or image you have not used here before,
`POST /v1/repos/validate` checks it is reachable:

```json
{"source": {"nix_image_build": {"repo_url": "github.com/owner/repo", "selected_branch": "main",
                                "check_repo": true, "check_branch": true}}}
{"source": {"container_image": {"image": "ghcr.io/owner/app", "refresh_versions": true}}}
```

The response to create, and to every write below, is a **receipt**:

```json
{"seq": 3124, "time": 1756990000000, "actor": -3,
 "mutations": [{"value": {"create": {"entity_type": 1, "entity_id": 25,
                                     "entity": {"value": {"deployment": {...}}}, "meta": {...}}}}]}
```

`mutations` holds one entry per entity the write touched, and each one's
`value` is exactly one of `create`, `update`, or `delete` with the same
`entity_type`, `entity_id`, `entity`, and `meta` fields as a snapshot entry. Read the new
`meta.version`, `meta.value_version`, and `seq` from it, and replace your copy
of the entity with it.

### Changing

`POST /v1/deployments/update` with `{"deployment_id": <id>, "expected_seq":
<the deployment's meta.updated_seq as you last saw it>, "update": {...}}`
where `update` holds **exactly one** of the following fields, selecting what
kind of change it is. Zero or two of them is a `400`.

- `"version_only": {"target_version": "<id from /v1/deployments/versions>"}`
  — deploy that version and mark the workload running, leaving the rest of the
  spec untouched.
- `"running_only": {"desired_running": false}` — stop the workload,
  keeping the current version so a later `true` can reuse it.
- `"restart": {}` — restart the running workload without changing
  anything else.
- `"spec": {"spec": {...}}` — replace the configuration.
- `"assigned_space": {"space_id": <dest>}` — move the deployment to
  another space. Validated like a create into the destination space: every
  secret, config, and asset the spec references has to be reachable from it.

**`spec` is a full replacement.** There is no merge and no partial update. Any
field you leave out is *dropped*, and the call still returns `200`. So always:

1. Take the deployment's current `spec` from the snapshot (or from
   `/v1/deployments/get`) and note its `meta.updated_seq`.
2. Modify that object in place.
3. Send the whole thing back as `update.spec` with `expected_seq` set to
   that seq.

If the deployment changed after that seq the call is rejected — that is the
concurrency check, and it means someone else changed the deployment while you
were working. Re-read and redo your change on top. `expected_seq: 0` skips
the check; do not use it for a spec update. Every kind of change bumps
`version`, including a space move; an update that changes nothing returns a
receipt for the current state without writing.

After any change, poll `POST /v1/deployments/get` until the deployment
settles. A `200` from `update` means the config was accepted, not that the
workload is running.

### Deleting

`POST /v1/deployments/delete` `{"deployment_id": <id>, "expected_seq":
<seq>}`. The workload must already be stopped, and nothing else may reference its
address. Ask the operator first (see section 10).

### Referencing values from a spec

Inside `workload.value.container.runtime.env_vars`, each entry's `value` is
one of:

```json
{"literal": {"value": "literal"}}
{"secret": {"secret": {"secret_id": 30, "version": 1}}}
{"config": {"config": {"config_id": 5, "version": 3}}}
{"asset": {"key": "nginx.conf", "asset": {"asset_id": 8, "version": 2}}}
{"address": {"deployment_id": 9, "space_id": 2}}
```

Files are mounted with `workload.value.container.runtime.asset_mounts`:

```json
{"asset": {"asset_id": 8, "version": 2}, "container_path": "/etc/nginx/nginx.conf", "permission": 2}
```

Every one of these pins an immutable value: `secret_id`, `config_id`, or
`asset_id` is the entity id and `version` is its `value_version`. Uploading a
new asset version or setting a new config value therefore changes nothing
until you update the spec to pin the new version.

### Logs, run reports, and metrics

All of these are withheld from agents under the builtin rules (section 3): a
`403` means ask the operator, not retry. When your session does hold them:

`POST /v1/deployments/log-query` searches one deployment's stored logs and
returns the newest matches in a single round trip — no pagination, no tailing:

```json
{"deployment_id": 24,
 "time_start": "2026-09-04T05:00:00Z", "time_end": "2026-09-04T05:30:00Z",
 "filters": [{"field": "", "op": "contains", "value": "prediction"}],
 "limit": 500, "order": "desc"}
```

- `time_end` defaults to now and `time_start` to 12 hours before it.
- Every filter has to match. `field` empty matches the message text; `level`
  and `msg` address those parsed columns; any other name is a structured field
  of the line. `op` is `eq`, `neq`, `contains`, `not_contains` (the last two
  case-insensitive), `exists`, `not_exists`, or `in` with `values`.
- `limit` caps the returned records (default and maximum 5000). `stats` in
  the response reports `matched_rows` over the whole range and whether the
  result was `truncated`; narrow the window or the filters rather than raising
  the limit.
- Records are in `records`, each with `time` (unix nanoseconds), `level`,
  `msg`, `fields`, and `run`.
- `deployment_id: 0` with `target_node_id` searches that node's own OpenDeploy
  agent log instead of a workload's.

`POST /v1/deployments/run-report` `{"scheduled_instance_id": 807, "run": 1}`
summarises one run of one instance: start and stop times, exit code, and the
last 20 log lines in `log_lines`. The instance id comes from
`/v1/deployments/get`; runs count from 1 and go up by one on every restart.

`POST /v1/metrics/latest` (empty body) is the live overview: the newest
sample of every running container you can see, in `entries`. Each carries the
raw `sample` (cgroup counters and gauges such as `cpu_usage_usec`,
`mem_current`, `pids`, and the `psi_*` pressure values) and `rates`, the
per-second rate of every counter against the previous sample. CPU rates are
in microseconds per second, so `cpu_usage_usec` divided by 1e6 is the number
of CPUs in use.

`POST /v1/metrics/query` returns one deployment's history on a time grid:

```json
{"deployment_id": 24,
 "time_start": "2026-09-03T18:00:00Z", "time_end": "2026-09-04T06:00:00Z",
 "step_ms": 120000, "fields": ["cpu_usage_usec", "mem_current"]}
```

- The range defaults to the last hour. `step_ms` is the bucket width (at least
  10000; `0` lets the server pick about 300 buckets). `fields` empty means
  every metric. Optional `scheduled_instance_id`, `deployment_version`, and
  `run` narrow the result to one placement.
- The response carries `time_start`, `step_ms`, `buckets`, and one entry in
  `series` per (run, field) with `values`: one number per bucket, oldest
  first. A counter series (`kind` `0`) is already a per-second rate; gauges
  (`kind` `1`) and kernel averages (`kind` `2`) are bucket means. A bucket
  with no data is `null`.
- Prefer one wide window at a coarse step over many narrow ones; each call
  fans out to every node holding the deployment.

## 6. Assets

An asset has a stable `id`. Its snapshot entries carry `fs.key`,
`fs.directory_id`, `space_id`, `sha256`, and `size_bytes`, one entry per
`meta.value_version` (section 4). Specs pin `{"id": <id>, "version": <value_version>}`.

Assets live in a per-space folder tree (entity type 11,
root = directory `0`), and keys are unique per folder, not globally.

To update an existing asset, upload against its stable id:

```sh
curl -sS -X POST '{{.BaseURL}}/v1/assets/upload?asset_id=12' \
  -H "Authorization: Bearer $TOKEN" -H 'Accept: application/json' \
  --data-binary @nginx.conf
```

**Use `?asset_id=`, never `?key=`, for updates.** `?asset_id=` appends a new
version to that exact asset, which is almost always what you want. `?key=`
means "create a new asset" and fails with `400 asset_key_exists` if the key is
taken in that folder — unless you also pass `unique_key=true`, which suffixes it
(`nginx.conf1`) and hands you a *different* asset that no deployment is using.
Only create when the operator asked for a brand-new asset:

```
POST /v1/assets/upload?key=nginx.conf&space_id=2&directory_id=0
```

The upload response is a receipt; the asset's new `meta.value_version` is the
content version. Uploading bytes identical to the current version is a no-op
that returns the current state. Uploading does not change what deployments
serve; update the spec to pin that version (section 5).

Reading and organising:

- `GET /v1/assets/content?asset_id=12&version=3` — the bytes of one content version, addressed by the asset id and its `value_version`.
- `POST /v1/assets/rename` `{"asset_id": 12, "new_key": "nginx.conf"}`
- `POST /v1/assets/move` `{"asset_id": 12, "asset_directory_id": 3}`
  (omit `space_id` to keep the space; omit `asset_directory_id` for the
  space root; a `space_id` moves the asset into that space, which also needs
  create rights there)
- `POST /v1/assets/delete` `{"asset_id": 12}` — destructive, see section 10.
- `/v1/asset-directories/create` `{"space_id": 2, "parent_id": 0, "key": "nginx"}`,
  plus `/move`, `/rename`, `/delete`. A directory must be empty to delete;
  contents are never cascaded.

## 7. Configs

A config has a stable `id`, with `fs.key`, `fs.directory_id`, `space_id`,
and plaintext `value`, one snapshot entry per `meta.value_version`. Create and
set return a receipt whose `entity_id` and `meta.value_version` env refs pin.
Configs and secrets share one folder tree per space (`value_directories`,
root = directory `0`). Names are unique per folder.

- `POST /v1/configs/create` `{"key": "log-level", "value": "debug", "space_id": 2}` (add `value_directory_id` to create inside a folder)
- `POST /v1/configs/set` — appends the next version of an existing config.
  Setting the current value again is a no-op: the current state is returned
  and no version is created.

```json
{"config_id": 7, "value": "info",
 "update_referencing_deployments": true,
 "referencing_deployments": [{"deployment_id": 3, "expected_seq": 120}]}
```

`update_referencing_deployments` re-pins the deployments that use this config
to the new version atomically. When you set it you must list **every**
deployment currently referencing the config with its **current**
`meta.updated_seq`; anything missing, extra, or stale is
`409 referencing_deployments_changed` — re-read and retry. Leave both fields
out to append a version without touching any deployment, then update specs
yourself.

- `POST /v1/configs/rename` `{"config_id": 7, "new_key": "log-level"}`
- `POST /v1/configs/move` `{"config_id": 7, "value_directory_id": 3}`
  (same `space_id` rule as assets)
- `POST /v1/configs/delete` `{"config_id": 7}` — destructive, see section 10.
- `/v1/value-directories/create` `{"space_id": 2, "key": "app"}` (add `parent_id` to nest),
  plus `/move`, `/rename`, `/delete` (must be empty).

**Configs are not secrets.** Their values are stored in plaintext and are
returned in the snapshot. Never put a credential in one — use section 8.

## 8. Secrets

**The default posture: you can create a secret but not read one.** Secret
metadata is visible to you as `secret` entities (name, folder, value versions
— never plaintext). Everything that would expose or destroy a value is denied
by default: `/v1/secrets/reveal`, `/v1/secrets/set`, `/v1/secrets/create`
(which carries a plaintext value), `/v1/secrets/rename`, `/v1/secrets/move`,
and `/v1/secrets/delete` return `403` unless the administrator has granted
them to you. When they are denied, ask the operator to do those in the
browser.

What you can do is `generate`, because the value is produced inside the server
and never leaves it:

```sh
curl -sS -X POST '{{.BaseURL}}/v1/secrets/generate' \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -d '{"key": "postgres-password", "space_id": 2, "spec": {"password": {"length": 32}}}'
```

The response is a receipt containing only metadata:

```json
{"seq": 3125, "time": 1756990000000, "actor": -3,
 "mutations": [{"value": {"create": {"entity_type": 4, "entity_id": 30,
   "entity": {"value": {"secret": {"id": 30, "fs": {"key": "postgres-password"}, "space_id": 2}}},
   "meta": {"created_time": 1756990000000, "updated_time": 1756990000000,
            "updated_seq": 3125, "updated_actor": -3, "value_version": 1}}}}]}
```

`entity_id` is the stable identity. Pin it with its `meta.value_version`:

```json
"env_vars": {"POSTGRES_PASSWORD": {"value": {"secret": {"secret": {"secret_id": 30, "version": 1}}}}}
```

and send it through `/v1/deployments/update` as in section 5. The workload
receives the value at spawn time; you never handle it.

- **`length`** defaults to 32 and must be 16–4096. Out of range is a `400`, not
  a clamp.
- **`include_symbols`** defaults to false. Leave it that way unless the operator
  asks otherwise — without `reveal` you cannot read the value back to debug a
  quoting problem in a shell or connection string.
- **The name must be new.** An existing name in that space root is
  `400 secret_name_exists`. `generate` cannot rotate a secret, only create one;
  rotating means `/v1/secrets/set`, so unless you hold that, ask the operator
  to rotate.
- Generated secrets land in the space root. Names are unique per folder, not
  globally.
- `password` is one specification among future others. Send exactly one.

`POST /v1/secrets/status` reports whether the store is unlocked. If
`unlocked` is false, every secret operation fails until the operator unlocks
it — that is not something you can fix.

## 9. Spaces

Spaces are entity type 8 in the snapshot (`id`, `name`). You can rename one
(`/v1/spaces/update` `{"id": 2, "name": "staging"}`) and delete one
(`/v1/spaces/delete` `{"id": 2}`), but by default **not create one** —
`/v1/spaces/create` is `403` under the builtin rules. Deleting a space is the
most destructive call in this API; treat it as section 10 and expect to be
told no.

### Network policies

Workloads reach each other within a space, and anything can reach the
`global` space (id `1`), by default. Crossing any other space boundary needs
an explicit policy. Policies are global entities, not part of a spec; they are
entity type 7 in the snapshot, filtered to the ones whose peers you can see.

```json
{"action": 1,
 "source": {"target": {"value": {"space": {"space_id": 2}}}},
 "destination": {"target": {"value": {"deployment": {"deployment_id": 9}}}},
 "ports": [{"protocol": 1, "range": {"start": 5432, "end": 5432}}]}
```

`action` `1` is allow, the only action. A peer's `target.value` is either
`space` (a whole space) or `deployment` (one deployment); a deployment peer
follows the deployment if it moves space. `ports` empty means every port and
protocol; `protocol` is `1` for TCP or `2` for UDP, and `range` is inclusive. Writing needs update rights on the
destination's space, and a policy whose source and destination resolve to the
same space is rejected as redundant. Create and update return receipts.
`/update` takes the policy's `id`, its `meta.updated_seq` as `expected_seq`
(a stale value is `409 network_policy_version_conflict`), plus the same
fields; `/delete` takes `{"id": <id>}` and is destructive (section 10).

## 10. Rules that apply everywhere

- **Destructive operations need explicit confirmation first.** Deleting a
  deployment, asset, config, directory, or space is not something to do because
  it seemed implied. Ask, quote exactly what will be deleted, and wait.
- **Streaming endpoints are protobuf-only.** `/v1/global/event-stream` and
  `/v1/deployments/prepare-output` ignore `Accept: application/json`. Use the
  non-streaming endpoints above instead.
- **Enums are numbers** in JSON, not names.
- **Ids are per-kind.** Entity ids, `seq`, and a deployment's `version` are
  different number spaces; value references use the entity id and
  `value_version`, never `seq`.

## 11. Endpoint reference

Everything on the public API, and what the builtin grant templates allow an
agent session to call. "operator" means the endpoint exists but is denied to
agents *by default*; on this cluster your session may or may not hold it —
the live API's answer is the truth. A `403` will not change on retry: ask.

**Reading**

| Endpoint | |
|---|---|
| `POST /v1/global/events` | yes |
| `POST /v1/deployments/get` `/history` `/versions` `/recently-deleted` | yes |
| `GET /v1/assets/content` | yes |
| `POST /v1/secrets/status` | yes |
| `POST /v1/value-directories/list`, `/v1/asset-directories/list` | yes |
| `POST /v1/repos/validate` | yes |
| `GET /v1/healthz`, `GET /v1/tls/ca.crt`, `GET /v1/auth/methods` | yes, no auth |
| `POST /v1/deployments/log-query` `/run-report` | operator (logs) |
| `POST /v1/metrics/query` `/latest` | operator (logs) |
| `POST /v1/deployments/prepare-output` | operator (logs), protobuf stream |
| `POST /v1/global/event-stream` | protobuf stream |
| `POST /v1/global/exported-config`, `/v1/nodes/exposure` | operator (cluster) |
| `POST /v1/access/grant-templates/list` `/global-rules/list` | operator (cluster) |

**Writing**

| Endpoint | |
|---|---|
| `POST /v1/deployments/create`, `POST /v1/deployments/update` | yes; host-access deployments require additional permissions (section 3) |
| `POST /v1/deployments/delete` | yes, subject to deletion requirements |
| `POST /v1/assets/upload` `/rename` `/move` `/delete` | yes |
| `POST /v1/asset-directories/create` `/move` `/rename` `/delete` | yes |
| `POST /v1/configs/create` `/set` `/rename` `/move` `/delete` | yes |
| `POST /v1/value-directories/create` `/move` `/rename` `/delete` | yes |
| `POST /v1/secrets/generate` | yes |
| `POST /v1/spaces/update` `/delete` | yes |
| `POST /v1/network-policies/create` `/update` `/delete` | yes |
| `POST /v1/spaces/create` | operator |
| `POST /v1/secrets/create` `/set` `/reveal` `/rename` `/move` `/delete` | operator (secret values) |
| `POST /v1/secrets/unlock` `/rotate-recovery-code` | operator (cluster) |
| `POST /v1/nodes/rename` `/allowed-spaces` `/drain` `/evict`, `/v1/nodes/enrollments/*` | operator (cluster) |
| `POST /v1/cluster-settings/get` `/update`, `/v1/nix-store/reset` | operator (cluster) |
| `POST /v1/access/grant-templates/*` `/grants/*` `/global-rules/*` | operator (cluster) |

**Sessions**

| Endpoint | |
|---|---|
| `GET /v1/agent-sessions/instructions`, `POST /v1/agent-sessions/request-start` `/get-session` | yes, no auth |
| `POST /v1/agent-sessions/revoke` (your own session_id only) | yes |
| `GET /v1/auth/current/session` | yes, but see below |
| `POST /v1/agent-sessions/approve` `/create` `/list` | human-only |
| `/v1/auth/master/password/*`, `/v1/auth/passkey/register/*`, `/v1/user-sessions/*` | human-only |
| `/v1/auth/master`, `/v1/auth/password/login`, `/v1/auth/passkey/login/*` | browser login, not for agents |

`GET /v1/auth/current/session` returns the caller's own session — user id,
session id, expiry, **and the bearer token itself**, since the web UI uses it
to restore a stored session. The response contains your credential: never
print it verbatim.
