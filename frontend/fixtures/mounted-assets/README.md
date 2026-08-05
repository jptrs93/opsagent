# Mounted assets sidebar fixture

Run from `frontend/`:

```sh
pnpm run dev:mounted-assets
```

Renders the production mounted-assets pane (`assetMountsPane` in
`src/components/deploymentForm.js`) against an in-memory asset store that has
real content and multiple versions per key, which the deployment-editor fixture
does not. The rest of the deployment form is omitted; only the summary row that
opens the pane is wired up.

## What the pane does

- **Compact table rows.** One grid row per mount under a shared
  `Asset | Container path | Mode | actions` header. The `Many` scenario shows ten
  mounts in roughly the height three used to need.
- **Actions at the right end.** Edit, preview and remove sit together in an
  action group; a revert button joins them only while a saved mount is edited.
  Remove is a cross rather than a bin, because it unmounts the asset from this
  deployment and does not delete the asset.
- **Inline asset editing.** Edit opens the asset editor overlay on the mounted
  asset. Saving writes a new version and re-points the mount at it, so the
  dropdown ends up on the version that was just edited. On a mount with no asset
  selected, edit opens the create form instead, same as `Create new asset`.
- **Version lives in the dropdown label** (`nginx.conf v3`). A mount pinned to a
  superseded version is labelled `(older)`.

## Scenarios

- `Typical` — five saved mounts, one of them pointing at an asset id that no
  longer resolves so the `won't be saved` state is visible.
- `Many` — ten mounts, for density.
- `Empty` — the header stays, with a centred `No assets currently mounted` as a
  single row occupying the same space a mount would, so nothing shifts when the
  first mount is added.

`Form state` under the form column shows what the pane writes back to
`form.assetMounts`. The asset catalog strip lists every version the mock store
holds, so a new version appearing after an inline save is visible.

## Mock behaviour

- `saveAsset` mints a new id and bumps the version for that key. It never
  overwrites, so repeated saves stack up versions.
- `geoip/city.mmdb` is a large asset and `branding/logo.png` is binary; editing
  either shows the editor's read-only states.
