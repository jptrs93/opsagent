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
names are `snake_case`, matching the examples exactly. Do not retry a `4xx` —
it will fail again. Retry `5xx` and connection errors.

## 3. Reading state

`GET /v1/global/state` is the starting point for everything. It returns all
deployments, assets, configs, secret metadata, and spaces, with the ids the
other endpoints expect. Read it before you change anything.

`POST /v1/deployments/get` with `{"id": <deployment id>}` returns one
deployment's live status: whether it is running, what it is preparing, and why
it last failed.

## 4. Assets

```sh
curl -sS -X POST '{{.BaseURL}}/v1/assets/upload?key=nginx.conf' \
  -H "Authorization: Bearer $TOKEN" -H 'Accept: application/json' \
  --data-binary @nginx.conf
```

**Use `?key=`, never `?name=`.** `?key=` appends a new version to the existing
asset, which is almost always what you want. `?name=` means "create a new
asset", and if that name is taken the server silently suffixes it — you get a
separate asset called `nginx.conf1`, a `200` response, and a deployment still
pointing at the original. Nothing tells you it went wrong.

Assets are versioned and immutable. Editing means uploading a new version and
pointing the deployment at it.

## 5. Changing a deployment

`POST /v1/deployments/update` with `{"deployment_id": <id>, "spec": {...},
"version": <current + 1>}`.

**`spec` is a full replacement.** There is no merge and no partial update. Any
field you leave out is *dropped*, and the call still returns `200`. So always:

1. `GET /v1/global/state` and take the deployment's current `spec` and `version`.
2. Modify that object in place.
3. Send the whole thing back with `version` set to `current + 1`.

If `version` is not exactly one greater than the stored one the call is
rejected — that is the concurrency check, and it means someone else changed the
deployment while you were working. Re-read and redo your change on top.

After any change, poll `POST /v1/deployments/get` until the deployment
settles. A `200` from `update` means the config was accepted, not that the
workload is running.

## 6. Secrets

**You cannot read a secret value.** Revealing, setting, renaming, or deleting
one returns `403`. That is intentional and there is no way around it — ask the
operator to do those in the browser.

You *can* create one, because creating it does not require seeing it. The server
generates the value, stores it encrypted, and returns only metadata:

```sh
curl -sS -X POST '{{.BaseURL}}/v1/secrets/generate' \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -d '{"name": "postgres-password", "password": {"length": 32}}'
```

The response is `{"id": 12, "name": "postgres-password", ...}`. Put that `id`
into the deployment's env as a `secret_id` reference and the workload receives
the value at spawn time:

```json
"env_vars": {"POSTGRES_PASSWORD": {"secret_id": 12}}
```

Send that through `deployment/update` as in section 5. You never handle the
value at any point.

- **`length`** defaults to 32 and must be 16–4096. Out of range is a `400`, not
  a clamp.
- **`include_symbols`** defaults to false. Leave it that way unless the operator
  asks otherwise — you cannot read the value back to debug a quoting problem in
  a shell or connection string.
- **The name must be new.** An existing name returns `400`. You cannot rotate a
  secret, only create one; ask the operator to rotate.
- `password` is one specification among future others. Send exactly one.

## 7. Limits

- **Streaming endpoints are protobuf-only.** `/v1/global/state-stream` and
  `/v1/deployments/log-search` do not honour `Accept: application/json`. Use the
  non-streaming endpoints above instead.
- **Enums are numbers** in JSON, not names.
- **Destructive operations** — deleting deployments, assets, or spaces —
  require the operator's explicit confirmation first. Ask before attempting one.
