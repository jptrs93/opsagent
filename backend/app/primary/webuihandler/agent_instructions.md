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

The response is `{"id": "...", "approval_code": "K7M-4QP2", "status": 1}`.

- **`approval_code` is meant to be shown.** Print it and tell the operator to
  approve the request on the Sessions page in OpenDeploy, checking the code
  matches. Without that check they cannot tell your request from anyone else's.
- **`id` is a secret.** It is how you collect the token. Do not print it, log
  it, or write it anywhere the operator's screen or your transcript will show.

Then poll every 5 seconds:

```sh
curl -sS -X POST '{{.BaseURL}}/v1/agent-sessions/get-session' \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -d '{"id": "<the id>"}'
```

`status` is `1` pending, `2` approved, `3` rejected, `4` revoked. Stop polling
on anything but `1`. The approved response carries `token` — **once**. Later
calls return the status without it.

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
names are `snake_case`, matching the examples exactly. Errors come back as
`{"code": 403, "display_err": "Access denied"}` — `code` repeats the HTTP
status. Do not retry a `4xx`; it will fail again. Retry `5xx` and connection
errors.

## 3. What your session may do

Your token carries the rights of the operator who approved it, filtered
through the access rules the administrator has configured for this cluster.
Those rules — not this document — decide what you can call: the live API is
authoritative, and a `403` or a success from it always outranks anything
written here.

Under the builtin rule templates, a delegated agent session gets everything
in the approving operator's spaces except:

- **Logs.** Deployment logs and build output are withheld by default, because a
  running workload can echo a secret value into them.
- **Secret values.** You may list secret metadata and create new secrets. By
  default you may not read, overwrite, rename, move, or delete one.
- **The cluster itself.** Node management, enrollment, cluster settings,
  access rules and grants, config export, and OpenDeploy's own internal
  deployments all live at the cluster level (space `0`) and default to
  human-only — either invisible to you or `403`. The one read the defaults
  keep is `POST /v1/nodes/list`, which returns the nodes hosting your spaces
  so you can place deployments.

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
`403 delegation_not_permitted`: the master password (`/v1/auth/master*`),
passkey registration, `/v1/personal-sessions/*`, and creating, approving, or
listing agent sessions. There is no grant that opens these, so do not ask for
one. The one you keep is `/v1/agent-sessions/revoke` for your own session id.

## 4. Reading state

`GET /v1/global/state` is the starting point for everything. It returns
`spaces`, `deployment_configs`, `assets`, `configs`, `secrets`,
`value_directories`, and `asset_directories` — with the ids the other endpoints
expect. Read it before you change anything. It is filtered to your access, so
what is absent is either absent or not yours.

Every collection is a wrapper around a list, so the deployments are at
`deployment_configs.items`, the secrets at `secrets.items`, and so on. Listing
endpoints return the same `{"items": [...]}` shape.

The same collections have their own endpoints when you want one of them fresh:
`/v1/assets/list`, `/v1/configs/list`, `/v1/secrets/list`,
`/v1/value-directories/list`, `/v1/asset-directories/list`.

Per deployment:

- `POST /v1/deployments/get` `{"id": <id>}` — the config plus its live
  `instances.items`. Each instance carries `status.preparer` (`inputs`, `image`) and
  `status.runner` (`status`, `running_version`, `number_of_restarts`). That
  tells you *which stage* failed, not why: the reason is in the build output or
  the logs. If your session has log access, `POST /v1/deployments/log-query`;
  otherwise report the stage and ask the operator to look.
- `POST /v1/deployments/history` `{"deployment_id": <id>}` — past config
  versions with the status each reached.
- `POST /v1/deployments/versions` `{"deployment_id": <id>}` — what is
  *deployable*: git commits for a nix build (optionally
  `"selected_branch": "main"`), release tags, or image tags. This is where a
  `target_version` comes from.
- `POST /v1/deployments/recently-deleted` `{"limit": 25}` — tombstones of
  deleted deployments, spec intact. Useful as a template for a new one; the id
  and version in them are dead.

**Nodes are not in global state.** `node_id` is required to create a
deployment; `POST /v1/nodes/list` (empty body) returns the nodes visible to
you as `{"items": [...]}`, each with its `id`, `name`, and `allowed_spaces` —
the spaces whose deployments the node accepts (space `0` is always listed and
is not yours). If none of them is the right host, ask the operator which node
to use.

## 5. Deployments

### Creating

```sh
curl -sS -X POST '{{.BaseURL}}/v1/deployments/create' \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -d '{"name": "api", "space_id": 2, "node_id": 1, "spec": { ... }}'
```

Write the `spec` by copying a working deployment's spec out of global state (or
a tombstone from `recently-deleted`) and editing it. It is a large validated
shape and inventing one field-by-field mostly produces `400`s. `name` is unique
per (name, space, node) — a clash is `409 duplicate_deployment` — and the node
must allow the space.

Before pointing a spec at a repo or image you have not used here before,
`POST /v1/repos/validate` checks it is reachable:

```json
{"nix_docker_build": {"repo_url": "github.com/owner/repo", "selected_branch": "main",
                      "check_repo": true, "check_branch": true}}
{"container_image": {"image": "ghcr.io/owner/app", "refresh_versions": true}}
```

### Changing

`POST /v2/deployments/update` with `{"deployment_id": <id>, "expected_version":
<current version + 1>, ...}` plus **exactly one** of the following fields,
selecting what kind of change it is. Zero or two of them is a `400`.

- `"version_only_update": {"target_version": "<id from /v1/deployments/versions>"}`
  — deploy that version and mark the workload running, leaving the rest of the
  spec untouched.
- `"running_only_update": {"desired_running": false}` — stop the workload,
  keeping the current version so a later `true` can reuse it.
- `"spec_update": {"spec": {...}}` — replace the configuration.
- `"assigned_space_update": {"space_id": <dest>}` — move the deployment to
  another space. Validated like a create into the destination space: every
  secret, config, and asset the spec references has to be reachable from it.

**`spec` is a full replacement.** There is no merge and no partial update. Any
field you leave out is *dropped*, and the call still returns `200`. So always:

1. `GET /v1/global/state` and take the deployment's current `spec` and `version`.
2. Modify that object in place.
3. Send the whole thing back as `spec_update` with `expected_version` set to
   `current + 1`.

If `expected_version` is not exactly one greater than the stored version the
call is rejected — that is the concurrency check, and it means someone else
changed the deployment while you were working. Re-read and redo your change on
top. Every kind of change bumps `version`, including a space move.

After any change, poll `POST /v1/deployments/get` until the deployment
settles. A `200` from `update` means the config was accepted, not that the
workload is running.

### Deleting

`POST /v1/deployments/delete` `{"deployment_id": <id>, "version": <current +
1>}`. The workload must already be stopped, and nothing else may reference its
address. Ask the operator first (see section 10).

### Referencing values from a spec

Inside `runtime.env_vars`, each entry is one of:

```json
{"value": "literal"}
{"secret_version_id": 12}
{"config_version_id": 7}
{"asset": "nginx.conf", "asset_version_id": 41}
{"address_deployment_id": 9, "address_space_id": 2}
```

Files are mounted with `runtime.asset_mounts`:

```json
{"asset_version_id": 41, "container_path": "/etc/nginx/nginx.conf", "permission": 2}
```

Every one of these pins an immutable **version row id**, never the stable
identity. Uploading a new asset version or setting a new config value therefore
changes nothing until you update the spec to pin the new id.

## 6. Assets

An asset is a stable identity — its `id` never changes across renames, moves,
or new content. Content lives in immutable numbered versions listed newest
first in `content_versions`; `content_versions[0].id` is what specs pin. The
name and folder are in `fs` (`fs.key`, `fs.directory_id`), and the space is in
`space_versions[0].space_id`.

Assets live in a per-space folder tree (`asset_directories` in global state,
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

The upload response is the asset, and `content_versions[0].id` is the new
version id. Uploading does not change what deployments serve; update the spec
to pin that id (section 5).

Reading and organising:

- `GET /v1/assets/content?content_version_id=41` — the bytes of one version.
- `POST /v1/assets/rename` `{"asset_id": 12, "new_key": "nginx.conf"}`
- `POST /v1/assets/move` `{"asset_id": 12, "asset_directory_id": 3, "space_id": 0}`
  (`space_id: 0` keeps the space; `asset_directory_id: 0` is the space root)
- `POST /v1/assets/delete` `{"asset_id": 12}` — destructive, see section 10.
- `/v1/asset-directories/create` `{"space_id": 2, "parent_id": 0, "key": "nginx"}`,
  plus `/move`, `/rename`, `/delete`. A directory must be empty to delete;
  contents are never cascaded.

## 7. Configs

A config is a plaintext value with the same identity/version split as an asset:
stable `id`, `fs.name`, `fs.directory_id`, and `value_versions` newest first
carrying both the `value` and the `id` that env refs pin. Configs and secrets
share one folder tree per space (`value_directories`, root = directory `0`).
Names are unique per folder.

- `POST /v1/configs/create` `{"name": "log-level", "value": "debug", "space_id": 2, "value_directory_id": 0}`
- `POST /v1/configs/set` — appends the next version of an existing config:

```json
{"config_id": 7, "value": "info",
 "update_referencing_deployments": true,
 "referencing_deployments": [{"id": 3, "version": 12}]}
```

`update_referencing_deployments` re-pins the deployments that use this config
to the new version atomically. When you set it you must list **every**
deployment currently referencing the config with its **current** version;
anything missing, extra, or stale is `409 referencing_deployments_changed` —
re-read global state and retry. Leave both fields out to append a version
without touching any deployment, then update specs yourself.

- `POST /v1/configs/rename` `{"config_id": 7, "new_name": "log-level"}`
- `POST /v1/configs/move` `{"config_id": 7, "value_directory_id": 3, "space_id": 0}`
- `POST /v1/configs/delete` `{"config_id": 7}` — destructive, see section 10.
- `/v1/value-directories/create` `{"space_id": 2, "parent_id": 0, "name": "app"}`,
  plus `/move`, `/rename`, `/delete` (must be empty).

**Configs are not secrets.** Their values are stored in plaintext and are
returned in global state. Never put a credential in one — use section 8.

## 8. Secrets

**The default posture: you can create a secret but not read one.** Secret
metadata is visible to
you in `secrets` (name, folder, version ids — never a value). Everything that
would expose or destroy a value is denied by default: `/v1/secrets/reveal`,
`/v1/secrets/set`, `/v1/secrets/create` (which carries a plaintext value),
`/v1/secrets/rename`, `/v1/secrets/move`, and `/v1/secrets/delete` return
`403` unless the administrator has granted them to you. When they are denied,
ask the operator to do those in the browser.

What you can do is `generate`, because the value is produced inside the server
and never leaves it:

```sh
curl -sS -X POST '{{.BaseURL}}/v1/secrets/generate' \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -d '{"name": "postgres-password", "space_id": 2, "password": {"length": 32}}'
```

The response is the secret metadata:

```json
{"id": 30, "fs": {"name": "postgres-password", "directory_id": 0},
 "space_versions": [{"id": 8, "space_id": 2}],
 "versions": [{"id": 12, "version": 1}]}
```

The root `id` is the stable identity; each entry in `versions` is one immutable
version, newest first. This is the one time you see these ids, so keep them.
Deployment env refs pin a **version**: put `versions[0].id` into the spec as

```json
"env_vars": {"POSTGRES_PASSWORD": {"secret_version_id": 12}}
```

and send it through `/v2/deployments/update` as in section 5. The workload
receives the value at spawn time; you never handle it.

- **`length`** defaults to 32 and must be 16–4096. Out of range is a `400`, not
  a clamp.
- **`include_symbols`** defaults to false. Leave it that way unless the operator
  asks otherwise — without `reveal` you cannot read the value back to debug a
  quoting problem in a shell or connection string.
- **The name must be new.** An existing name in that space root is `400`.
  `generate` cannot rotate a secret, only create one; rotating means
  `/v1/secrets/set`, so unless you hold that, ask the operator to rotate.
- Generated secrets land in the space root. Names are unique per folder, not
  globally.
- `password` is one specification among future others. Send exactly one.

`POST /v1/secrets/status` reports whether the store is unlocked. If
`unlocked` is false, every secret operation fails until the operator unlocks
it — that is not something you can fix.

## 9. Spaces

Spaces come from `spaces` in global state. You can rename one
(`/v1/spaces/update` `{"id": 2, "name": "staging"}`) and delete one
(`/v1/spaces/delete` `{"id": 2}`), but by default **not create one** —
`/v1/spaces/create` is `403` under the builtin rules. Deleting a space is the
most destructive call in this
API; treat it as section 10 and expect to be told no.

## 10. Rules that apply everywhere

- **Destructive operations need explicit confirmation first.** Deleting a
  deployment, asset, config, directory, or space is not something to do because
  it seemed implied. Ask, quote exactly what will be deleted, and wait.
- **Streaming endpoints are protobuf-only.** `/v1/global/state-stream` and
  `/v1/deployments/prepare-output` ignore `Accept: application/json`. Use the
  non-streaming endpoints above instead.
- **Enums are numbers** in JSON, not names.
- **Ids are per-kind.** Stable identity ids and version row ids are different
  number spaces; so are a deployment's `version` and its `space_version`.

## 11. Endpoint reference

Everything on the public API, and what the builtin rule templates allow an
agent session to call. "operator" means the endpoint exists but is denied to
agents *by default*; on this cluster your session may or may not hold it —
the live API's answer is the truth. A `403` will not change on retry: ask.

**Reading**

| Endpoint | |
|---|---|
| `GET /v1/global/state` | yes |
| `POST /v1/nodes/list` | yes |
| `POST /v1/deployments/get` `/history` `/versions` `/recently-deleted` | yes |
| `POST /v1/assets/list`, `GET /v1/assets/content` | yes |
| `POST /v1/configs/list`, `/v1/secrets/list`, `/v1/secrets/status` | yes |
| `POST /v1/value-directories/list`, `/v1/asset-directories/list` | yes |
| `POST /v1/repos/validate` | yes |
| `GET /v1/healthz` | yes, no auth |
| `POST /v1/deployments/log-query` | operator (logs) |
| `POST /v1/deployments/prepare-output` | operator (logs), protobuf stream |
| `POST /v1/global/state-stream` | protobuf stream |
| `POST /v1/global/exported-config` | operator (cluster) |

**Writing**

| Endpoint | |
|---|---|
| `POST /v1/deployments/create` `/delete`, `POST /v2/deployments/update` | yes |
| `POST /v1/assets/upload` `/rename` `/move` `/delete` | yes |
| `POST /v1/asset-directories/create` `/move` `/rename` `/delete` | yes |
| `POST /v1/configs/create` `/set` `/rename` `/move` `/delete` | yes |
| `POST /v1/value-directories/create` `/move` `/rename` `/delete` | yes |
| `POST /v1/secrets/generate` | yes |
| `POST /v1/spaces/update` `/delete` | yes |
| `POST /v1/spaces/create` | operator |
| `POST /v1/secrets/create` `/set` `/reveal` `/rename` `/move` `/delete` | operator (secret values) |
| `POST /v1/secrets/unlock` `/rotate-recovery-code` | operator (cluster) |
| `POST /v1/nodes/rename` `/allowed-spaces`, `/v1/nodes/enrollments/*` | operator (cluster) |
| `POST /v1/cluster-settings/get` `/update` | operator (cluster) |
| `POST /v1/access/rule-templates/*` `/grants/*` `/global-rules/*` | operator (cluster) |

**Sessions**

| Endpoint | |
|---|---|
| `POST /v1/agent-sessions/request-start` `/get-session` | yes, no auth |
| `POST /v1/agent-sessions/revoke` (your own id only) | yes |
| `GET /v1/auth/current/session` | yes, but see below |
| `POST /v1/agent-sessions/approve` `/create` `/list` | human-only |
| `/v1/auth/master*`, `/v1/auth/passkey/*`, `/v1/personal-sessions/*` | human-only |

`GET /v1/auth/current/session` returns the caller's own session — user id,
scopes, expiry, **and the bearer token itself**, since the web UI uses it to
restore a stored session. Use it to check your grants if you need to, but the
response contains your credential: never print it verbatim.
