# Assets

Assets are versioned user-managed file blobs intended for config files that can later be mounted read-only into container deployments.

## Current implementation

- Storage tables: `assets` holds the stable identity — `(space_id, asset_directory_id, key)` plus `created_at`/`created_by`; `asset_versions` holds the immutable content rows with unique `(asset_id, version)`; `asset_directories` exists for the upcoming folder phase (every asset currently sits in the implicit root, `asset_directory_id = 0`). `created_by` is the acting user id, `0` for migrated or system rows.
- **Two id spaces.** `assets.id` is the stable asset id: it survives renames, moves, and new versions, and is what the write API targets. `asset_versions.id` is the version row id: it is what deployment configs pin (`assetVersionId`), what workers fetch and cache by, and what S3 object keys are derived from. The shape migration preserved every pre-split row id into `asset_versions.id` and started the `assets` sequence above them, so the two spaces do not overlap on migrated installs and an accidental cross-join resolves to nothing.
- Each space is an independent file system. Sibling keys must be unique per `(space_id, asset_directory_id)` across **both** assets and directories; that spans two tables, so it is enforced by the storage layer's mutex-guarded create/rename ops, not by a SQL constraint. Keys must be valid file names (no `/`, `\`, NUL, `.`, `..`, ≤255 chars).
- Version rows are immutable. Appending targets the stable asset id and writes the next integer version; `location` changes only after a durable storage transition. Renaming updates only `assets.key` — version rows, ids, content, and locations are untouched, and pinned deployment references keep working.
- Assets are listed via `/v1/assets/list`, one entry per asset: identity fields (`id`, `key`, `space_id`, directory, the asset's own `created_at`/`created_by`) plus `version_refs`, the full published-version index carrying each version's row id, number, creation info, size, and location. `version_refs` is ordered **newest first**, so `version_refs[0]` is the latest version — no latest-version fields are duplicated at the root. The row ids are what deployment specs pin, so this index is what usage matching and version-pinned references join against. An asset with no published version (e.g. a pending first upload) is never listed. The blob is fetched via `/v1/assets/get` with `{asset_id, version}` (version 0 = latest).
- Write endpoints: `/v1/assets/create` (`{key, space_id, blob}`) creates an asset in a space's root; `/v1/assets/set` (`{asset_id, blob}`) appends the next version; both carry the blob in the request body and are subject to the mux-wide `MaxRequestBodySize` (20 MB, base64-inflated to roughly 15 MB of file under JSON). `/v1/assets/upload` is a `go_custom` route that takes the raw bytes as the body and streams them, so no decode limit applies; its only ceiling is `math.MaxInt32`. Upload requires a `Content-Length` and rejects chunked bodies.
- `/v1/assets/upload` distinguishes its two target parameters. `?asset_id=` targets an exact asset and appends its next version. `?name=` (optionally with `&space_id=`) asks for a new asset and is suffixed (`nginx.conf` → `nginx.conf1`) when the name is taken in that space's root; this is what the web UI file picker sends. Supplying `?name=` for a key that already exists therefore creates a separate asset rather than a new version, and returns `200`.
- There is no stored format hint. Editors infer syntax from the key's file extension.
- Assets up to 10 MiB are stored inline in the primary DB.
- Assets larger than 10 MiB use primary-local storage while Backup is disabled and S3 while Backup is enabled; the DB row keeps metadata and the active location.
- The UI does not load large asset content for preview/edit. It shows a "too large to show" message while deployments and worker mounts still fetch the blob transparently.
- `frontend/src/components/assetEditor.js` is the shared asset content surface. It supports inline and overlay presentation, create/edit/read modes, and loading an exact historical version. Editing historical content still appends after the latest known version; asset rows are never mutated.
- UTF-8 inline assets use the shared CodeMirror editor. Inline assets containing invalid UTF-8 are displayed read-only in a plain textarea so a text edit cannot replace their original bytes.
- `location` is empty for inline versions, `local://<version-row-id>` for primary-local large versions, and `s3://...` for S3-backed large versions. A `pending://...` location is used only for an unpublished, interrupted large-asset upload; public asset queries exclude those rows until recovery finishes the upload.
- Asset rename rejects a destination key already used by a sibling asset or directory and preserves the complete version history. Existing deployments remain valid because they pin immutable version row ids; their stored display key is refreshed only when the deployment config is updated.
- `asset_migrations` records each complete local-to-S3 or S3-to-local transition with its old and new `system_config_revisions` row IDs, durable status, timestamps, and latest error. Individual asset locations are the per-asset progress markers; there is no migration-item table.
- Primary/secondary startup creates the fixed local large-asset and materialized-asset cache roots up front. Asset operations create files inside those roots but do not recreate missing roots.

## Shape migration (pre-directories → split tables)

The pre-directories schema stored one `assets` row per version, grouped only by the `key` string. A Go startup migration (`backend/storage/primarydb/assets_shape_migration.go`) transforms it in a single transaction, running on the primary before the schema files apply (the secondary schema is independently defined and has no asset tables — legacy copies on workers are dropped by its migrations) — `CREATE TABLE IF NOT EXISTS` would otherwise silently no-op against the old-shape table. Detection is by column shape (old `assets` has `blob`), which also makes the migration idempotent.

- Every old row id is preserved verbatim into `asset_versions.id`, so pinned deployment references, worker caches, and recorded S3 locations stay valid. `pending://` rows migrate too, so interrupted-upload recovery still finds its row.
- One `assets` row is minted per key. Old rows stored `space_id` per version and a targeted upload could override it, so versions of one key could disagree; the group takes the newest version's space, matching what the latest-version list always displayed.
- New `assets` ids start above the highest preserved version id, keeping the two id spaces disjoint.
- The cluster protocol version was bumped (6 → 7) with this change: the cluster asset fetch renamed its query param and headers to `asset_version_id` naming.

## Large-asset storage modes

The 10 MiB boundary is inclusive. Asset versions of 10 MiB or less remain inline in SQLite in every mode. Asset versions larger than 10 MiB are stored outside SQLite according to the overall Backup setting:

| Backup | Active storage for versions larger than 10 MiB |
|--------|-------------------------------------------------|
| Disabled | Local storage on the primary |
| Enabled | S3 |

The separate large-asset S3 option does not independently enable S3 storage. It only selects which S3 configuration large assets use while Backup is enabled.

### S3 configuration

By default, large assets use the Backup S3 credentials, bucket, region, and endpoint. They use an independent large-asset S3 path rather than the database backup path.

An installation can opt into a separate large-asset S3 configuration. The separate configuration supplies its own credentials, bucket, path, region, and endpoint. This option is also the compatibility path for installations that already stored large assets in a separately configured S3 location.

Changing any effective large-asset S3 configuration is rejected while an asset version is S3-backed or an upload is pending. Disable Backup and wait for the transition to local storage before changing credentials, bucket, path, region, endpoint, or shared/separate selection, then re-enable Backup to migrate the files to the new S3 configuration. Disabling Backup and changing S3 settings in one save is rejected because the old configuration must remain available as the migration source.

While Backup is enabled, S3 is the sole active location for each large asset after its transition completes. If S3 is unavailable, a new upload larger than 10 MiB is rejected rather than accepted into local storage. Uploads of 10 MiB or less continue to use inline SQLite storage.

### Mode transitions

Changing Backup atomically appends the new application-config version and a pending `asset_migrations` row. The settings save returns after that durable intent is committed; an event-driven worker then runs the transition asynchronously:

- Enabling Backup copies locally stored large versions to S3 and makes S3 their sole active location.
- Disabling Backup copies S3-backed large versions to local primary storage and makes local storage their sole active location.
- A transfer briefly keeps both source and destination copies. The destination becomes durable before the active location changes, and the source stops being an active copy only after that change. This overlap provides crash safety; it is not a second active storage mode.
- The worker uses the new config version for an S3 destination and the old config version for an S3 source. It updates each asset directly from `local://...` to `s3://...`, or vice versa; full migrations never use `pending://`.
- Startup resumes a `pending` or `running` migration by inspecting current asset locations. Transfer errors are stored on the migration row and retried indefinitely with exponential backoff.
- All subsequent settings saves are rejected until the migration is finished. Internal config writes such as master-password rotation remain available.
- When no asset locations remain in the source mode, the worker marks the migration `finished`. Completed rows remain as migration history.

Large-asset transition status is included in `BackupStatus`, including the target mode, pending count, running state, and transition error. Database replication does not start or report in sync until the durable migration row is finished. When Backup is disabled, Litestream is stopped before any asset location changes to local storage.

### Upload recovery

New large-asset uploads are synchronous but cross SQLite and filesystem or S3 durability boundaries. The upload is first staged and inserted with an internal `pending://<staging-name>` location. It is not published to lists, state snapshots, deployment selection, or asset readers. After the destination is durable, the same row changes to its final local or S3 location and is published.

At startup the asset worker handles upload recovery before full migration work. It can finish from the staging file, the final local file, or an already-uploaded S3 object. An unpublished row with no recoverable source or destination is removed. This upload state is independent of `asset_migrations`.

### Retention and restore

S3 objects are retained when their current asset row or version is deleted, becomes historical, or transitions back to local active storage. OpenDeploy does not eagerly delete those objects because a retained database restore point can still reference them. The S3 bucket lifecycle policy controls when retained objects expire.

A completed database restore point created in Backup mode refers to the retained S3 objects recorded by that database state. Restoring the database does not copy or restore primary-local large-asset files. Recovery from such a restore point therefore requires its referenced S3 objects to remain available; a lifecycle policy can make an older database restore point incomplete by expiring those objects.

### Compatibility

Existing asset rows with `s3://` locations remain valid and readable from their recorded bucket and key. Existing installations that enabled the former independent large-asset S3 setting remain opted into separate large-asset S3 configuration, so those rows are not reinterpreted as objects in the shared Backup location. A legacy S3 row is transition input: its active S3 object remains usable until a required destination copy is durable.

## Container mounts

Asset mounts are defined under `container1Spec.runtime`, separate from raw host mounts:

```yaml
container1Spec:
  runtime:
    assetMounts:
      - assetVersionId: 12
        containerPath: /etc/nginx/nginx.conf
        permission: READ_ONLY
      - assetVersionId: 33
        containerPath: /etc/nginx/conf.d/site.conf
        permission: READ_ONLY
      - assetVersionId: 47
        containerPath: /docker-entrypoint-initdb.d/init.sh
        permission: READ_EXECUTE
```

Current semantics:

- Resolve the selected asset version row when the deployment config is created or updated, then store its immutable version row id, container path, and permission in config history. Asset content is not embedded in deployment configs.
- During preparation, call `preparer.EnsureAssetsReady` before the deployment reaches READY.
- On the primary, the asset provider streams inline blobs from the primary DB and large blobs from their active local or S3 location without changing the mount contract.
- On a secondary, the asset provider streams the blob on demand from the primary over the mTLS cluster endpoint `/v1/cluster/asset?asset_version_id=<id>`.
- Materialize/cache assets on each target machine at `/var/lib/opendeploy-assets/<asset-version-id>` or `/var/lib/opendeploy-assets/<asset-version-id>_x` for executable mounts.
- The cache survives restarts and is reclaimed by the secondary's retention sweep, which deletes any cached file no instance assigned to that node still references. See "Local runtime input persistence" in [secrets.md](secrets.md) for the sweep's timing rules.
- Mount materialized files read-only into the container. Explicit asset mounts may use `READ_EXECUTE` to enable execute bits; implicit env asset mounts are always read-only/non-executable.
- Reject paths that are empty, relative, directories, or dangerous container destinations.
- Fail deployment preparation if a pinned asset version id no longer exists.
- Keep `container1Spec.runtime.mounts` for raw host bind mounts; use `assetMounts` only for OpenDeploy-managed config files.
- In the UI, use the compact Assets section under environment variables to select key/path/mode or create a new asset in the side pane.
