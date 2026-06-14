# Assets

Assets are versioned user-managed file blobs intended for config files that can later be mounted read-only into container deployments.

## Current implementation

- Storage table: `assets` stores immutable versions with an auto-incremented numeric `id` and unique `(key, version)`.
- Rows are immutable. Saving an existing key appends the next integer version.
- Latest metadata is listed via `/v1/assets/list`; the blob is fetched explicitly via `/v1/assets/get`.
- Inline DB blobs are capped at 10 MiB for now.
- `format` is a UI/editor hint such as `text`, `nginx`, `yaml`, or `json`.
- `location` is currently empty and reserved for future disk/S3-backed assets.

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
- On the primary, the asset provider loads the blob from the primary DB.
- On a secondary, the asset provider downloads the blob on demand from the primary over the mTLS cluster endpoint `/v1/cluster/asset`.
- Materialize/cache assets on each target machine at `/var/lib/opendeploy-assets/<asset-id>_<version>`.
- Mount materialized files read-only into the container.
- Reject paths that are empty, relative, directories, or dangerous container destinations.
- Fail deployment preparation if an asset key/version no longer exists.
- Keep existing `runner.container.mounts` for operator-managed host paths; use `assetMounts` only for OpenDeploy-managed config files.
- In the UI, use the compact Assets section under environment variables to select key/path or create a new asset in the side pane.

Open questions before implementation:

- Whether large future `location` assets should be streamed to workers through the cluster transport or fetched directly by workers from object storage.
- Whether an asset mount can set file mode/owner. First pass uses read-only bind mounts with default materialized file permissions.
