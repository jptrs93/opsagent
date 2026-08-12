# Authentication and access control

## Overview

Authentication uses passkeys for normal operator login. A master password can issue a short-lived token for passkey registration, including bootstrap and recovery when an operator needs to enroll a replacement authenticator. Both flows produce a JWT token used for subsequent requests. Access control is enforced in two layers: per-route scope policies defined in the protobuf API contract, and per-user authz grants evaluated by `lib/authz` inside the handlers.

Key files:
- `backend/app/primary/webuihandler/auth.go` — master password handler, JWT verification, and `VerifyAuth`.
- `backend/lib/authz/` — grant, rule-template, and global-rule evaluation and storage.
- `backend/app/primary/webuihandler/access_enforce.go` — handler-side authz checks and per-user visibility filters.
- `backend/app/primary/webuihandler/access.go` — the `/v1/access/` CRUD surface for templates, grants, and global rules.
- `backend/app/primary/webuihandler/agent_sessions.go` — agent session request, approve, pickup, create, list, and revoke.
- `backend/app/primary/webuihandler/agent_instructions.go` / `.md` — the unauthenticated instructions page an operator hands to an agent.
- `backend/app/primary/webuihandler/passkey.go` — passkey registration and login handlers, credential persistence, and WebAuthn user adapter.
- `backend/apigen/policy_ext.go` — access control policy enforcement.

## User model

The `User` proto exposes `{id, name}` to the UI for audit display. The full `InternalUser` record (with WebAuthn ID and credentials) is stored in the SQLite `users` table keyed by integer id. The first user is created automatically when the master password is used for the first time; every new user starts with a `cluster_admin` grant, which can then be narrowed or replaced through the access-control layer below.

## Master password bootstrap

Fresh primary installs generate a high-entropy setup password and persist its Argon2id hash directly in the initial `primary.db` config envelope before systemd starts. The hash is never written to `/etc/opendeploy/env`; the installer prints the password once after the database has been initialized. It obtains a short-lived token for passkey registration and remains configured until rotated.

The configured master password hash is stored in the persisted OpenDeploy config envelope. It is owned by the config service but is not editable through the general settings update endpoint.

### Flow (`POST /v1/auth/master`)
1. Resolve the configured master password hash from the persisted OpenDeploy config envelope.
2. Verify the request password against the resolved hash using `authu.VerifyPassword` (constant-time comparison).
3. If no user exists, create one with a new UUID v7.
4. Return a JWT with `scopes: ["passkey:create"]` and 10-minute expiry.

### Rotation (`POST /v1/auth/master/password/save`)

An authenticated default-session user can save a replacement master password. The frontend may generate a random password locally, but the endpoint only accepts a supplied password, hashes it with `authu.HashPassword`, and stores the hash in the persisted OpenDeploy config envelope.

`POST /v1/auth/master/password/verify` checks a supplied password against the current configured hash without issuing a bootstrap token.

After registering a passkey, the master password is no longer needed for normal operation. It intentionally remains available as a recovery route for enrolling a new passkey or creating an additional operator user, until rotated through the authenticated master-password endpoint.

## JWT tokens

Tokens are signed with RSA-256 (RS256) via `github.com/jptrs93/goutil/authu`. Each token contains:
- `sub`: user ID.
- `scopes`: list of granted scopes.
- `exp`: expiration timestamp.
- `iat`: issued-at timestamp.
- `jti`: agent session ID. Present only on agent session tokens.

Three token types exist:
- **Bootstrap token**: scopes `["passkey:create"]`, 10-minute expiry. Issued by master password exchange.
- **Session token**: scopes `["default", "secrets_access"]`, 2-day expiry. Issued by passkey registration or login.
- **Agent session token**: the caller's scopes minus `secrets_access`, 6-hour expiry. Issued under `/v1/agent-sessions/` for command-line, script, and agent use.

`GET /v1/auth/current/session` is an authenticated validation endpoint that echoes the caller's current bearer token without minting a new one. The frontend uses it on app startup to confirm persisted auth state and to force re-login on `401`.

### Agent sessions

An agent session is a 6-hour bearer token for command-line, script, and agent use. The lifetime is deliberately shorter than the 2-day browser session because these tokens get pasted into shells and end up in history files and CI logs.

Whichever route mints one, `secrets_access` is dropped on the way through (`agentSessionScopes`), so an agent token can list secret metadata and reference secrets by id from deployment env, but cannot reveal, set, rename, or delete a secret value. Those calls return `403`. It *can* call `PostV1SecretsGenerate`, which creates a value it is then unable to read. This is the one place where an agent token is strictly weaker than its parent session, and it is deliberate: the token's longer reach into shell history and CI logs is a poor place to carry the right to read plaintext secrets. An operator who needs to change a secret does it in the browser.

All routes live under `/v1/agent-sessions/`, which is also the rate-limit prefix.

#### Request and approve (the normal path)

The operator pastes one line into their agent — "Load instructions for using our deployment orchestration platform from `<origin>/v1/agent-sessions/instructions?user_id=<id>`" — and the agent does the rest. Nothing is copied by hand and no credential passes through the operator.

1. `GET /v1/agent-sessions/instructions?user_id=` (`NO_AUTH`) renders the API instructions the agent needs, as markdown, or as an HTML wrapper when the `Accept` header prefers it. The source is `agent_instructions.md`, embedded and rendered through `text/template` with the base URL taken from the request. `user_id` is validated here so a mistyped URL fails immediately rather than hours into a session. It grants nothing.
2. `POST /v1/agent-sessions/request-start` (`NO_AUTH`) opens a `PENDING` row carrying `requesting_address` and an `approval_code`, and returns the row `id` plus that code. The two pull in opposite directions: **`id` is the pickup secret** — 32 random bytes, never displayed in full — while **`approval_code` exists to be read out**. `request-start` is unauthenticated by necessity, so without a code the operator has no way to tell their own agent's request from anyone else's that reached the server. Only one request may be open per user; a second returns `409`.
3. `POST /v1/agent-sessions/approve` (`default`) turns the operator's own pending row into `APPROVED` and freezes the approver's narrowed scopes onto it in the same statement, so a second approval cannot re-scope it.
4. `POST /v1/agent-sessions/get-session` (`NO_AUTH`) polls by `id`. On the first call after approval it mints the token, stores its hash, and returns the plaintext. Every later call returns status alone.

**The token is minted at pickup, not at approval.** Minting at approval would mean the plaintext had to sit in the database waiting to be collected, which is exactly what the hash-only rule below exists to prevent. It also means the 6-hour clock starts when the agent actually collects.

A request expires unapproved after `agentSessionPendingTTL` (10 minutes) and an approved one expires uncollected after `agentSessionPickupTTL` (15 minutes); both then read as `REJECTED`. These TTLs are what keep the one-open-request-per-user rule from becoming a denial of service — without them a single unauthenticated request would occupy an operator's only slot indefinitely. Expiry is applied lazily on the next `request-start` or `get-session`; nothing sweeps.

Rate limits in `run.go` back this up: the family gets 2/s burst 30 per IP to accommodate a 5s poll, `instructions` 0.2/s, and `request-start` 3 per 10 minutes. Nested prefixes stack.

#### Direct creation

`POST /v1/agent-sessions/create` (`default`) mints a token immediately and returns it once, for non-interactive callers with no agent waiting on an approval. It requires an existing `default` scope session and derives from the caller's own scopes, so it can never grant more access than the session that requested it — a `passkey:create` bootstrap token is rejected with `403` and cannot be traded up into general access. Rows land already `APPROVED` and collected.

#### Storage and revocation

Each session gets a row in `agent_sessions` (`id`, `user_id`, `created_at`, `expires_at`, `token_hash`, `token_prefix`, `revoked_at`, `scopes`, `status`, `requesting_address`, `approval_code`, `approved_at`). The row `id` is the token's `jti` claim, which is how verification finds it. `status` is the `AgentSessionStatus` enum — `PENDING`, `APPROVED`, `REJECTED`, `REVOKED` — and is authoritative; `revoked_at` survives only as the timestamp that goes with `REVOKED`. Expiry is *not* a status: it is derived from `expires_at` on read, so nothing has to sweep the table to keep the list honest. A pending row has no `token_hash`, `token_prefix`, or `expires_at` at all.

**Only the SHA-256 of the token is stored.** The plaintext is returned once and never again, so a copy of `primary.db` — including an off-box Litestream backup — carries no usable credential at any point in the lifecycle. `token_prefix` holds the leading 12 characters so an operator can tell two sessions apart; it is short enough to be useless on its own. `ClaimAgentSessionToken` guards the write with `length(token_hash) = 0` and reports rows affected, so two concurrent pickups can never both walk away with a working token.

`POST /v1/agent-sessions/revoke` (`default`) stops a session: a pending row becomes `REJECTED`, anything else `REVOKED`. This is real revocation, not a display change: `VerifyAuth` calls `verifyAgentSession`, which for any token carrying a `jti` loads the row and rejects the request unless the status is `APPROVED` and the token's hash matches the stored one. Bootstrap and browser session tokens carry no `jti` and keep the stateless fast path, so the extra indexed read applies only to agent-token traffic.

`POST /v1/agent-sessions/list` returns the caller's own sessions, newest first, and never returns a token. The web UI does not call it: sessions reach the browser through `PostV1GlobalStateStream`, which is the one field in `State` filtered to the connected user rather than broadcast. List, approve, and revoke are all scoped by `ctx.User.ID`, so one operator cannot act on another's session by guessing its id. What the resulting token can *do* is bounded twice: by the approver's scopes frozen onto the row, and by the authz layer, which sees any token carrying a `jti` as delegated and only matches rules with `delegation_allowed`.

Rows are not garbage collected; finished sessions accumulate as a record of what was issued and from where. Rotating the signing key still invalidates all outstanding tokens, sessions included.

Public keys are persisted in the SQLite `public_keys` table keyed by `kid`. Key rotation is handled by the `authu` package.

## WebAuthn passkeys

Passkeys use the FIDO2/WebAuthn standard via `github.com/go-webauthn/webauthn`. Discoverable resident keys and user verification are required.

### Relying party configuration

- When HTTPS Web UI is enabled, RPID is the first configured Web UI host from `ACME_HOSTS` (default `opendeploy.dev`) and origins are HTTPS versions of the configured hosts. `OPENDEPLOY_PASSKEY_EXTRA_ORIGINS` can append comma-separated additional origins, such as test tunnels with explicit ports.
- When only HTTP Web UI is enabled, RPID is `localhost` and origins are `http://localhost:8080` and `http://localhost:5173`.

### Registration flow

Requires an authenticated session (scope: `passkey:create` or `default`).

1. **Start** (`POST /v1/auth/passkey/register/start`): generates a session ID and WebAuthn creation options JSON.
2. The client performs the WebAuthn ceremony with the authenticator.
3. **Finish** (`POST /v1/auth/passkey/register/finish`): validates the credential, saves it to the credential store, and returns a session JWT with `scopes: ["default"]`.

### Login flow

No authentication required (discoverable login).

1. **Start** (`POST /v1/auth/passkey/login/start`): generates a session ID and assertion options.
2. The client completes the WebAuthn assertion.
3. **Finish** (`POST /v1/auth/passkey/login/finish`): verifies the assertion, resolves the user from the credential, and returns a session JWT.

### Credential storage

Credentials are persisted inside each user's `data_blob` column in the SQLite `users` table (protobuf-encoded `InternalUser` containing the full credential list). Lookup on login fetches all users and resolves the credential by its raw id.

## Access control

Two layers gate every request. The **scope layer** is coarse and token-level: each route in the `api-contract/*_service.proto` files declares an `AccessPolicy` checked by `VerifyAuth` before the handler runs. The **authz layer** is fine-grained and per-user: handlers ask `lib/authz` whether this user may perform this verb on this entity in this space. Both must pass.

### Scope layer

Route policies:
- `NO_AUTH`: no token required.
- `ANY_OF`: requires a valid JWT with at least one of the listed scopes.

Scopes in use:
- `passkey:create` — enroll a passkey, nothing else.
- `default` — ordinary operator access: deployments, assets, configs, spaces, and secret *metadata*.
- `secrets_access` — additionally reveal and change secret values. Gates `PostV1SecretsSet`, `Rename`, `Reveal`, `Delete`, `GenerateRecoveryCode`, and `Unlock`. `PostV1SecretsList` and `PostV1SecretsStatus` stay on `default` so metadata reads survive without it.

`PostV1SecretsGenerate` is the one deliberate exception: it writes a secret value from a `default`-scope call. The scope split is about *seeing* values, not about the store being read-only, and generation is the one write where the caller supplies a name and a specification rather than a value and gets back nothing but metadata. It is create-only and never echoes what it made — see [secrets.md](secrets.md#server-side-generation).

Because `ANY_OF` is a disjunction, a secrets-gated route lists `secrets_access` alone — adding `default` beside it would defeat the split.

`VerifyAuth` reads the route's policy from the generated mux, skips validation for `NO_AUTH`, verifies the bearer JWT's signature and expiry, checks its scopes against the policy, and populates the request context with the resolved user. Tokens carrying a `jti` (agent sessions) additionally mark the user **delegated** (`InternalUser.Delegated`, a runtime-only field never persisted) — the authz layer uses this below.

### Authz layer (`backend/lib/authz`)

Access is purely additive **grants** evaluated against a `RequestedAccess{verb, space, entity type, entity id, delegated}`; everything not granted is denied. A grant carries either a direct rule or a reference to a **rule template** with bound arguments. A rule is five positions — `spaces : entity types : entity refs : permissions : delegation` — where each of the first four is a selector (wildcard, value list, or template argument, with exclusions applied first) and `delegation_allowed` controls whether the rule matches delegated (agent-token) requests. **Global rules** are deny rules checked before any grant; `delegated_only` narrows one to agents. Global rules can never target the `access` entity, so an admin cannot deny themselves out of repairing a bad rule.

Two builtin templates are seeded (and re-asserted) at startup: `cluster_admin` (everything, plus a delegable everything-except-reveal-and-space-0 rule) and `space_admin(spaces)` (the same shape scoped to bound spaces). Both also carry a narrow delegable rule granting `view` on `node` and `user` entities in space 0 — without it, a space-limited operator or agent could not list nodes to place a deployment or resolve user names for audit display.

Entity-to-space mapping: deployments, secrets, configs, assets, and folders live in their record's space (values normalize a requested space `<= 0` to the default space — `state.NormalizedUserSpaceID` — and checks gate on the effective space). Spaces are their own entity with the space's id. Nodes, users, cluster settings, the config export, enrollment, secrets-store recovery/unlock, and access management itself are cluster-level: checked in space 0 against the `node`, `user`, `cluster`, and `access` entity types. Space *creation* is also cluster-level (the new space has no id yet).

Handler enforcement lives in `webuihandler/access_enforce.go` (`requireAccess`, `requireEntityAccess`) and follows one convention: an entity the caller cannot `view` reads as **404** (its existence is not leaked); a viewable entity without the requested verb reads as **403** `access_denied`. Moving an entity between spaces needs `edit` in the source and `create` in the destination. List endpoints filter to viewable items instead of erroring. Enforcement is active whenever `Handler.Authz` is wired, which `webuihandler.New` always does; handler tests that construct a bare `Handler` without it run unenforced.

The state stream (`PostV1GlobalStateStream`) applies the same `view` filters per connected user: space-scoped collections filter per item, cluster-scoped fields (nodes, users, enrollments, backup status, cluster config) are all-or-nothing on the space-0 check, and a space row is visible if the user holds *any* grant touching that space (`SpaceVisible`), not only explicit `space:view`. Authz collections are all-or-nothing on `access:view`, except every user always receives the template catalogue and their own grants so the UI can describe what they hold. Because snapshot fields replace wholesale on the client, a grant or rule change simply re-emits the full filtered state on every open stream — previously hidden items have no pending updates that could reveal them, so diffing is not attempted.

Lifecycle guarantees:
- Every newly created user is granted `cluster_admin`; a run-once migration (`migration.authz-cluster-admin-grants`) did the same for users predating the authz tables, so enforcement never locked out an existing install.
- `DeleteGrant` refuses (`409 access_last_admin`) to remove the last grant in the system conferring `create` on the `access` entity — self-lockout needs master-password recovery otherwise, and that is a recovery path, not a UX.
- Anyone holding `access:create` can grant any access, including more than they hold themselves. There is no attenuation; access management is full trust.

Deliberately unenforced: `PostV1ReposValidate` (a stateless validation helper touching no entity), `PostV1SecretsStatus` and the secrets-status stream field (two booleans every secret-capable page needs), and the personal auth/passkey/agent-session routes, which stay scoped to `ctx.User.ID` as before.
