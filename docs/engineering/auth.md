# Authentication and access control

## Overview

Authentication uses passkeys for normal operator login, with an opt-in username/password login for installs where a browser will not run WebAuthn (see [Password login](#password-login)). A master password can issue a short-lived token for passkey registration or password setup, including bootstrap and recovery when an operator needs to enroll a replacement authenticator. All flows produce a JWT token used for subsequent requests. What a token may *do* is decided in one place: per-user authz grants evaluated by `lib/authz` inside the handlers. Scopes remain on each route in the protobuf API contract, but only to separate a bootstrap token from a real session — they no longer carve up what a real session can reach.

Key files:
- `backend/app/primary/webuihandler/auth.go` — master password handler, JWT verification, and `VerifyAuth`.
- `backend/lib/authz/` — grant, rule-template, and global-rule evaluation and storage.
- `backend/app/primary/webuihandler/access_enforce.go` — handler-side authz checks and per-user visibility filters.
- `backend/app/primary/webuihandler/access.go` — the `/v1/access/` CRUD surface for templates, grants, and global rules.
- `backend/app/primary/webuihandler/agent_sessions.go` — agent session request, approve, pickup, create, list, and revoke.
- `backend/app/primary/webuihandler/agent_instructions.go` / `.md` — the unauthenticated instructions page an operator hands to an agent.
- `backend/app/primary/webuihandler/passkey.go` — passkey registration and login handlers, credential persistence, and WebAuthn user adapter.
- `backend/app/primary/webuihandler/password.go` — opt-in master-password login, the unauthenticated auth-methods endpoint, and the local CA download.
- `backend/apigen/policy_ext.go` — access control policy enforcement.

## User model

The `User` proto exposes `{id, name}` to the UI for audit display. The full `InternalUser` record (with WebAuthn ID and credentials) is stored in the SQLite `users` table keyed by integer id. A user is created automatically when the master password is exchanged with a username that does not exist yet; every new user starts with a `cluster_admin` grant, which can then be narrowed or replaced through the access-control layer below.

## Master password bootstrap

Fresh primary installs generate a high-entropy setup password and persist its Argon2id hash directly in the initial `primary.db` config envelope before systemd starts. The hash is never written to `/etc/opendeploy/env`; the installer prints the password once after the database has been initialized. It obtains a short-lived token for passkey registration and remains configured until rotated.

The configured master password hash is stored in the persisted OpenDeploy config envelope. It is owned by the config service but is not editable through the general settings update endpoint.

### Flow (`POST /v1/auth/master`)
1. Resolve the configured master password hash from the persisted OpenDeploy config envelope.
2. Verify the request password against the resolved hash using `authu.VerifyPassword` (constant-time comparison).
3. Look up the request's `username`; if no user with that name exists, create one (next integer id, fresh WebAuthn ID) with a `cluster_admin` grant.
4. Return a JWT with `scopes: ["passkey:create"]` and 10-minute expiry. The bootstrap page then registers a passkey, which ends in a full session.

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
- **Session token**: scopes `["default"]`, 2-day expiry. Issued by passkey registration or login, and by master-password login when that is enabled.
- **Agent session token**: the caller's own scopes, 6-hour expiry. Issued under `/v1/agent-sessions/` for command-line, script, and agent use.

`GET /v1/auth/current/session` is an authenticated validation endpoint that echoes the caller's current bearer token without minting a new one. The frontend uses it on app startup to confirm persisted auth state and to force re-login on `401`.

### Agent sessions

An agent session is a 6-hour bearer token for command-line, script, and agent use. The lifetime is deliberately shorter than the 2-day browser session because these tokens get pasted into shells and end up in history files and CI logs.

An agent token carries its parent session's scopes unchanged. Nothing is withheld at the token layer: the only thing that narrows an agent is the authz layer, which sees any token carrying a `jti` as **delegated** and matches only rules with `delegation_allowed`. Under the builtin templates that means an agent can see and create secrets but not reveal, change, or destroy one, and cannot view logs (which can echo secret values) — see [the authz layer](#authz-layer-backendlibauthz) — and an operator writing custom rules is free to decide otherwise. There is no separate list of things agents may not do.

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

`POST /v1/agent-sessions/list` returns the caller's own sessions, newest first, and never returns a token. The web UI does not call it: sessions reach the browser through `PostV1GlobalStateStream`, which is the one field in `State` filtered to the connected user rather than broadcast. List, approve, and revoke are all scoped by `ctx.User.ID`, so one operator cannot act on another's session by guessing its id. What the resulting token can *do* is decided by the authz layer, which sees any token carrying a `jti` as delegated and only matches rules with `delegation_allowed`; the scopes frozen onto the row only fix which token *type* it is.

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

## Password login

Password login exists for one reason: browsers only expose WebAuthn in a secure context, which means HTTPS with a certificate the browser trusts or plain HTTP on `localhost`. A single-node install reached over plain HTTP at a VM or LAN address cannot use passkeys, and one reached over HTTPS behind a certificate the operator clicked past is not a reliably supported passkey configuration (Chrome refuses outright; other browsers vary). Passkeys stay the default; password login is an opt-in that is off unless someone turns it on.

It is **master-password login**: there are no per-user passwords. With the setting on, the master password, which otherwise only mints a bootstrap token, opens a full session directly for whatever username is supplied, creating that user with `cluster_admin` on first use exactly as first-time setup does. Anyone holding the master password can therefore sign in under any name, which was already true through first-time setup; the setting only makes it the everyday login rather than a bootstrap-only route.

- **Gate.** `ClusterSettings.auth.password_login_enabled` (installer `--password-login true`, restore override `PASSWORD_LOGIN_ENABLED`, Settings → Authentication). While it is off, the login endpoint returns `403 password_login_disabled` and the UI shows no password controls, so a production install that never enables it carries no password login surface.
- **Discovery.** `GET /v1/auth/methods` (`NO_AUTH`) reports which methods are on (`passkey_login_enabled` is false when the WebAuthn service could not be initialised, see below) and whether a local CA is available for download. The login and bootstrap pages read it, because neither can see cluster settings before a session exists. The request is deferred to a microtask in `frontend/src/state/authMethods.js`: the API client reads the login state synchronously to build its auth header, and a page constructed inside the reactive route binding would otherwise capture a dependency on the login state and be rebuilt mid-flow when a token is stored.
- **Login** (`POST /v1/auth/password/login`, `NO_AUTH`). Username plus master password → a normal personal session, identical to a passkey login, recorded in `personal_sessions` and revocable from the Sessions page. A wrong password answers `401 invalid_master_password`. Rate limited at 0.2/s burst 10 per IP, the same as the bootstrap route. Usernames are trimmed on creation and matched trimmed on both sides, so accounts created before trimming with surrounding whitespace still resolve.
- **Passkeys stay optional at startup.** `webuihandler.New` fails on a relying-party configuration the WebAuthn library rejects, unless password login is on, in which case passkeys are logged as unavailable, the passkey routes answer `503 passkeys_unavailable`, and the login page says so. A password-only install can therefore never be locked out by its passkey configuration.

Over plain HTTP the master password crosses the network in clear text. The installer prints a warning when `--password-login` is combined with `--http-only`; the intended remedies are a loopback listen with an SSH tunnel, or HTTPS.

### Relying-party derivation

The Web UI hostnames setting (`https_web.acme_hosts`, installer `--web-hosts`, alias `--acme-hosts`) is the single source for both the certificate names and the WebAuthn relying party, in every mode. It defaults to `localhost` for HTTP-only and self-managed TLS installs. The RP ID is the first hostname that is a DNS name (WebAuthn forbids IP addresses; an address-only list falls back to `localhost`). Origins are every hostname under every enabled scheme, with the listen port appended when it is not the scheme default, plus the Vite dev server in HTTP-only mode and `OPENDEPLOY_PASSKEY_EXTRA_ORIGINS`. So `--http-only true --web-listen 127.0.0.1:9090` yields `http://localhost:9090`, and `--web-tls-self-managed true --web-listen :8443 --web-hosts mybox.local` yields `https://mybox.local:8443`.

### Local CA for self-managed TLS

Self-managed Web UI TLS without an operator-supplied bundle serves a leaf issued by a locally generated CA (`certu.EnsureWebUILocalTLS`), not a bare self-signed leaf, so the operator trusts one thing once and the leaf can be reissued freely. The CA and leaf live in the internal secrets store; the CA certificate is also written world-readable to `<data dir>/web-ca.crt` and served unauthenticated at `GET /v1/tls/ca.crt` while the UI is actually being served under it. The leaf covers the configured hostnames, the listen host, `localhost`, and the loopback addresses, and is reissued at startup or on a settings save whenever it no longer covers those names, was not signed by the current CA, or is within 30 days of expiry. The installer prints trust instructions for the OS and browser stores after such an install, and the login page carries the same instructions behind a **Trust the CA** button on its connection panel (a three-step overlay with the PEM inlined into each command and the CA fingerprint shown for checking) so an operator who continued through the warning can install the CA and reload.

## Access control

Two layers gate every request, but only one of them carries policy. The **scope layer** is token-level and now answers a single question — is this a real session or a bootstrap token: each route in the `api-contract/*_service.proto` files declares an `AccessPolicy` checked by `VerifyAuth` before the handler runs. The **authz layer** is where access is actually decided, per user, entity, and space: handlers ask `lib/authz` whether this user may perform this verb on this entity in this space. Both must pass.

There is deliberately no third layer. Route-level special cases for delegated tokens (an agent token used to have `secrets_access` stripped from it) have been removed: if agents should not do something, that belongs in a rule, where an admin can see it and change it.

### Scope layer

Route policies:
- `NO_AUTH`: no token required.
- `ANY_OF`: requires a valid JWT with at least one of the listed scopes.

Scopes in use:
- `passkey:create` — enroll a passkey, nothing else.
- `default` — a real session. Every authenticated route carries it, including the secrets routes; what the caller may do with any of them is the authz layer's decision.

`VerifyAuth` reads the route's policy from the generated mux, skips validation for `NO_AUTH`, verifies the bearer JWT's signature and expiry, checks its scopes against the policy, and populates the request context with the resolved user. Tokens carrying a `jti` (agent sessions) additionally mark the user **delegated** (`InternalUser.Delegated`, a runtime-only field never persisted) — the authz layer uses this below.

### Authz layer (`backend/lib/authz`)

Access is purely additive **grants** evaluated against a `RequestedAccess{verb, space, entity type, entity id, delegated}`; everything not granted is denied. A grant carries either a direct rule or a reference to a **rule template** with bound arguments. A rule is five positions — `spaces : entity types : entity refs : permissions : delegation` — where each of the first four is a selector (wildcard, value list, or template argument, with exclusions applied first) and `delegation_allowed` controls whether the rule matches delegated (agent-token) requests. **Global rules** come in two modes, with allow as the unflagged default. Deny rules (`deny`) are checked before any grant; `delegated_only` narrows one to agents, and they can never target the `access` entity, so an admin cannot deny themselves out of repairing a bad rule. Allow rules are evaluated alongside grants, exactly as if every user held the rule as a grant — `delegation_allowed` decides whether agents receive it too — and denies still beat them, so the order is always: global denies → (grants ∪ global allows). An allow may target `access`: it only adds.

Two builtin templates are seeded (and re-asserted) at startup: `cluster_admin` (everything) and `space_admin(spaces)` (the same scoped to bound spaces). Each then carries two delegable rules that together define what an agent session inherits:

- everything **except the `secret` entity type and the `view_logs` verb**, in every space except 0 (or the bound spaces) — cluster-level entities stay human-only even for a fully privileged agent, and logs stay off-limits because a deployment can echo secret values into them, which would sidestep the secret rule below;
- `view` and `create` on `secret` in the same spaces — an agent can see which secrets exist, mint a credential, and wire it into a deployment, but cannot reveal, edit, or delete one.

Two defaults replace what used to be a per-template "directory rule". First, **node visibility is derived**: a node is visible when an explicit `node:view` grant covers it *or* its `allowed_spaces` intersects the spaces the caller can see (space 0 excluded — every node allows it as an invariant). Since a new node allows every space, a space-limited operator sees every node until an admin narrows one away from their spaces; narrowing a node's allow list triggers a full re-filter of open state streams so lost nodes actually disappear. Editing a node still requires `node:edit`; a derived-visible node without it reads as 403, not 404. Second, a **seeded allow-mode global rule** `default_user_visibility` (`allow view on all users by user + agents`) makes the user roster visible to everyone so audit displays can resolve names. It is seeded exactly once — `SeedAuthzGlobalRule` inserts only when no row (live or soft-deleted) has ever carried the rule's name, so a deletion tombstone blocks re-seeding — and is not re-asserted at startup: an admin who deletes it has opted out, and reverse lookups then degrade to "unknown" in the UI. Note the seeded rule touches space 0, so the `_system` space row is visible to every user via `SpaceVisible`, which counts allow rules like grants.

These are defaults, not guarantees. An admin who writes a rule with `delegation_allowed` covering `secret : reveal` gets exactly that; nothing outside the rules second-guesses it. `PostV1SecretsGenerate` is what makes `secret : create` a safe verb to hand an agent: it takes a name and a specification, seals the value it produces, and returns only metadata — see [secrets.md](secrets.md#server-side-generation). `PostV1SecretsCreate`, where the caller supplies the value, additionally requires `secret : reveal` in the target space: a caller that chose the value knows it, so a value-supplying create is treated as a read.

Entity-to-space mapping: deployments, secrets, configs, assets, and folders live in their record's space (values normalize a requested space `<= 0` to the global space (space 1) — `state.NormalizedUserSpaceID` — and checks gate on the effective space). Spaces are their own entity with the space's id. Nodes, users, cluster settings, the config export, enrollment, secrets-store recovery/unlock, and access management itself are cluster-level: checked in space 0 against the `node`, `user`, `cluster`, and `access` entity types. Space *creation* is also cluster-level (the new space has no id yet).

Handler enforcement lives in `webuihandler/access_enforce.go` (`requireAccess`, `requireEntityAccess`) and follows one convention: an entity the caller cannot `view` reads as **404** (its existence is not leaked); a viewable entity without the requested verb reads as **403** `access_denied`. Moving an entity between spaces needs `edit` in the source and `create` in the destination. List endpoints filter to viewable items instead of erroring. Enforcement is active whenever `Handler.Authz` is wired, which `webuihandler.New` always does; handler tests that construct a bare `Handler` without it run unenforced.

The state stream (`PostV1GlobalStateStream`) applies the same `view` filters per connected user: space-scoped collections filter per item, cluster-scoped fields (enrollments, backup status, cluster config) are all-or-nothing on the space-0 check, nodes and node statuses filter per node through the derived-visibility rule above, users filter per row (the seeded roster rule normally shows all of them), and a space row is visible if the user holds *any* grant — or any allow-mode global rule — touching that space (`SpaceVisible`), not only explicit `space:view`. Authz collections are all-or-nothing on `access:view`, except every user always receives the template catalogue and their own grants so the UI can describe what they hold. Because snapshot fields replace wholesale on the client, a grant, rule, or node allow-list change simply re-emits the full filtered state on every open stream — previously hidden items have no pending updates that could reveal them, so diffing is not attempted.

Lifecycle guarantees:
- Every newly created user is granted `cluster_admin`; a run-once migration (since removed, marker `migration.authz-cluster-admin-grants`) did the same for users predating the authz tables, so enforcement never locked out an existing install.
- `DeleteGrant` refuses (`409 access_last_admin`) to remove the last grant in the system conferring `create` on the `access` entity — self-lockout needs master-password recovery otherwise, and that is a recovery path, not a UX.
- The Users page adds its own rails ahead of that error (`grantRevokeBlock` in `frontend/src/lib/authz.js`): a `cluster_admin` grant shows a padlock instead of a revoke × when it is the holder's own or the last `cluster_admin` in the cluster, and any other `cluster_admin` revoke asks for confirmation first. These are narrower than the backend guard — they watch the one role rather than every access-managing grant — so the 409 remains the real backstop.
- Anyone holding `access:create` can grant any access, including more than they hold themselves. There is no attenuation; access management is full trust.

Deliberately unenforced: `PostV1ReposValidate` (a stateless validation helper touching no entity), `PostV1SecretsStatus` and the secrets-status stream field (two booleans every secret-capable page needs), and the personal auth/passkey/agent-session routes, which stay scoped to `ctx.User.ID` as before.
