# Upgrade log

Notes for operators upgrading a cluster, newest release first. Each entry
covers what changes on disk and on the wire, what to check before upgrading,
and what to expect during and after the rollout.

## v0.0.614 (unreleased)

### What changed

- **Secret ciphertexts are bound to the secret id alone, and every row is
  re-sealed.** The AEAD associated data is `opendeploy-secret:s<secret_id>`
  instead of `(secret_id, value_version)`. At the first start where the
  secrets store unlocks (machine key present, or a recovery unlock), every
  `secret_event_log` row is opened under its old binding and re-sealed in
  place. `Secret` field 3 (`seal_id`, never released) is reserved.
- **System secrets live in the secret event log, in space 0.** The cluster
  and workload CA material, the web UI CA and bundle, and the ACME account
  key move from the `system_secrets` table into `secret_event_log` as
  secrets in the root of space 0, authored by the system, during the same
  re-seal pass; the table is dropped once every row has moved. Space 0
  values are unreachable through every API and never appear in the state
  stream, whatever the caller's grants.
- **Space 0 is closed to users.** Secret, config, and asset requests in
  space 0 are refused before any grant is consulted, and a deployment can no
  longer be created in space 0 or moved into it (`spaceId must be between 1
  and 4095`). Cluster-level checks that name space 0 (nodes, users,
  settings, access) and the self and netproxy deployments are unaffected.
- **Asset content lives on disk and in S3, never in the database.** The
  `inline_blob` column of `asset_store` and the `asset_migrations` table are
  gone. Every asset version now carries a `storage_key` on
  `asset_event_log` and on `Asset`, naming its content file in the
  large-asset root and its S3 object. Uploads of every size stage to
  `large-assets/<key>` and, with backup enabled, are copied to S3 before the
  version is committed. Design and status: `docs/engineering/assets.md`.
- **`user_event_log.last_login_at` is dropped** from the table and from the `User`
  proto (field 4 reserved). The users page no longer shows a last-login
  column; the newest personal session for the user carries the same time.
- **Bearer tokens are opaque and every existing token is invalid.** Tokens
  are now `u_<id>.<secret>` (browser and bootstrap sessions) and
  `a_<id>.<secret>` (agent sessions), verified against the session row's
  hash instead of an RSA signature. The `public_keys` table is dropped.
  Every operator is signed out on upgrade and every agent token stops
  working; agents must request a new session.
- **`personal_sessions` becomes `user_session_event_log`** with a `kind` column,
  and the `/v1/personal-sessions/*` endpoints become `/v1/user-sessions/*`.
  The old table is dropped rather than migrated: its rows held hashes of the
  old token format and could never match again. The per-request activity
  touch and `last_active_at` are gone with it; the sessions page loses its
  "Last active" column.
- **Sessions are part of the state stream.** Every create, approval, and
  revocation is a commit that stamps a `global_seq` and publishes the session in
  `CoreUpdate.agent_sessions` or `CoreUpdate.user_sessions` to its owner. The
  `StateStreamMsg.agent_sessions` sidecar (field 8) is reserved, `UserSession`
  gains `user_id` and loses `current`, and `LoginResponse` gains
  `session_id`. Session rows are never deleted.
- **Scopes are gone.** A user session has a `UserSessionKind`, `FULL` or
  `BOOTSTRAP` (master-password exchange, passkey registration only), and an
  agent session always acts as a full, delegated session. Route policies
  name the kinds they accept. `agent_session_event_log.scopes` is dropped; `scopes` is
  reserved on `AgentSession` (5) and `LoginResponse` (3), which gains `kind`.
- **Passkey logins no longer grow the user record.** Each login used to append
  a copy of the credential to the user's blob. The credential is now replaced
  by id, and existing duplicates are collapsed onto the newest copy at startup.
- **Nine tables become append-only event tables.** `spaces`, `users`,
  `value_directories`, `asset_directories`, `system_config_revisions`,
  `nix_store_resets`, `agent_sessions`, `user_sessions`, and
  `secret_keyslots` become `space_event_log`, `user_event_log`,
  `value_directory_event_log`, `asset_directory_event_log`,
  `system_config_event_log`, `nix_store_reset_event_log`,
  `agent_session_event_log`, `user_session_event_log`, and
  `secret_keyslot_event_log`, named like every other event log. Each keeps
  one row per event (`global_seq`, `event_time`, `author`, entity id,
  `event_type`, full document) and is never updated or deleted in place. Live state is the
  newest row per entity that is not a delete, and `primary.db` grows by one
  row per space rename, directory move, settings save, passkey login, or
  session transition. A deleted space's id is no longer reused. Nothing
  changes on the wire.
- **Keyslots are keyed by kind and node.** `secret_keyslot_event_log.slot` (`machine`
  or `recovery`) becomes `kind` (`SecretKeyslotKind`: 1 machine, 2 recovery)
  plus `node_id`, so a future replica can hold its own wrapped copy of the
  master key. The wrapped bytes and their bindings are unchanged. Evicting a
  node deletes its machine slot. Every keyslot write is a commit with a seq
  and the acting user as author.
- **Settings saves are no longer blocked by an asset transition.** The
  reconciler converges placement from the `asset_store` flags; `BackupStatus`
  field 8 (`asset_migration_running`) is reserved and `asset_pending > 0` is
  the "syncing" signal in the cluster page.

### Before upgrading

- **Back up `primary.db`.** The secret re-seal, the `storage_key` backfill,
  and the dropped `inline_blob` column are one-way.
- **Make sure the secrets store will unlock.** The re-seal needs the master
  key. A primary whose `machine.key` is missing starts locked as before, and
  the re-seal then runs at the recovery unlock; until then internal reads
  (cluster TLS, ACME) fail as they would on any locked store.
- **Free disk under `large-assets/` on the primary** for the sum of the
  inline asset blobs currently in `primary.db`:

  ```sql
  SELECT COUNT(*), COALESCE(SUM(LENGTH(inline_blob)), 0)
    FROM asset_store WHERE local_status = 0 AND remote_status = 0;
  ```

### What the primary does on first start

- Adds `storage_key` to `asset_event_log` and backfills it from the
  `asset_store` row with the same sha256; drops `asset_migrations`,
  `personal_sessions`, and `public_keys`, and sweeps the long-dead
  `system_config`, `secret_config_directories`, `config_displays`, `events`,
  `local_runtime_inputs`, and `local_scheduled_instance_cache` tables that
  older installs still carry. Nothing reads any of them.
- After the secrets store unlocks, re-seals every `secret_event_log` row
  under the `secret_id` binding, appends each `system_secrets` row to the
  event log in space 0, and drops `system_secrets` (and the `seal_id` column
  on databases from unreleased v0.0.614 builds). One log line with the row
  counts. A row that opens under no known binding is logged and left, the
  artifacts stay, and the pass retries at the next unlock.
- Rebuilds `spaces`, `users`, `value_directories`, `asset_directories`,
  `system_config_revisions`, `nix_store_resets`, `agent_sessions`,
  `user_sessions`, and `secret_keyslots` as the `*_event_log` tables: the
  new table is created beside the old one, every row is copied in as a
  create event with `global_seq = 0` (the config revision keeps its id,
  which is its version number; the `user_event_log.last_login_at` and
  `agent_session_event_log.scopes` columns are not carried over; the machine keyslot
  takes the primary's node id), and the old table is dropped. One log line per table. This runs once
  and takes well under a second at the row counts these tables have.
- Rewrites each user whose passkey list holds duplicate credential ids,
  keeping the newest entry per id, and logs one line per user changed.
- Writes each inline blob to `large-assets/<key>`, flags the row as locally
  present, and then drops the `inline_blob` column. A blob whose length does
  not match `size_bytes` is skipped with an error log and left in the column,
  which then stays until the row is fixed by hand.
- With backup enabled, the reconciler uploads every locally present row that
  has no S3 copy, so `asset_pending` is non-zero until the former inline
  content is in the bucket.

### During the rollout

- Every browser lands on the login page and every agent token is refused
  with 401 from the first request after the primary restarts.
- Agent sessions that were approved but not yet collected still hand out a
  token, in the new format, on pickup.
- Secondaries are unaffected: the cluster protocol is unchanged and the
  asset content endpoints serve the same bytes from the new location.

## v0.0.613 (2026-09-25, commit 837917a)

### What changed

- **Value references are `(id, version)` pairs.** Every secret, config, and
  asset reference in deployment specs, cluster settings, ACME bindings,
  worker caches, and the cluster wire moves from an event log row id to
  `ValueRef{id, version}`: the stable entity id plus its value version.
  Design and status: `docs/future-work/value-reference-pairs-implementation-plan.md`.
- **Value event logs drop the space facet.** `space_version` and
  `space_changed` are removed from `secret_event_log`, `config_event_log`,
  and `asset_event_log`, and from the `SecretEvent`, `ConfigEvent`, and
  `AssetEvent` protos.
- **Cluster protocol 11 → 12.** An upgraded secondary refuses an old
  primary, and the other way round.

### Before upgrading

- **Back up `primary.db`.** Every change below is one-way: the dropped columns
  and the rewritten blobs cannot be read by v0.0.612. Rolling back means
  restoring the pre-upgrade database, not reinstalling the old binary.
- **Check for duplicate value versions.** The new partial unique indexes on
  `(entity id, value_version) WHERE value_changed != 0` are built at startup,
  and a duplicate stops the primary. Run this read-only query against the
  primary database; it must return no rows:

  ```sql
  SELECT 'secret', secret_id, value_version, COUNT(*) FROM secret_event_log
   WHERE value_changed != 0 GROUP BY secret_id, value_version HAVING COUNT(*) > 1
  UNION ALL
  SELECT 'config', config_id, value_version, COUNT(*) FROM config_event_log
   WHERE value_changed != 0 GROUP BY config_id, value_version HAVING COUNT(*) > 1
  UNION ALL
  SELECT 'asset', asset_id, value_version, COUNT(*) FROM asset_event_log
   WHERE value_changed != 0 GROUP BY asset_id, value_version HAVING COUNT(*) > 1;
  ```

- **Dangling references in current versions stop startup.** The migration
  also stops the primary if a deployment's latest version, or a version a
  live instance runs, references a value row that does not exist, and for
  any dangling asset mount, ingress certificate, or settings reference.
  Dry-running the migration against a copy of `primary.db` finds these
  before the real upgrade.
- **Free disk on each secondary** for one extra copy of the assets its
  deployments mount (see the asset cache notes below).

### What the primary does on first start

1. Drops the space facet columns from the three value event logs
   (`migrations.sql`).
2. Builds the three unique indexes on `(entity id, value_version)`.
3. Rewrites every `deployment_event_log.value` and
   `system_config_event_log.config_blob` in one transaction
   (`pq/migrate_value_refs.go`), replacing each row id with its pair. Rows
   without references are left byte-for-byte unchanged, and later starts
   rewrite nothing.
4. In a deployment version that is neither the latest nor run by a live
   instance, a dangling env var reference becomes the literal value
   `"<unknown ref>"`, with a `Store` warning in the primary's system log
   naming the row, version, field, and old row id. Redeploying such a
   version passes validation and starts with that placeholder string.

### What each secondary does on first start

- **Runtime input cache is rebuilt.** The encrypted `local_runtime_inputs`
  table is dropped and recreated with a version column. Secrets and configs
  are fetched again from the primary on the next prepare. Until then the node
  cannot cold-start workloads that use secrets or configs without reaching
  the primary.
- **Asset cache files are renamed.** Cached assets are now named
  `<asset_id>@<version>` (plus `_x` for executable mounts). Files from both
  earlier layouts (`<row id>`, and `<old asset id>_<version>` from before
  2026-07-06) are no longer used and the retention sweep deletes them.
  Assets still in use are downloaded again under the new name on the next
  prepare. Running containers are not affected: their bind mounts hold the
  file data until the container is replaced, and only then is that space
  freed.

### During the rollout

The group upgrade walks secondaries first and the primary last. Between a
secondary's upgrade and the upgraded primary's first snapshot reaching it:

- the secondary cannot connect (protocol 12 against 11) and retries with
  backoff;
- its cached instance specs still have the old shape and decode with empty
  references;
- running containers keep running, and the operator retries the input fetch
  in the background;
- a container that crashes in this window cannot restart (its env var and
  asset references do not resolve) and retries with backoff until the
  snapshot arrives.

Upgrade the primary promptly after the secondaries to keep this window
short.

### Cluster notes

**flippingcopilot** (primary coflip-staging 54.39.100.199, secondary
coflip-prod 54.39.100.89). A dry run on copies of both databases taken
2026-09-25 10:00 UTC:

- no duplicate value versions;
- 745 of 1,461 blobs rewritten; all 5,178 resolvable references map to
  exactly their original rows; cluster settings resolve (GitHub token →
  secret 50@2, backup and large-assets S3 keys → secret 52@1); config
  revisions and the snapshot load; a second start rewrites nothing;
- 108 dangling env var references (secret rows 5 and 18, asset rows 5, 6,
  and 19) in 70 historical versions of deployments 3, 4, 5, 6, 23, 24, and
  27, written 2026-06-18 to 2026-07-15. None is in a latest or live version,
  so they become `"<unknown ref>"` and the primary starts;
- coflip-prod drops 21 cached runtime inputs; 6 of its 8 cached instances
  hold old-shape references until the primary's snapshot arrives;
- coflip-prod's asset cache: about 2.9 GB of orphaned pre-July files and about
  2 GB of row-id files are deleted by the sweep; the ones still in use
  (the largest is 1.1 GB) are downloaded again.

**allevia**: not dry-run yet. Run the duplicate query and a dry run of the
migration against a copy of its `primary.db` before upgrading.
