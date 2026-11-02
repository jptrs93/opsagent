# User enrolment, per-user passwords, and root sessions

Plan for replacing master-password login with per-user passwords, a root session for the master password, an enrol-or-recover flow with single-use links, and a self-service account page. The UX was built and judged first in `frontend/fixtures/user-enrol/`; this document covers the model, the server, the wiring, and the order of work.

**Status (2026-10-23): implemented.** Steps 1 to 6 below have shipped; the current behaviour is documented in [auth.md](../engineering/auth.md) and [frontend.md](../engineering/frontend.md). This page stays for the reasoning and the [deferred](#deferred) items. Two departures from the plan as first written: the `activated_users_are_named` law was dropped, because redeeming a link or opening a session for an invitee activates an account that has no name yet (the landing page asks for one next); and the event stream routes accept a root session (with a fixed visible set of users, spaces, templates and grants) instead of the enrol widget fetching those lists through separate endpoints.

## Goals

- A user signs in with a username and password by default. Passkeys are an add-on a user registers for themselves; a cluster setting can require them.
- The master password no longer signs anyone in. It opens a short-lived root session whose only powers are creating users, granting to them, issuing links, and opening a session for a user.
- New users get their privileges at creation. The default `cluster_admin` grant on first login goes away.
- Enrolment and recovery hand over either a single-use link or a direct session. A user is pending until a session is opened for them.
- The install story loses its sign-in dimension: password sign-in works everywhere, passkeys work once HTTPS is trusted, and `--password-login` is no longer needed.

Out of scope for this work: PAKE protocols (SRP, OPAQUE), revoking other sessions on recovery, showing root sessions in the UI, disabling users, SSO, and second factors. The model leaves room for each; see [Deferred](#deferred).

## Current state

- `users` rows hold a name and WebAuthn credentials. There is no per-user password. See [auth.md](../engineering/auth.md).
- `POST /v1/auth/master` takes a username and the master password, creates the user with `cluster_admin` if the name is new, and opens a `Bootstrap` session that may only register a passkey.
- `POST /v1/auth/password/login`, gated by `auth.password_login_enabled`, opens a `Full` session for any username with the master password.
- The Users page cannot create users; its New user dialog points at the login screen.
- Route policies in `api-contract/api_service.proto` separate the two session kinds with the scopes `full` and `bootstrap`. What a `full` session may do is decided by authz grants, not scopes.

## Model

### Session kinds

`UserSessionKind` becomes:

| Kind | Opened by | Holder | Lifetime | Accepted as bearer |
|---|---|---|---|---|
| `Full` | password login, passkey login, link redemption, root open-session | a user | 2 days | yes; authz decides what it may do |
| `Root` | master password exchange | nobody (`user_id` absent) | 10 minutes | yes; fixed allowlist, no authz |
| `Link` | create-link by a root session or a grant holder | the target user | 7 days | no; redeemed once by the redeem endpoint |

`Bootstrap@1` is renamed to `Root@1` and changes meaning. Every existing `Bootstrap` row is expired, so no live token is affected and no historical row is misread in a way that matters. `Link` is a new value.

### Root

A root session writes as author 0, which the UI already renders for seeded records. Its allowlist is exactly: create user, create grant, create link, open session for user, and the current-session read. Every other route rejects it through the route policy. Root never registers a passkey and never reads cluster data.

A grant holder on the Users page reaches create user, create grant and create link through authz (`create` on `user` and `access`). Open session for user is root-only; it is the one place an account can be entered without its own credential, and the master password is already that credential today.

### Links

A link is a `UserSession` row of kind `Link`: token minted and hashed like a bearer, `user_id` the target, `author` the issuer, `expires_at` seven days out. The token carries a distinct prefix, `l_`, so `VerifyAuth` rejects it before any lookup. Redemption is an unauthenticated endpoint that loads the row by id, compares the hash in constant time, rejects an expired, revoked or redeemed row, stamps `redeemed_at`, opens a `Full` session for the user with the redeeming device's address and agent, and sets `activated_at` on the user if absent. Revoking an unredeemed link uses the existing session revoke endpoint.

Enrol and recovery links are the same row kind. The difference is only whether the user was already activated.

### Pending users

A user is pending while `activated_at` is absent. It is set once, the first time a `Full` session is opened for the user by any route. Password login with an admin-set password activates. Nothing else about a pending user differs; grants attach to the row from creation.

### Passwords

A user's password is stored as `password_hash` using `authu.HashPassword` (Argon2id, 64 MiB, 3 iterations, 16-byte salt). It is never sent to the browser. Login compares with `authu.VerifyPassword`. The rate limit on the login route is unchanged.

A user may not delete their last passkey while they have no password, and may not clear a password while they have no passkey. The check lives in the handlers, not in a law, because a freshly redeemed link legitimately leaves an activated user with neither for a moment.

## Schema

### `datamodel/core/users.dm`

`User@9`:

```
name@2: ?string [min_length = 1]
email@4: ?string [min_length = 1]
password_hash@5: ?string [min_length = 1]
activated_at@6: ?i64
```

- `name` is optional so an invitee can choose it from the link. The `user_name` uniqueness law keeps its meaning; confirm how `unique` treats absent values and guard with `where name is present` if needed.
- `email` is unused and exists for SSO later. Unique, same guard.
- `password_hash` is stripped by `browserEntity` with `authentication`.
- Laws:

```
activated_once on update {
    assert before.activated_at is absent || before.activated_at == after.activated_at
}
```

A user may be activated while still nameless: a link redeemed before the invitee has chosen a name opens their session first, and the landing page collects the name.

`WebAuthnCredential`:

```
label@3: string
created_at@4: i64 [mutable = false]
last_used_at@5: ?i64
```

`UserSession@19`:

```
user_id@3: ?User.id [mutable = false]
kind@4: enum UserSessionKind {
    Full@0
    Root@1
    Link@2
} [mutable = false]
redeemed_at@10: ?i64
opened_by@11: ?UserSession.id [mutable = false]
issued_by@12: ?User.id [mutable = false]
```

- Laws:

```
root_has_no_user {
    assert user_id is present || kind == Root
}
redeemed_once on update {
    assert before.redeemed_at is absent || before.redeemed_at == after.redeemed_at
}
only_links_redeem {
    assert redeemed_at is absent || kind == Link
}
```

- `opened_by` on a `Full` row names the `Link` row it was redeemed from.
- `issued_by` on a `Link` row names the issuing user (absent for root). It was added during implementation: `author` is the last writer and becomes the target on redemption, so the issuer's list and stream key on `issued_by` instead.
- For a `Link` row, `requesting_address` and `user_agent` are the issuer's.

### `datamodel/core/system_config.dm`

```
auth@7: type AuthSettings {
    password_login_enabled@1: BoolSetting
    require_passkey@2: BoolSetting
}
```

Field 1 stays declared for the config history and is no longer read. `master_password_hash` is unchanged.

### Proto

- `model/sessions.proto`: `UserSessionKind` values `FULL = 0`, `ROOT = 1`, `LINK = 2`; `UserSession` gains `optional uint64 user_id`, `optional int64 redeemed_at`, `optional uint64 opened_by`.
- `model/user.proto`: `optional string name`, `optional string email`, `optional int64 activated_at`, `optional string password_hash` marked absent on the browser stream, and `label`, `created_at`, `optional last_used_at` on the credential.
- `model/system_config.proto`: `require_passkey = 2` on `AuthSettings`.
- `model_auth_operations.proto` and `model_user_operations.proto`: the request and response shapes under [Endpoints](#endpoints).

The `users` and `user_sessions` tables are materialised from the write log and take the new columns from the model. The data-model conversion tooling must accept `name` loosening from required to optional; verify with the release migration check before merging.

## Endpoints

Policy scopes become `full` and `root`. Authz continues to decide what a `full` session may do.

| Route | Policy | Request | Response | Effect |
|---|---|---|---|---|
| `POST /v1/auth/master` | no auth, rate limited | master password | `LoginResponse` kind `Root` | opens a `Root` row |
| `POST /v1/auth/password/login` | no auth, rate limited | username, password | `LoginResponse` | verifies `password_hash`; refuses with `passkey_required` when `auth.require_passkey` is on; opens `Full`; activates |
| `POST /v1/auth/link/redeem` | no auth, rate limited as `/v1/auth/master` | token | `LoginResponse` plus `name_set`, `password_set` | redeems; opens `Full` with `opened_by`; activates |
| `POST /v1/users/create` | root, or full with `create` on `user` and `access` | optional name, optional password, grant sources | `CoreWriteUpdate` with the user | writes the user, `password_hash` if given, and the grants in one commit |
| `POST /v1/users/link` | root, or full with `update` on `user` | user id | token, session id, `expires_at` | writes a `Link` row |
| `POST /v1/auth/open-session` | root only | user id | `LoginResponse` | opens `Full`; activates |
| `GET /v1/account` | full | — | the caller's user with credentials (label, created, last used) and `has_password` | self-only projection; the stream's user entity stays stripped |
| `POST /v1/account/rename` | full | name | `CoreWriteUpdate` | unique-name check; the pending-user name case included |
| `POST /v1/account/password` | full | optional current, next | empty | current required when a hash exists; last-way-in check on clear |
| `POST /v1/account/passkeys/delete` | full | credential id | empty | last-way-in check |
| `POST /v1/auth/passkey/register/*` | full | unchanged | unchanged | `root` scope removed; registration records `label` and `created_at` |

Removed: `PostV1AuthMaster` no longer takes a username. The `auth.password_login_enabled` gate on password login goes; `GET /v1/auth/methods` reports `require_passkey` instead of `password_login_enabled`.

Passkey login stamps `last_used_at` on the credential that signed.

## Server changes

- `webuihandler/auth.go`: `VerifyAuth` rejects the `l_` prefix; `resolveUserSession` rejects kind `Link`; a `Root` row resolves with no user and `res.User` empty. `PostV1AuthMaster` drops `resolveOrCreateUser` and opens a `Root` row. `startUserSession` gains the `opened_by` and activation steps.
- `webuihandler/password.go`: per-user verification; `passkey_required` error; the auth-methods response.
- New `webuihandler/enrol.go`: create user, create link, redeem, open session. New `webuihandler/account.go`: the four self-service routes.
- `domain/users`: write helpers for `password_hash`, `activated_at`, credential metadata; the last-way-in rule as one function both handlers call.
- `domain/authz`: no rule changes. `CreateGrant` already takes an author; root passes 0.
- `visibility.go`: `browserEntity` strips `password_hash`; `Root` rows have no owner and are never forwarded; `Link` rows forward to their `user_id` like other sessions.
- `app/primary/run.go`: rate limit `/v1/auth/link/redeem` at 0.2/s burst 10 per IP, and `/v1/auth/open-session` the same.
- Logging follows [logging.md](../engineering/logging.md): one line per session open with `session_kind` and `user`, one per redemption with the link's session id.

## Frontend

- `pages/bootstrap.js` is replaced by `pages/enrol.js`: the master-password step, then `components/userEnrol.js` built on submit (the detached-DOM pitfall in [frontend.md](../engineering/frontend.md)). The login page footer link reads "Enrol or recover a user".
- `pages/login.js`: the password form is the primary path and the passkey button secondary unless `require_passkey` is on, in which case only the passkey button shows. The username placeholder loses its master-password wording.
- New `pages/enrolLanding.js` at `/enrol/<token>`: redeems on load, then asks for a username if `name_set` is false, a password if `password_set` is false, offers passkey registration when the browser supports it, and navigates home. A failed redemption shows the reason and a link to the login page.
- `pages/users.js`: pass `enrol` and `account` from `dashboard.js` with real actions; the pending tag reads `activated_at`.
- `components/userEnrol.js` and `components/accountPanel.js`: the fixture seams become the generated `capi` calls. The grant list from the enrol widget goes in the create request, not as separate grant calls.
- `pages/sessions.js`: list `Link` rows under their own heading with issued-by and redeemed state; the revoke control cancels an unredeemed link.
- `pages/settings.js`: the Authentication group shows "Require passkey sign-in" in place of "Password login"; the master-password rows are unchanged.
- `state/login.js`: the login state's `kind` recognises `Root`; the dashboard refuses to mount for a `Root` session and keeps the user on the enrol page.

## Installer and settings

- `--password-login` is accepted and ignored with a printed notice for one release, then removed. The plain-HTTP warning stays, reworded: passwords cross the network unencrypted over plain HTTP.
- Installer output after bootstrap: "Use this password under Enrol or recover in the web UI to create the first administrator."
- Restore override `PASSWORD_LOGIN_ENABLED` is dropped; `REQUIRE_PASSKEY` is added.

## Documentation

- `docs/engineering/auth.md`: rewrite the bootstrap, password login and session kind sections to this model.
- `docs/product/deployments.md`: no change.
- `user-docs` Getting started: the demo install drops `--password-login true`; sign-in on both paths is username and password from the enrol step; the passkey callout moves to the You tab. The install table loses its sign-in column.

## Tests

- Go: `webuihandler/auth_test.go` for root exchange, link mint and redeem (single use, expiry, revoked, wrong hash), open-session activation, password login against a per-user hash, `passkey_required`, last-way-in refusals, `browserEntity` stripping `password_hash`.
- Data model: law tests for `root_has_no_user`, `redeemed_once`, `only_links_redeem`, `activated_once`.
- Frontend unit: no new pure modules; the widget and panel stay fixture-verified.
- E2E: `bootstrap-enroll-nixdocker.js` changes from "master password plus passkey registration" to "master password, enrol admin with a password, sign in, register a passkey from You". `access-enforcement.js` creates its non-admin users through create-user with grants instead of the login-screen route. A new `enrol-links.js` covers issue, redeem, second redeem refused, and cancel.

## Order of work

1. Data model and proto: the schema above, codegen, conversion check against a copied production database. Checkpoint: existing tests pass with the renamed kind and the optional `name`.
2. Server core: `VerifyAuth`, root exchange, per-user password login, `require_passkey`, create user, create link, redeem, open session. Checkpoint: Go tests for every row of the endpoint table.
3. Self-service: account read, rename, password, passkey delete, credential metadata on registration and login.
4. Frontend wiring: enrol page, login page, landing page, dashboard hooks, sessions and settings rows. Checkpoint: the fixture's mock seams are deleted or reduced to the users fixture.
5. Installer, restore overrides, installer output.
6. E2E cases, engineering doc, user docs.

Steps 1 and 2 ship together; a primary running step 1 alone has no way to create users. Steps 3 to 6 can follow in separate releases.

## Deferred

- Revoking other sessions on recovery: a flag on the redeem endpoint, default off.
- Root sessions in the UI: a filter on the Sessions page; the rows already exist.
- Disabling a user: `disabled_at` beside `activated_at`, checked in `VerifyAuth`.
- SSO: a login method that resolves a user by `email`.
- Second factor: a step after password login, gated per user.
- PAKE: replace the password login and set-password endpoints; sessions and enrolment are unaffected.
