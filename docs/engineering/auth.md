# Authentication and access control

## Overview

Authentication is passkey-only. A master password is used once to bootstrap the first passkey. Both flows produce a JWT token used for subsequent requests. Access control is enforced per-route via policies defined in the protobuf API contract.

Key files:
- `backend/handler/auth.go` — master password handler, JWT signing/verification, token generation.
- `backend/handler/passkey.go` — passkey registration and login handlers, credential persistence, and WebAuthn user adapter.
- `backend/apigen/policy_ext.go` — access control policy enforcement.

## Single-user model

Opsagent is a single-admin tool. The `User` proto exposes `{id, name}` to the UI for audit display. The full `InternalUser` record (with WebAuthn ID and credentials) is stored in the SQLite `users` table keyed by integer id. The first user is created automatically when the master password is used for the first time.

## Master password bootstrap

The master password is provisioned via the `OPSAGENT_MASTER_PASSWORD_HASH` environment variable, which holds an argon2id hash produced by `authu.HashPassword`. It is used only to obtain a short-lived token for passkey registration.

### Flow (`POST /v1/auth/master`)
1. Verify the request password against `OPSAGENT_MASTER_PASSWORD_HASH` using `authu.VerifyPassword` (constant-time comparison).
2. If no user exists, create one with a new UUID v7.
3. Return a JWT with `scopes: ["passkey:create"]` and 10-minute expiry.

After registering a passkey, the master password is no longer needed. It can be removed from the environment.

## JWT tokens

Tokens are signed with RSA-256 (RS256) via `github.com/jptrs93/goutil/authu`. Each token contains:
- `sub`: user ID.
- `scopes`: list of granted scopes.
- `exp`: expiration timestamp.
- `iat`: issued-at timestamp.

Two token types exist:
- **Bootstrap token**: scopes `["passkey:create"]`, 10-minute expiry. Issued by master password exchange.
- **Session token**: scopes `["default"]`, 7-day expiry. Issued by passkey registration or login.

`GET /v1/auth/current/session` is an authenticated validation endpoint that echoes the caller's current bearer token without minting a new one. The frontend uses it on app startup to confirm persisted auth state and to force re-login on `401`.

Public keys are persisted in the SQLite `public_keys` table keyed by `kid`. Key rotation is handled by the `authu` package.

## WebAuthn passkeys

Passkeys use the FIDO2/WebAuthn standard via `github.com/go-webauthn/webauthn`. Discoverable resident keys are required. User verification is preferred.

### Relying party configuration

- Local dev (`OPSAGENT_LOCAL_DEV=true`): RPID is `localhost`, origin is `http://localhost:5173`.
- Production: RPID is the first value from `OPSAGENT_ACME_HOSTS` (default `opsagent.dev`), origins are HTTPS versions of all configured hosts.

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

Enforcement happens in `VerifyAuth` before the handler runs:
1. Read the route's policy from the generated mux.
2. If `NO_AUTH`, skip validation.
3. Extract the JWT from the `Authorization: Bearer <token>` header.
4. Verify the token signature and expiration.
5. Check that the token's scopes satisfy the policy.
6. Populate the request context with the authenticated user ID.
