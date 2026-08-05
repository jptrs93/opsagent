const encoder = new TextEncoder();

const at = day => new Date(`2026-07-${String(day).padStart(2, '0')}T09:30:00Z`);

const nginxV2 = `worker_processes auto;

events { worker_connections 1024; }

http {
    server {
        listen 8080;
        location / { proxy_pass http://127.0.0.1:3000; }
    }
}
`;

const nginxV3 = `worker_processes auto;

events { worker_connections 2048; }

http {
    server {
        listen 8080;
        location /healthz { return 200 'ok'; }
        location / { proxy_pass http://127.0.0.1:3000; }
    }
}
`;

const entrypoint = `#!/bin/sh
set -eu

exec /app/api serve --config /etc/api/api.yaml
`;

const apiYaml = `listen: ":3000"
log_level: info
features:
  rollover: true
`;

// Every version of every asset. versionedAssetOptions() collapses this to the
// latest per key, which is what the mount dropdown offers.
export const mockAssetVersions = [
    {id: 201, key: 'nginx.conf', version: 2, format: 'text', spaceId: 1, createdAt: at(4), body: nginxV2},
    {id: 205, key: 'nginx.conf', version: 3, format: 'text', spaceId: 1, createdAt: at(11), body: nginxV3},
    {id: 210, key: 'entrypoint.sh', version: 1, format: 'text', spaceId: 1, createdAt: at(6), body: entrypoint},
    {id: 214, key: 'api.yaml', version: 4, format: 'yaml', spaceId: 1, createdAt: at(18), body: apiYaml},
    // Exercises the editor's binary and large read-only states from the pane.
    {id: 220, key: 'branding/logo.png', version: 1, format: 'binary', spaceId: 1, createdAt: at(2), binary: true},
    {id: 224, key: 'geoip/city.mmdb', version: 2, format: 'binary', spaceId: 1, createdAt: at(9), large: true, sizeBytes: 68_400_000},
];

export const mockAssets = mockAssetVersions.map(({body, binary, large, sizeBytes, ...meta}) => meta);

// key@version -> full asset record, as the load-asset endpoint would return it.
export const mockAssetContent = new Map(mockAssetVersions.map(asset => {
    const blob = asset.binary
        ? new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0xff, 0x10, 0x42])
        : encoder.encode(asset.body || '');
    return [`${asset.key}@${asset.version}`, {
        id: asset.id,
        key: asset.key,
        version: asset.version,
        format: asset.format,
        spaceId: asset.spaceId,
        createdAt: asset.createdAt,
        blob: asset.large ? new Uint8Array() : blob,
        location: asset.large ? 'assets/geoip/city.mmdb' : '',
        sizeBytes: asset.large ? asset.sizeBytes : blob.length,
    }];
}));

// Mirrors an update deployment: saved mounts carry original* so the pane can
// show and discard edits.
const savedMount = (id, assetId, path, executable = false) => ({
    id,
    assetId,
    path,
    executable,
    originalAssetId: assetId,
    originalPath: path,
    originalExecutable: executable,
});

export const mockAssetMounts = [
    savedMount(1, 205, '/etc/nginx/nginx.conf'),
    savedMount(2, 210, '/usr/local/bin/entrypoint.sh', true),
    savedMount(3, 214, '/etc/api/api.yaml'),
    savedMount(4, 224, '/usr/share/geoip/city.mmdb'),
    // Points at a version that no longer resolves, so the pane shows its
    // "won't be saved" state.
    savedMount(5, 999, '/etc/api/removed.conf'),
];
