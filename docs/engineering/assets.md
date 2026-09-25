# Assets

Assets are versioned user-managed file blobs intended for config files that can later be mounted read-only into container deployments.

## Current implementation

- Storage tables: `asset_event_log` is one append-only log per asset, one row per event — identity facets (`key`, `asset_directory_id`, `space_id`) are denormalised onto every row, so the highest-version row is the complete current state with `event_type` as the deletion truth (deletion is a terminal delete event; historical rows and content survive but every current-state read excludes the asset, freeing the key). `value_version` bumps only on content writes, with `value_changed` flagging the events that bumped it; the content payload (`size_bytes`, `sha256`) is carried forward onto every row, and a `value_changed` row is an immutable content version whose row id is the pinnable version id. `asset_store` holds the content itself, keyed by a uuidv7 `id` with a unique `sha256`, an `inline_blob` for small content, and `local_status`/`remote_status` flags saying which storage side holds a durable copy; `asset_directories` holds the per-space folder tree (`parent_id`, `0` = the implicit root). Event rows carry `author`, the acting user id, `0` for migrated or system rows.
- **Content is content-addressed.** A version row links its bytes through `sha256 = asset_store.sha256`, so identical content — across versions, assets, and spaces — shares one store row and one copy in storage. Deleting an asset is soft, so its version rows keep their content referenced and reclaimable by nothing; a store row (and its local file) is reclaimed only when a failed write leaves it with no referencing version. S3 objects are never eagerly deleted (see retention). The store row's uuid names the physical copies: `LargeAssetsDir/<id>` locally, `<s3-path>/<id>` in S3.
- **Two id spaces.** `asset_id` is the stable asset id: it survives renames, moves, and new versions, and is what the write API targets. References never use the content-event row id: deployment configs pin `ValueRef{id, version}` pairs of `asset_id` and `value_version` (`assetRef` on env vars, `asset` on mounts), and workers fetch and cache by that pair. A partial unique index on `(asset_id, value_version) WHERE value_changed != 0` keeps the pair unique. The row id stays the join key inside `pq` and the UI content download. Every shape migration preserved the pinned version-row ids verbatim (pre-split rows into `asset_versions.id`, then those into `asset_event_log.id`) and kept the asset-id sequence above them, so the two spaces do not overlap on migrated installs and an accidental cross-join resolves to nothing.
- Each space is an independent file system. Sibling keys must be unique per `(space_id, asset_directory_id)` across **both** assets and directories; that spans two tables, so it is enforced by the storage layer's mutex-guarded create/rename ops, not by a SQL constraint. Keys must be valid file names (no `/`, `\`, NUL, `.`, `..`, ≤255 chars).
- Content version rows are immutable. Appending targets the stable asset id and writes the next integer version. Renaming appends an event that changes only the key — content version rows, ids, and content are untouched, and pinned deployment references keep working.
- **Uploads land content before identity.** A large upload inserts a staging `asset_store` row (empty sha, both statuses 0), streams to `LargeAssetsDir/<id>` while hashing, and only after the content is durable (local fsync or S3 put) marks the row complete and appends the `asset_event_log` row — which is pure SQLite, so an identity can never point at content that failed to land. If the computed sha already exists, the staged copy is discarded and the version links the existing row. Inline uploads hash in memory and insert store row plus identity directly. A crash leaves at worst an unreferenced store row: nothing references it, and the reconciler's sweep reclaims unreferenced rows — immediately at startup, after a 24h grace period at runtime (the grace leaves room for future upload-then-confirm flows).
- `/v1/assets/list` returns the latest live `AssetEvent` per asset. The envelope contains `asset_id`, `version`, `event_id`, `seq`, and facet versions; `value` holds `fs`, `space_id`, `sha256`, and `size_bytes`. `Snapshot.asset_events` contains every event of each live asset, oldest first. Content pins are `event_id`s of events that changed `value_version`; a rename or move preserves that facet. Upload returns the exact appended event. Content bytes are served by `GET /v1/assets/content?content_version_id=N`. Current asset metadata queries never join or load blobs from `asset_store`.
- `/v1/assets/upload` is the single write endpoint: a `go_custom` route that takes the raw bytes as the body and streams them, so no decode limit applies; its only ceiling is `math.MaxInt32`. Upload requires a `Content-Length` and rejects chunked bodies.
- `/v1/assets/upload` distinguishes its two target parameters. `?asset_id=` targets an exact asset and appends its next version. `?key=` (optionally with `&space_id=` and `&directory_id=`; `directory_id` 0 = the root, a directory in another space is a 404) creates a new asset and refuses a taken key unless `&unique_key=1` is passed, which suffixes it (`nginx.conf` → `nginx.conf1`) within that folder's namespace; this is what the web UI file picker sends. With `unique_key`, a taken key therefore creates a separate asset rather than a new version, and returns `200`.
- Folder tree API: `/v1/asset-directories/{list,create,move,rename,delete}` manages `asset_directories`; delete is rejected unless the folder is empty, and moving a folder inside its own subtree is a cycle error. `/v1/assets/move` (`{asset_id, asset_directory_id, space_id}`) relocates an asset — version rows and ids are untouched, so pinned deployment references survive. The state stream carries `asset_directories` in `Snapshot` and `Update` (updates mark deleted folders with `deleted`), and `GET /v1/global/snapshot` includes the same collection.
- **Cross-space moves are supported for assets, not for directories.** Both move requests carry a `space_id` (`0` keeps the row where it is). For an asset, naming another space moves it there under a reference-locality rule: the handler collects every deployment pinning one of the asset's version ids (mounts and asset env refs, via the same `runtimeinputs` collectors the engine fetches by); a move to the global space is always reference-safe, and any other destination is refused with `move_references_outside_space` unless every referencing deployment lives there. The check-and-move runs under `SystemConfig.LockReferences()` so no new pin can appear in between; `MoveAssetSpace` then appends one event that bumps the space facet (with the acting user as `author`) and carries the new `asset_directory_id` in one locked tx (destination directory must belong to the destination space, sibling-key uniqueness holds there); the log preserves the full assignment history while the API keeps showing only the current space. Version rows and ids are untouched, so surviving pins keep resolving; a delete-tombstone precedes the update on the state stream so clients that cannot see the destination drop the row. Directory space moves are still refused with `asset_space_move_unsupported` (`MoveDirectorySpace`): a subtree move needs per-item reference checks. The gates run before any reparenting, so a refused cross-space move never lands the row at its own space's root, and the explorer's drag-and-drop and Move dialog surface the refusal.
- **Deleting a referenced asset is refused** (`reference_in_use`): `/v1/assets/delete` runs the same deployment reverse lookup under `LockReferences()`, matching the protection secrets and configs have always had.
- There is no stored format hint. Editors infer syntax from the key's file extension.
- Assets up to 10 MiB are stored inline in the primary DB.
- Assets larger than 10 MiB use primary-local storage while Backup is disabled and S3 while Backup is enabled; with `large_assets.keep_local_copy` set they are kept in both while Backup is enabled. The store row's `local_status`/`remote_status` flags record which copies are durable.
- The UI does not load large asset content for preview/edit. It shows a "too large to show" message while deployments and worker mounts still fetch the blob transparently.
- `frontend/src/components/assetEditor.js` is the shared asset content surface. It supports inline and overlay presentation, create/edit/read modes, and loading an exact historical version. Editing historical content still appends after the latest known version; asset rows are never mutated.
- UTF-8 inline assets use the shared CodeMirror editor. Inline assets containing invalid UTF-8 are displayed read-only in a plain textarea so a text edit cannot replace their original bytes.
- Storage placement (inline, local file, S3) is invisible on the wire: the content endpoints stream from whichever side is durable, and `content_versions` carries only the `sha256` content hash and size. `OpenAsset` prefers the local copy; if a claimed local file cannot be opened it clears `local_status`, wakes the reconciler, and falls through to S3 when `remote_status` is set.
- Asset rename rejects a destination key already used by a sibling asset or directory and preserves the complete version history. Existing deployments remain valid because they pin immutable `(asset_id, version)` pairs; their stored display key is refreshed only when the deployment config is updated.
- `asset_migrations` records each storage-target change with its old and new `system_config_revisions` row IDs, durable status, timestamps, and latest error. The per-row `local_status`/`remote_status` flags are the progress markers; there is no migration-item table.
- Primary/secondary startup creates the fixed local large-asset and materialized-asset cache roots up front. Asset operations create files inside those roots but do not recreate missing roots.

## Shape-migration history

The pre-directories schema stored one `assets` row per version, grouped only by the `key` string. A one-time Go startup migration transformed it into the identity + versions split, preserving every old row id verbatim into `asset_versions.id` (which is why the two id spaces are disjoint on migrated installs — new `assets` ids were seeded above the highest preserved version id) and bumping the cluster protocol (6 → 7) for the `asset_version_id` fetch naming. The migration code was removed after every active cluster had been rolled forward; upgrading a pre-split database now requires stepping through a release that still carried it.

## Large-asset storage modes

The 10 MiB boundary is inclusive. Asset versions of 10 MiB or less remain inline in SQLite in every mode. Asset versions larger than 10 MiB are stored outside SQLite according to the overall Backup setting and `large_assets.keep_local_copy` (`systemconfig.LargeAssetStorageTarget`):

| Backup | Keep local copy | Storage target for versions larger than 10 MiB |
|--------|-----------------|-------------------------------------------------|
| Disabled | any | Local storage on the primary |
| Enabled | off (default) | S3 |
| Enabled | on | Both: S3 and local storage on the primary |

`keep_local_copy` is inert while Backup is disabled. The separate large-asset S3 option does not independently enable S3 storage. It only selects which S3 configuration large assets use while Backup is enabled.

In the both target every file-backed store row is a full replica on both sides, with no eviction: the primary's disk holds the whole large-asset set. Reads are served from the local file and fall back to S3 when the local copy is missing; a new upload lands the staged local file and the S3 object before the version row is written, so a successful upload is durable on both sides.

### S3 configuration

By default, large assets use the Backup S3 credentials, bucket, region, and endpoint. They use an independent large-asset S3 path rather than the database backup path.

An installation can opt into a separate large-asset S3 configuration. The separate configuration supplies its own credentials, bucket, path, region, and endpoint. This option is also the compatibility path for installations that already stored large assets in a separately configured S3 location.

Changing any effective large-asset S3 configuration is rejected while an asset version is S3-backed or an upload is pending. Disable Backup and wait for the transition to local storage before changing credentials, bucket, path, region, endpoint, or shared/separate selection, then re-enable Backup to migrate the files to the new S3 configuration. Disabling Backup and changing S3 settings in one save is rejected because the old configuration must remain available as the migration source.

While Backup is enabled, S3 holds every large asset after its transition completes, alone in the S3 target or alongside the local copy in the both target. If S3 is unavailable, a new upload larger than 10 MiB is rejected rather than accepted into local storage only. Uploads of 10 MiB or less continue to use inline SQLite storage.

### Reconciliation and target changes

A settings save that changes the storage target atomically appends the new application-config version and a pending `asset_migrations` row. The settings save returns after that durable intent is committed and wakes the reconciler. The reconciler (`Store.Reconcile`) always converges every file-backed store row to the target implied by the current settings; it does not depend on a migration row being present. It runs at startup, on every target change, whenever a read had to fall back from a missing local file, and hourly. Each pass:

- Verifies local claims against the filesystem: one readdir of the large-asset root is compared with every row. A row claiming `local_status` whose file is missing or has the wrong size loses the claim. A row without the claim whose file is present with the right size is adopted after its hash matches `sha256`, when the target uses local storage.
- Converges each row. Target both: download when only S3 holds the content, upload when only the local file does. Target S3: upload when S3 lacks the content, then clear `local_status` and remove the file. Target local: download when the local file is missing, then clear `remote_status` (the S3 object itself is retained).
- A row with neither copy has no source and is counted as unavailable, logged, and reported through `BackupStatus.asset_error`; it never counts as pending, so a lost file cannot block settings saves forever.
- Transfers run outside the store mutex: the row is read under the lock, the bytes move to S3 or to a temp file in the root, and the flag flips under the lock after re-checking the row. Downloads verify size and `sha256` before the temp file is renamed into place.
- Source clears precede deletes: `local_status` is cleared before the local file is removed, so a crash in between leaves an orphan file for the inactive-file cleanup rather than a claim with no file behind it.
- S3 identity cannot change while any row is S3-backed, so the current settings always describe where every S3 copy lives; the reconciler reads only the current config.
- Startup resumes a `pending` or `running` migration by inspecting the status flags. Transfer errors are stored on the migration row and retried indefinitely with exponential backoff, capped at one minute.
- All subsequent settings saves are rejected until the migration is finished. Internal config writes such as master-password rotation remain available. Steady-state work without a migration row, such as re-downloading after a read fallback, never blocks saves.
- When no row remains pending for the target, the worker marks the migration `finished`. Completed rows remain as migration history.

Large-asset status is included in `BackupStatus`: whether the target uses S3 (`asset_target_s3`), whether it also keeps local copies (`asset_keep_local`), the pending count, whether a migration is running, and the latest error. Database replication is otherwise decoupled from asset migration: Litestream starts, stops, and reports sync state purely from the Backup setting, regardless of where large assets currently live. Ensuring large assets are actually in S3 is the cluster admin's responsibility — a database backup taken while file-backed assets are still local-only (Backup freshly enabled with the migration pending or failing) references content that exists only on the primary's disk, so restoring that backup onto a replacement machine yields unresolvable large assets. The `BackupStatus` asset fields exist to make that window visible.

### Interrupted uploads

New large-asset uploads are synchronous but cross SQLite and filesystem or S3 durability boundaries. Because identity rows are only inserted after content is durable, there is nothing to "finish" after a crash: the client saw an error and retries, and what remains is at most a staging `asset_store` row (empty sha) with a partial file, or a completed-but-unreferenced row. The reconciler's unreferenced-row sweep reclaims both — everything at startup, and rows older than the 24h grace period during runtime. A disk-scan cleanup additionally removes files in the large-asset root that no store row names. This upload state is independent of `asset_migrations`.

### Retention and restore

S3 objects are retained when their store row is deleted, its last referencing version is deleted, or its content transitions back to local active storage. OpenDeploy does not eagerly delete those objects because a retained database restore point can still reference them. The S3 bucket lifecycle policy controls when retained objects expire.

A completed database restore point created in Backup mode refers to the retained S3 objects recorded by that database state. Restoring the database does not copy or restore primary-local large-asset files. Recovery from such a restore point therefore requires its referenced S3 objects to remain available; a lifecycle policy can make an older database restore point incomplete by expiring those objects.

A restored database in the both target claims a local copy for every row while the replacement machine's large-asset root is empty. The startup verify pass clears those claims and the converge pass re-downloads each row from S3; a read that arrives before then falls back to S3 through the same path. No installer step is involved, and a root that was preserved keeps its files after the hash check.

### Content-store migration history

The 2026-08 content split (v0.0.435) moved blobs and locations off `asset_versions` into `asset_store`. The SQL migration seeded one store row per version row, preserving the version row id as the store row's `id` — local files were already named `<version-row-id>` and S3 keys `<s3-path>/<version-row-id>`, so the id-derived naming resolved every existing copy with zero renames or S3 copies, and older database restore points keep referencing valid object keys. New rows use uuidv7 ids, which cannot collide with the numeric legacy ids. Migrated rows carried a `legacy:<id>` placeholder sha until a background converter hashed their content and repointed the links, merging duplicate content into single rows. The migration and converter were removed after every active cluster had converted; upgrading an older database now requires stepping through v0.0.435. Location naming stays derivable because changing the effective S3 configuration is rejected while anything is S3-backed or staging, so the current settings always describe where every S3 copy lives.

## Container mounts

Asset mounts are defined under `container1Spec.runtime`, separate from raw host mounts:

```yaml
container1Spec:
  runtime:
    assetMounts:
      - asset: {id: 4, version: 2}
        containerPath: /etc/nginx/nginx.conf
        permission: READ_ONLY
      - asset: {id: 9, version: 1}
        containerPath: /etc/nginx/conf.d/site.conf
        permission: READ_ONLY
      - asset: {id: 15, version: 3}
        containerPath: /docker-entrypoint-initdb.d/init.sh
        permission: READ_EXECUTE
```

Current semantics:

- Resolve the selected asset version row when the deployment config is created or updated, then store its `(asset_id, version)` pair, container path, and permission in config history. Asset content is not embedded in deployment configs.
- During preparation, the runtime-input service (`prepare/runtimeinputs`) runs `EnsureAssetsReady` before the deployment reaches READY.
- On the primary, the asset provider streams inline blobs from the primary DB and large blobs from their active local or S3 location without changing the mount contract.
- On a secondary, the asset provider streams the blob on demand from the primary over the mTLS cluster endpoint `/v1/cluster/asset?asset_id=<id>&version=<n>`.
- Materialize/cache assets on each target machine at `/var/lib/opendeploy-assets/<asset_id>@<version>` or `/var/lib/opendeploy-assets/<asset_id>@<version>_x` for executable mounts. A cache hit is trusted by file name alone, with no hash check, so the `@` separator is one no earlier layout used: files named by a bare version row id (2026-07 to v0.0.612) or `<old asset id>_<version>` (before 2026-07) never match a current ref and are removed by the retention sweep.
- The cache survives restarts and is reclaimed by the secondary's retention sweep, which deletes any cached file no instance assigned to that node still references. See "Local runtime input persistence" in [secrets.md](secrets.md) for the sweep's timing rules.
- Mount materialized files read-only into the container. Explicit asset mounts may use `READ_EXECUTE` to enable execute bits; implicit env asset mounts are always read-only/non-executable.
- Reject paths that are empty, relative, directories, or dangerous container destinations.
- Fail deployment preparation if a pinned asset version no longer exists.
- Keep `container1Spec.runtime.mounts` for raw host bind mounts; use `assetMounts` only for OpenDeploy-managed config files.
- In the UI, use the compact Assets section under environment variables to select key/path/mode or create a new asset in the side pane.
