# Assets

Assets are versioned user-managed file blobs intended for config files that can later be mounted read-only into container deployments.

## Current implementation

- Storage table: `assets` stores immutable versions with an auto-incremented numeric `id` and unique `(key, version)`.
- Rows are immutable. Saving an existing key appends the next integer version.
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
        version: 0          # 0 or omitted means latest at deployment config time
        path: /etc/nginx/nginx.conf
      - asset: site.conf
        version: 3
        path: /etc/nginx/conf.d/site.conf
```

Current semantics:

- Resolve `version: 0` when the deployment config is created or updated, then store the resolved key, numeric asset id, version, format, and mount path in config history. Asset content is not embedded in deployment configs.
- During preparation, call `preparer.EnsureAssetsReady` before the deployment reaches READY.
- On the primary, the asset provider streams inline blobs from the primary DB and large blobs from S3.
- On a secondary, the asset provider streams the blob on demand from the primary over the mTLS cluster endpoint `/v1/cluster/asset?asset_id=<id>&version=<version>`.
- Materialize/cache assets on each target machine at `/var/lib/opendeploy-assets/<asset-id>_<version>`.
- Mount materialized files read-only into the container.
- Reject paths that are empty, relative, directories, or dangerous container destinations.
- Fail deployment preparation if an asset key/version no longer exists.
- Keep existing `runner.container.mounts` for raw host bind mounts; use `assetMounts` only for OpenDeploy-managed config files.
- In the UI, use the compact Assets section under environment variables to select key/path or create a new asset in the side pane.

Open questions before implementation:

- Whether an asset mount can set file mode/owner. First pass uses read-only bind mounts with default materialized file permissions.
