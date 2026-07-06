# Assets

Assets are versioned user-managed file blobs intended for config files that can later be mounted read-only into container deployments.

## Current implementation

- Storage table: `assets` stores immutable versions with an auto-incremented numeric `id` and unique `(key, version)`.
- Rows are immutable. Saving an existing key appends the next integer version.
- Deployment configs pin assets by immutable asset row ID; the key/version are display and selection metadata.
- Latest metadata is listed via `/v1/assets/list`; the blob is fetched explicitly via `/v1/assets/get`.
- Assets up to 10 MiB are stored inline in the primary DB.
- Assets larger than 10 MiB are stored in the configured large-asset S3 bucket under the asset row ID; the DB row keeps metadata and the S3 location.
- The UI does not load large asset content for preview/edit. It shows a "too large to show" message while deployments and worker mounts still fetch the blob transparently.
- `format` is a UI/editor hint such as `text`, `nginx`, `yaml`, or `json`.
- `location` is empty for inline assets and `s3://...` for S3-backed large assets.

## Phase 2: container mounts

Asset mounts are defined under the container runner, separate from raw host mounts:

```yaml
runner:
  container:
    assetMounts:
      - asset: nginx.conf
        assetId: 12
        path: /etc/nginx/nginx.conf
      - asset: site.conf
        assetId: 33
        path: /etc/nginx/conf.d/site.conf
      - asset: init.sh
        path: /docker-entrypoint-initdb.d/init.sh
        executable: true  # read-only mount with execute bits enabled
```

Current semantics:

- Resolve the selected asset row when the deployment config is created or updated, then store the immutable numeric asset id plus display key, format, and mount path in config history. Asset content is not embedded in deployment configs.
- During preparation, call `preparer.EnsureAssetsReady` before the deployment reaches READY.
- On the primary, the asset provider streams inline blobs from the primary DB and large blobs from S3.
- On a secondary, the asset provider streams the blob on demand from the primary over the mTLS cluster endpoint `/v1/cluster/asset?asset_id=<id>`.
- Materialize/cache assets on each target machine at `/var/lib/opendeploy-assets/<asset-id>` or `/var/lib/opendeploy-assets/<asset-id>_x` for executable mounts.
- Mount materialized files read-only into the container. Explicit asset mounts may set `executable: true` to enable execute bits; implicit env asset mounts are always read-only/non-executable.
- Reject paths that are empty, relative, directories, or dangerous container destinations.
- Fail deployment preparation if an asset ID no longer exists.
- Keep existing `runner.container.mounts` for raw host bind mounts; use `assetMounts` only for OpenDeploy-managed config files.
- In the UI, use the compact Assets section under environment variables to select key/path/mode or create a new asset in the side pane.
