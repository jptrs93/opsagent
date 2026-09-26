# Upgrade log

Notes for operators upgrading a cluster, newest release first. Each entry
covers what changes on disk and on the wire, what to check before upgrading,
and what to expect during and after the rollout.

## v0.0.614 (unreleased)

### What changed

- **Secret seals bind an opaque `seal_id`.** Each secret value write records
  a `seal_id` on `secret_event_log`, surfaced on `Secret`, and the AEAD
  associated data binds `(secret_id, seal_id)` instead of
  `(secret_id, value_version)`. Existing rows get `seal_id = 'v<version>'`
  and are not re-sealed.
- **Asset content lives on disk and in S3, never in the database.** The
  `inline_blob` column of `asset_store` and the `asset_migrations` table are
  gone. Every asset version now carries a `storage_key` on
  `asset_event_log` and on `Asset`, naming its content file in the
  large-asset root and its S3 object. Uploads of every size stage to
  `large-assets/<key>` and, with backup enabled, are copied to S3 before the
  version is committed. Design and status: `docs/engineering/assets.md`.
- **`users.last_login_at` is dropped** from the table and from the `User`
  proto (field 4 reserved). The users page no longer shows a last-login
  column; the newest personal session for the user carries the same time.
- **Passkey logins no longer grow the user record.** Each login used to append
  a copy of the credential to the user's blob. The credential is now replaced
  by id, and existing duplicates are collapsed onto the newest copy at startup.
- **Settings saves are no longer blocked by an asset transition.** The
  reconciler converges placement from the `asset_store` flags; `BackupStatus`
  field 8 (`asset_migration_running`) is reserved and `asset_pending > 0` is
  the "syncing" signal in the cluster page.

### Before upgrading

- **Back up `primary.db`.** The `seal_id` column, the `storage_key` backfill,
  and the dropped `inline_blob` column are one-way.
- **Free disk under `large-assets/` on the primary** for the sum of the
  inline asset blobs currently in `primary.db`:

  ```sql
  SELECT COUNT(*), COALESCE(SUM(LENGTH(inline_blob)), 0)
    FROM asset_store WHERE local_status = 0 AND remote_status = 0;
  ```

### What the primary does on first start

- Adds `seal_id` and backfills legacy rows; adds `storage_key` to
  `asset_event_log` and backfills it from the `asset_store` row with the same
  sha256; drops `asset_migrations` and `users.last_login_at`.
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
   `system_config_revisions.config_blob` in one transaction
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
