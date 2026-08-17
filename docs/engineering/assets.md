# Assets

Assets are versioned user-managed file blobs intended for config files that can later be mounted read-only into container deployments.

## Current implementation

- Storage tables: `assets` holds the stable identity — `(space_id, asset_directory_id, key)` plus `created_at`/`created_by`; `asset_versions` holds the immutable version rows with unique `(asset_id, version)`, each carrying `size_bytes` and the `sha256` of its content; `asset_store` holds the content itself, keyed by a uuidv7 `id` with a unique `sha256`, an `inline_blob` for small content, and `local_status`/`remote_status` flags saying which storage side holds a durable copy; `asset_directories` holds the per-space folder tree (`parent_id`, `0` = the implicit root). `created_by` is the acting user id, `0` for migrated or system rows.
- **Content is content-addressed.** A version row links its bytes through `sha256 = asset_store.sha256`, so identical content — across versions, assets, and spaces — shares one store row and one copy in storage. Deleting an asset reclaims a store row (and its local file) only when no other version links its sha; S3 objects are never eagerly deleted (see retention). The store row's uuid names the physical copies: `LargeAssetsDir/<id>` locally, `<s3-path>/<id>` in S3.
- **Two id spaces.** `assets.id` is the stable asset id: it survives renames, moves, and new versions, and is what the write API targets. `asset_versions.id` is the version row id: it is what deployment configs pin (`assetVersionId`) and what workers fetch and cache by. The shape migration preserved every pre-split row id into `asset_versions.id` and started the `assets` sequence above them, so the two spaces do not overlap on migrated installs and an accidental cross-join resolves to nothing.
- Each space is an independent file system. Sibling keys must be unique per `(space_id, asset_directory_id)` across **both** assets and directories; that spans two tables, so it is enforced by the storage layer's mutex-guarded create/rename ops, not by a SQL constraint. Keys must be valid file names (no `/`, `\`, NUL, `.`, `..`, ≤255 chars).
- Version rows are immutable. Appending targets the stable asset id and writes the next integer version. Renaming updates only `assets.key` — version rows, ids, and content are untouched, and pinned deployment references keep working.
- **Uploads land content before identity.** A large upload inserts a staging `asset_store` row (empty sha, both statuses 0), streams to `LargeAssetsDir/<id>` while hashing, and only after the content is durable (local fsync or S3 put) marks the row complete and inserts the `assets`/`asset_versions` rows — which is pure SQLite, so an identity can never point at content that failed to land. If the computed sha already exists, the staged copy is discarded and the version links the existing row. Inline uploads hash in memory and insert store row plus identity directly. A crash leaves at worst an unreferenced store row: nothing references it, and the reconciler's sweep reclaims unreferenced rows — immediately at startup, after a 24h grace period at runtime (the grace leaves room for future upload-then-confirm flows).
- Assets are listed via `/v1/assets/list`, one entry per asset: identity fields (`id`, `key`, `space_id`, directory, the asset's own `created_at`/`created_by`) plus `version_refs`, the full version index carrying each version's row id, number, creation info, size, sha256, and location. `version_refs` is ordered **newest first**, so `version_refs[0]` is the latest version — no latest-version fields are duplicated at the root. The row ids are what deployment specs pin, so this index is what usage matching and version-pinned references join against. Version rows only exist once their content is durable, so every asset with a version is listable. The blob is fetched via `/v1/assets/get` with `{asset_id, version}` (version 0 = latest).
- Write endpoints: `/v1/assets/create` (`{key, space_id, blob, asset_directory_id}`) creates an asset in a space's folder (`asset_directory_id` 0 = the root; a directory in another space is a 404); `/v1/assets/set` (`{asset_id, blob}`) appends the next version; both carry the blob in the request body and are subject to the mux-wide `MaxRequestBodySize` (20 MB, base64-inflated to roughly 15 MB of file under JSON). `/v1/assets/upload` is a `go_custom` route that takes the raw bytes as the body and streams them, so no decode limit applies; its only ceiling is `math.MaxInt32`. Upload requires a `Content-Length` and rejects chunked bodies.
- `/v1/assets/upload` distinguishes its two target parameters. `?asset_id=` targets an exact asset and appends its next version. `?name=` (optionally with `&space_id=` and `&directory_id=`) asks for a new asset and is suffixed (`nginx.conf` → `nginx.conf1`) when the name is taken among that folder's assets; this is what the web UI file picker sends. Supplying `?name=` for a key that already exists therefore creates a separate asset rather than a new version, and returns `200`.
- Folder tree API: `/v1/asset-directories/{list,create,move,rename,delete}` manages `asset_directories`; delete is rejected unless the folder is empty, and moving a folder inside its own subtree is a cycle error. `/v1/assets/move` (`{asset_id, asset_directory_id, space_id}`) relocates an asset — version rows and ids are untouched, so pinned deployment references survive. The state stream carries `asset_directories_snapshot`/`asset_directory_update` (deletes arrive with `deleted` set) and `GET /v1/global-state` includes `asset_directories`.
- **Cross-space moves are supported for assets, not for directories.** Both move requests carry a `space_id` (`0` keeps the row where it is). For an asset, naming another space moves it there under a reference-locality rule: the handler collects every deployment pinning one of the asset's version ids (mounts and asset env refs, via the same `runtimeinputs` collectors the engine fetches by) and refuses with `move_references_outside_space` unless they all live in the destination space. The check-and-move runs under `ConfigService.LockReferences()` so no new pin can appear in between; `MoveAssetSpace` then rewrites `space_id` + `asset_directory_id` in one locked store op (destination directory must belong to the destination space, sibling-key uniqueness holds there). Version rows and ids are untouched, so surviving pins keep resolving; a delete-tombstone precedes the update on the state stream so clients that cannot see the destination drop the row. Directory space moves are still refused with `asset_space_move_unsupported` (`MoveDirectorySpace`): a subtree move needs per-item reference checks. The gates run before any reparenting, so a refused cross-space move never lands the row at its own space's root, and the explorer's drag-and-drop and Move dialog surface the refusal.
- **Deleting a referenced asset is refused** (`reference_in_use`): `/v1/assets/delete` runs the same deployment reverse lookup under `LockReferences()`, matching the protection secrets and configs have always had.
- There is no stored format hint. Editors infer syntax from the key's file extension.
- Assets up to 10 MiB are stored inline in the primary DB.
- Assets larger than 10 MiB use primary-local storage while Backup is disabled and S3 while Backup is enabled; the DB row keeps metadata and the active location.
- The UI does not load large asset content for preview/edit. It shows a "too large to show" message while deployments and worker mounts still fetch the blob transparently.
- `frontend/src/components/assetEditor.js` is the shared asset content surface. It supports inline and overlay presentation, create/edit/read modes, and loading an exact historical version. Editing historical content still appends after the latest known version; asset rows are never mutated.
- UTF-8 inline assets use the shared CodeMirror editor. Inline assets containing invalid UTF-8 are displayed read-only in a plain textarea so a text edit cannot replace their original bytes.
- The wire `location` is derived, not stored: empty for inline versions, `local://<store-id>` when the local side is durable, `s3://<store-id>` when the S3 side is. The wire `sha256` is the content hash.
- Asset rename rejects a destination key already used by a sibling asset or directory and preserves the complete version history. Existing deployments remain valid because they pin immutable version row ids; their stored display key is refreshed only when the deployment config is updated.
- `asset_migrations` records each complete local-to-S3 or S3-to-local transition with its old and new `system_config_revisions` row IDs, durable status, timestamps, and latest error. The per-row `local_status`/`remote_status` flags are the progress markers; there is no migration-item table.
- Primary/secondary startup creates the fixed local large-asset and materialized-asset cache roots up front. Asset operations create files inside those roots but do not recreate missing roots.

## Shape-migration history

The pre-directories schema stored one `assets` row per version, grouped only by the `key` string. A one-time Go startup migration transformed it into the identity + versions split, preserving every old row id verbatim into `asset_versions.id` (which is why the two id spaces are disjoint on migrated installs — new `assets` ids were seeded above the highest preserved version id) and bumping the cluster protocol (6 → 7) for the `asset_version_id` fetch naming. The migration code was removed after every active cluster had been rolled forward; upgrading a pre-split database now requires stepping through a release that still carried it.

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

- Enabling Backup copies locally stored file-backed store rows to S3: each row's destination becomes durable, `remote_status` is set, the local file is removed, and `local_status` is cleared.
- Disabling Backup downloads S3-backed rows to local primary storage the same way in reverse: `local_status` is set only after the file is durable, then `remote_status` is cleared (the S3 object itself is retained).
- A transfer briefly has both statuses set. The destination becomes durable before its status is set, and the source status clears only after that, so a crash at any point leaves a resumable state. This overlap provides crash safety; it is not a second active storage mode.
- The worker uses the new config version for an S3 destination and the old config version for an S3 source.
- Startup resumes a `pending` or `running` migration by inspecting the status flags. Transfer errors are stored on the migration row and retried indefinitely with exponential backoff.
- All subsequent settings saves are rejected until the migration is finished. Internal config writes such as master-password rotation remain available.
- When no store rows remain in the source mode, the worker marks the migration `finished`. Completed rows remain as migration history.

Large-asset transition status is included in `BackupStatus`, including the target mode, pending count, running state, and transition error. Database replication does not start or report in sync until the durable migration row is finished. When Backup is disabled, Litestream is stopped before any asset location changes to local storage.

### Interrupted uploads

New large-asset uploads are synchronous but cross SQLite and filesystem or S3 durability boundaries. Because identity rows are only inserted after content is durable, there is nothing to "finish" after a crash: the client saw an error and retries, and what remains is at most a staging `asset_store` row (empty sha) with a partial file, or a completed-but-unreferenced row. The reconciler's unreferenced-row sweep reclaims both — everything at startup, and rows older than the 24h grace period during runtime. A disk-scan cleanup additionally removes files in the large-asset root that no store row names. This upload state is independent of `asset_migrations`.

### Retention and restore

S3 objects are retained when their store row is deleted, its last referencing version is deleted, or its content transitions back to local active storage. OpenDeploy does not eagerly delete those objects because a retained database restore point can still reference them. The S3 bucket lifecycle policy controls when retained objects expire.

A completed database restore point created in Backup mode refers to the retained S3 objects recorded by that database state. Restoring the database does not copy or restore primary-local large-asset files. Recovery from such a restore point therefore requires its referenced S3 objects to remain available; a lifecycle policy can make an older database restore point incomplete by expiring those objects.

### Content-store migration history

The 2026-08 content split (v0.0.435) moved blobs and locations off `asset_versions` into `asset_store`. The SQL migration seeded one store row per version row, preserving the version row id as the store row's `id` — local files were already named `<version-row-id>` and S3 keys `<s3-path>/<version-row-id>`, so the id-derived naming resolved every existing copy with zero renames or S3 copies, and older database restore points keep referencing valid object keys. New rows use uuidv7 ids, which cannot collide with the numeric legacy ids. Migrated rows carried a `legacy:<id>` placeholder sha until a background converter hashed their content and repointed the links, merging duplicate content into single rows. The migration and converter were removed after every active cluster had converted; upgrading an older database now requires stepping through v0.0.435. Location naming stays derivable because changing the effective S3 configuration is rejected while anything is S3-backed or staging, so the current settings always describe where every S3 copy lives.

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
