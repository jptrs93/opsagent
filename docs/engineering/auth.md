# Authentication and access control

## Overview

Authentication uses passkeys for normal operator login. A master password can issue a short-lived token for passkey registration, including bootstrap and recovery when an operator needs to enroll a replacement authenticator. Both flows produce a JWT token used for subsequent requests. Access control is enforced per-route via policies defined in the protobuf API contract.

Key files:
- `backend/app/primary/webuihandler/auth.go` — master password handler, JWT signing/verification, agent session create/list/revoke.
- `backend/app/primary/webuihandler/passkey.go` — passkey registration and login handlers, credential persistence, and WebAuthn user adapter.
- `backend/apigen/policy_ext.go` — access control policy enforcement.

## Single-user model

OpenDeploy is a single-admin tool. The `User` proto exposes `{id, name}` to the UI for audit display. The full `InternalUser` record (with WebAuthn ID and credentials) is stored in the SQLite `users` table keyed by integer id. The first user is created automatically when the master password is used for the first time.

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
- **Agent session token**: the caller's scopes minus `secrets_access`, 12-hour expiry. Issued by `POST /v1/agent/sessions/create` for command-line, script, and agent use.

`GET /v1/auth/current/session` is an authenticated validation endpoint that echoes the caller's current bearer token without minting a new one. The frontend uses it on app startup to confirm persisted auth state and to force re-login on `401`.

### Agent sessions (`POST /v1/agent/sessions/create`)

Requires an existing `default` scope session. The minted token derives from the caller's scopes rather than a fixed list, so it can never grant more access than the session that requested it — a `passkey:create` bootstrap token is rejected with `403` and cannot be traded up into general access. The 12-hour lifetime is deliberately shorter than the 2-day browser session because these tokens get pasted into shells and end up in history files and CI logs.

`secrets_access` is dropped on the way through (`agentSessionScopes`), so an agent token can list secret metadata and reference secrets by id from deployment env, but cannot reveal, create, rename, or delete a secret value. Those calls return `403`. This is the one place where an agent token is strictly weaker than its parent session, and it is deliberate: the token's longer reach into shell history and CI logs is a poor place to carry the right to read plaintext secrets. An operator who needs to change a secret does it in the browser.

The Sessions page in the web UI exposes this as a copyable `export OPENDEPLOY_URL=...` / `export OPENDEPLOY_TOKEN=...` pair, with the URL taken from the page's own origin.

#### Storage and revocation

Each issued token gets a row in `agent_sessions` (`id`, `user_id`, `created_at`, `expires_at`, `token_hash`, `token_prefix`, `revoked_at`, `scopes`). The row `id` is the token's `jti` claim, which is how verification finds it.

**Only the SHA-256 of the token is stored.** The plaintext is returned once at creation and never again, so a copy of `primary.db` — including an off-box Litestream backup — carries no usable credential. `token_prefix` holds the leading 12 characters so an operator can tell two sessions apart in the list; it is short enough to be useless on its own.

`POST /v1/agent/sessions/revoke` stamps `revoked_at`. This is real revocation, not a display change: `VerifyAuth` calls `verifyAgentSession`, which for any token carrying a `jti` loads the row and rejects the request if it is missing, revoked, or if the token's hash does not match the stored one. Bootstrap and browser session tokens carry no `jti` and keep the stateless fast path, so the extra indexed read applies only to agent-token traffic.

`POST /v1/agent/sessions/list` returns the caller's own sessions, newest first, and never returns a token. Both list and revoke are scoped by `ctx.User.ID`, so one operator cannot revoke another's session by guessing its id — but note this is scoping rather than isolation: every user holds the same scopes and there is no admin role.

Rows are not garbage collected; expired sessions accumulate as a record of what was issued. Rotating the signing key still invalidates all outstanding tokens, sessions included.

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

Each route in `api.proto` declares an `AccessPolicy`:
- `NO_AUTH`: no token required.
- `ANY_OF`: requires a valid JWT with at least one of the listed scopes.

Scopes in use:
- `passkey:create` — enroll a passkey, nothing else.
- `default` — ordinary operator access: deployments, assets, configs, spaces, and secret *metadata*.
- `secrets_access` — additionally reveal and change secret values. Gates `PostV1SecretsSet`, `Rename`, `Reveal`, `Delete`, `GenerateRecoveryCode`, and `Unlock`. `PostV1SecretsList` and `PostV1SecretsStatus` stay on `default` so metadata reads survive without it.

Because `ANY_OF` is a disjunction, a secrets-gated route lists `secrets_access` alone — adding `default` beside it would defeat the split.

Enforcement happens in `VerifyAuth` before the handler runs:
1. Read the route's policy from the generated mux.
2. If `NO_AUTH`, skip validation.
3. Extract the JWT from the `Authorization: Bearer <token>` header.
4. Verify the token signature and expiration.
5. Check that the token's scopes satisfy the policy.
6. Populate the request context with the authenticated user ID.
