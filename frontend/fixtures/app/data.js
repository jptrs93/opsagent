import {DATASETS as ACCESS} from "/fixtures/users-redesign/mock.js";
import {allDomainsExcluding, allowEffect, denyEffect, exactDomains} from "/src/lib/domainPolicies.js";

export const T = {
    DEPLOYMENT: 1, SCHEDULED_INSTANCE: 2, NODE: 3, SECRET: 4, CONFIG: 5, ASSET: 6, NETWORK_POLICY: 7, SPACE: 8, USER: 9,
    VALUE_DIRECTORY: 10, ASSET_DIRECTORY: 11, AUTHZ_GRANT_TEMPLATE: 12, AUTHZ_GRANT: 13, AUTHZ_GLOBAL_RULE: 14, SYSTEM_CONFIG: 15,
    SCHEDULED_INSTANCE_STATUS: 16, NODE_STATUS: 17, AGENT_SESSION: 18, USER_SESSION: 19, SPACE_DOMAIN_POLICY: 22,
};

export const SIGNED_IN_USER = 1;
export const CURRENT_SESSION_ID = "us_7f3a9c21e4b84d0e";
export const AGENT_VERSION = "v0.0.621";
export const RUNTIME_VERSIONS = "containerd 2.0.4, runc 1.2.6";

const MIN = 60e3, HOUR = 3600e3, DAY = 86400e3;

export const SPACE = {system: 0, global: 1, prod: 2, staging: 3, dev: 4};
export const NODE = {primary: 1, a: 2, b: 3, c: 4, edge: 5, pending: 6, evicted: 7};

const hex = (seed, length = 40) => {
    let out = "";
    for (let round = 0; out.length < length; round++) {
        let h = (2166136261 ^ round * 374761393) >>> 0;
        for (let i = 0; i < seed.length; i++) {
            h ^= seed.charCodeAt(i);
            h = Math.imul(h, 16777619) >>> 0;
        }
        h = (h ^ (h >>> 13)) >>> 0;
        h = Math.imul(h, 2246822519) >>> 0;
        h = (h ^ (h >>> 16)) >>> 0;
        out += h.toString(16).padStart(8, "0");
    }
    return out.slice(0, length);
};
export const commitSha = (seed) => hex(`commit:${seed}`);
export const bytesOf = (seed, n) => Uint8Array.from({length: n}, (_, i) => parseInt(hex(`${seed}:${i >> 3}`, 16).slice((i & 7) * 2, (i & 7) * 2 + 2), 16));

const v4 = (...o) => ({value: {ipv4: {octets: Uint8Array.from(o)}}});
const v6 = (text) => {
    const [head, tail = ""] = text.split("::");
    const parts = (s) => s ? s.split(":").map((p) => parseInt(p, 16)) : [];
    const h = parts(head), t = parts(tail);
    const groups = [...h, ...new Array(8 - h.length - t.length).fill(0), ...t];
    return {value: {ipv6: {octets: Uint8Array.from(groups.flatMap((g) => [g >> 8, g & 255]))}}};
};
const prefix4 = (a, b, c, d, len) => ({value: {ipv4: {address: {octets: Uint8Array.from([a, b, c, d])}, prefixLength: len}}});

const literal = (value) => ({value: {value: {literal: value}}});
const configSetting = (configId, version) => ({value: {value: {configRef: {configId, version}}}});
const secretEnv = (secretId, version) => ({value: {secret: {secret: {secretId, version}}}});
const configEnv = (configId, version) => ({value: {config: {config: {configId, version}}}});
const literalEnv = (value) => ({value: {literal: {value}}});
const addressEnv = (deploymentId, spaceId) => ({value: {address: {deploymentId, spaceId}}});

const WG_KEYS = [
    "Qm3xTzpL9vK2wR8nY5sH1cJ7dF4gB6aE0uI2oP9lM3k=",
    "aH7kLm2PqR9sT4vW6xY1zB3cD5eF8gJ0nM2oQ4rU6i=",
    "zX9cV2bN4mK7jH5gF3dS1aQ8wE6rT4yU2iO0pL9kM7n=",
    "pL5kJ3hG1fD9sA7qW2eR4tY6uI8oZ0xC3vB5nM7mK9l=",
    "rT2yU4iO6pA8sD0fG1hJ3kL5zX7cV9bN2mQ4wE6rT8y=",
    "mN8bV6cX4zL2kJ0hG9fD7sA5qW3eR1tY4uI6oP8lK2j=",
    "eR6tY8uI0oP2aS4dF6gH8jK0lZ2xC4vB6nM8qW0eR2t=",
];

const LOREM_NGINX = (v) => `worker_processes auto;
events { worker_connections ${1024 * v}; }

http {
    include       mime.types;
    sendfile      on;
    keepalive_timeout 65;
    gzip on;
    gzip_types text/css application/javascript application/json;

    upstream api {
        server api.space-2.internal:8080;
        keepalive 32;
    }

    server {
        listen 8080;
        server_name shop.example.com;
        client_max_body_size ${v * 8}m;

        location /api/ {
            proxy_pass http://api;
            proxy_set_header Host $host;
            proxy_set_header X-Request-Id $request_id;
        }

        location / {
            root /srv/www;
            try_files $uri /index.html;
        }
    }
}
`;

const CA_BUNDLE = `-----BEGIN CERTIFICATE-----
MIIDdzCCAl+gAwIBAgIUJ7qY0cZ2rC6m1vA0iU3yF0p8Q3QwDQYJKoZIhvcNAQEL
BQAwSzELMAkGA1UEBhMCTloxEzARBgNVBAoMCkFjbWUgTGltaXRlZDEnMCUGA1UE
AwweQWNtZSBJbnRlcm5hbCBSb290IENBIC0gMjAyNTAeFw0yNTAxMTUwMDAwMDBa
Fw0zNTAxMTMwMDAwMDBaMEsxCzAJBgNVBAYTAk5aMRMwEQYDVQQKDApBY21lIExp
bWl0ZWQxJzAlBgNVBAMMHkFjbWUgSW50ZXJuYWwgUm9vdCBDQSAtIDIwMjUwggEi
MA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIBAQC7mN2k1v8w0n3s1pXc8m1f3zq9
-----END CERTIFICATE-----
`;

const SEED_SQL = (v) => `-- dev seed data, revision ${v}
BEGIN;
INSERT INTO accounts (id, email, plan) VALUES
  (1, 'alice@example.com', 'pro'),
  (2, 'bob@example.com', 'free'),
  (3, 'carol@example.com', 'team')${v > 1 ? ",\n  (4, 'dave@example.com', 'free')" : ""};
INSERT INTO products (sku, name, price_cents) VALUES
  ('TSHIRT-01', 'Logo tee', 2900),
  ('MUG-02', 'Enamel mug', 1800);
COMMIT;
`;

const ROBOTS = `User-agent: *
Disallow: /admin/
Disallow: /api/
Sitemap: https://staging.example.net/sitemap.xml
`;

const FEATURE_FLAGS = (v) => JSON.stringify({
    checkout_v2: v >= 2,
    search_suggestions: true,
    recommendations: v >= 3 ? {enabled: true, model: "sentiment-v3"} : false,
    max_cart_items: 50,
}, null, 2);

// Builds the cluster as rows of {type, id, entity, actor, at, meta} in
// commit order. Versioned types (deployments, secrets, configs, assets) list
// one row per version.
export function buildDataset(now = Date.now()) {
    const rows = [];
    const ago = (ms) => now - ms;
    const put = (type, id, entity, {actor = SIGNED_IN_USER, at = ago(30 * DAY), meta = {}} = {}) => {
        rows.push({type, id, entity, actor, at, meta});
    };

    // --- spaces ---------------------------------------------------------------
    const spaces = [[0, "_system"], [1, "global"], [2, "prod"], [3, "staging"], [4, "dev"]];
    spaces.forEach(([id, name], i) => put(T.SPACE, id, {id, name}, {actor: 0, at: ago((120 - i * 10) * DAY)}));

    // --- users and access, reused from the users fixture ------------------------
    const access = ACCESS.typical();
    for (const u of access.users) {
        const pending = u.name === "sam";
        put(T.USER, u.id, {id: u.id, name: u.name, ...(pending ? {} : {activatedAt: new Date(u.activatedAt)})}, {actor: 1, at: u.createdAt});
    }
    for (const t of access.templates) put(T.AUTHZ_GRANT_TEMPLATE, t.id, {id: t.id, name: t.name, builtin: t.builtin, spec: t.spec}, {actor: t.author, at: t.createdAt});
    for (const g of access.grants) put(T.AUTHZ_GRANT, g.id, {id: g.id, userId: g.userId, grant: g.grant}, {actor: g.author, at: g.createdAt});
    for (const r of access.globalRules) put(T.AUTHZ_GLOBAL_RULE, r.id, {id: r.id, name: r.name, rule: r.rule}, {actor: r.author, at: r.createdAt});

    // --- nodes ----------------------------------------------------------------
    const nodes = [
        {id: NODE.primary, name: "primary", identifier: "7c1f0a6e-3b2d-4e8f-9a01-5d6c7b8e9f10", roles: [0], ula: "fd7a:115c:a1e0::1", host: [v4(10, 0, 0, 11), v4(203, 0, 113, 4), v6("2001:db8::11")], remote: "10.0.0.11", connectedAgo: 18 * DAY, enrolled: 120 * DAY},
        {id: NODE.a, name: "worker-a", identifier: "a19b4f02-5ca3-4ad2-9b61-ee0d1c2a3b44", roles: [1], ula: "fd7a:115c:a1e0::2", host: [v4(10, 0, 0, 12)], remote: "10.0.0.12", connectedAgo: 12 * DAY, enrolled: 110 * DAY},
        {id: NODE.b, name: "worker-b", identifier: "b7e2c813-1d4f-4a8e-a0c2-9f3b7d1e5c66", roles: [1], ula: "fd7a:115c:a1e0::3", host: [v4(10, 0, 0, 13), v4(203, 0, 113, 6)], remote: "10.0.0.13", connectedAgo: 2 * HOUR, enrolled: 95 * DAY, spaces: [0, 1, 2]},
        {id: NODE.c, name: "worker-c", identifier: "c4d9e1f0-8b2a-4c6d-b3e7-1a5f9c8d2e77", roles: [1], ula: "fd7a:115c:a1e0::4", host: [v4(10, 0, 0, 14)], remote: "10.0.0.14", connectedAgo: 5 * DAY, enrolled: 60 * DAY, status: 6, version: "v0.0.620", runtime: "containerd 2.0.3, runc 1.2.5"},
        {id: NODE.edge, name: "edge-1", identifier: "d8f3a7b2-6e1c-4f9a-8d2b-3c7e1f5a9b88", roles: [1], ula: "fd7a:115c:a1e0::5", host: [v4(198, 51, 100, 20)], remote: "198.51.100.20", connectedAgo: 9 * DAY, enrolled: 40 * DAY, connected: false, spaces: [0, 1, 4]},
    ];
    nodes.forEach((n, i) => {
        put(T.NODE, n.id, {
            id: n.id, status: n.status || 4,
            operator: {name: n.name, roles: n.roles, allowedSpaces: n.spaces || [0, 1, 2, 3, 4], enrolledTime: new Date(ago(n.enrolled))},
            reported: {identifier: n.identifier, underlayAddress: v6(n.ula), wgPublicKey: WG_KEYS[i], hostAddresses: n.host},
        }, {actor: 1, at: ago(n.enrolled)});
        const connected = n.connected !== false;
        put(T.NODE_STATUS, n.id, {
            nodeId: n.id, updatedAt: new Date(ago(connected ? 4e3 : n.connectedAgo - HOUR)), isConnected: connected,
            lastConnectedAt: new Date(ago(n.connectedAgo)), remoteAddress: `${n.remote}:${41000 + i * 17}`,
            opendeployVersion: n.version || AGENT_VERSION, runtimeVersions: n.runtime || RUNTIME_VERSIONS,
        }, {actor: 0, at: ago(connected ? 4e3 : n.connectedAgo)});
    });
    put(T.NODE, NODE.evicted, {
        id: NODE.evicted, status: 8,
        operator: {name: "old-worker", roles: [1], allowedSpaces: [0, 1, 2], enrolledTime: new Date(ago(200 * DAY))},
        reported: {identifier: "e2a6c4d8-0f3b-4e7a-9c1d-5b8f2a6e4c99", underlayAddress: v6("fd7a:115c:a1e0::7"), wgPublicKey: WG_KEYS[6], hostAddresses: [v4(10, 0, 0, 9)]},
    }, {actor: 1, at: ago(20 * DAY)});
    put(T.NODE, NODE.pending, {
        id: NODE.pending, status: 1, enrollmentRequestedAt: new Date(ago(6 * MIN)),
        operator: {name: "", roles: [1], allowedSpaces: []},
        reported: {identifier: "f5b1d9e3-2c7a-4b8f-a6e0-4d9c3b7f1a00", underlayAddress: v6("fd7a:115c:a1e0::6"), wgPublicKey: WG_KEYS[5], hostAddresses: [v4(10, 0, 0, 17)]},
    }, {actor: 0, at: ago(6 * MIN)});
    put(T.NODE_STATUS, NODE.pending, {
        nodeId: NODE.pending, updatedAt: new Date(ago(5e3)), isConnected: true, lastConnectedAt: new Date(ago(6 * MIN)),
        remoteAddress: "10.0.0.17:52114", opendeployVersion: AGENT_VERSION, runtimeVersions: RUNTIME_VERSIONS,
    }, {actor: 0, at: ago(5e3)});

    // --- value directories, secrets, configs -----------------------------------
    const vdirs = [
        [1, SPACE.prod, "db"], [2, SPACE.prod, "stripe"], [3, SPACE.prod, "api"], [4, SPACE.prod, "web"],
        [5, SPACE.staging, "db"], [6, SPACE.staging, "api"], [7, SPACE.global, "cluster"], [8, SPACE.global, "backup", 7],
    ];
    for (const [id, spaceId, key, parentId] of vdirs) put(T.VALUE_DIRECTORY, id, {id, spaceId, key, ...(parentId ? {parentId} : {})}, {at: ago((80 - id) * DAY)});

    const secretVersions = [];
    const secret = (id, spaceId, key, directoryId, versions) => {
        versions.forEach(([daysAgo, actor], i) => {
            put(T.SECRET, id, {id, fs: {key, ...(directoryId ? {directoryId} : {})}, spaceId}, {actor, at: ago(daysAgo * DAY), meta: {valueVersion: i + 1}});
        });
        secretVersions.push({id, key, versions: versions.length});
    };
    secret(1, SPACE.prod, "postgres_password", 1, [[70, 1], [41, 4], [9, 1]]);
    secret(2, SPACE.prod, "replica_password", 1, [[70, 1]]);
    secret(3, SPACE.prod, "api_key", 2, [[65, 2], [12, 2]]);
    secret(4, SPACE.prod, "webhook_secret", 2, [[65, 2]]);
    secret(5, SPACE.prod, "session_signing_key", 0, [[88, 1], [30, 1]]);
    secret(6, SPACE.staging, "postgres_password", 5, [[50, 3], [3, 3]]);
    secret(7, SPACE.staging, "api_token", 6, [[20, 3]]);
    secret(8, SPACE.global, "github_token", 7, [[110, 1], [14, 1]]);
    secret(9, SPACE.global, "s3_secret_access_key", 8, [[100, 1]]);
    secret(10, SPACE.dev, "openai_api_key", 0, [[6, 3]]);
    secret(11, SPACE.global, "grafana_admin_password", 0, [[45, 4]]);

    const config = (id, spaceId, key, directoryId, versions) => {
        versions.forEach(([daysAgo, actor, value], i) => {
            put(T.CONFIG, id, {id, fs: {key, ...(directoryId ? {directoryId} : {})}, spaceId, value}, {actor, at: ago(daysAgo * DAY), meta: {valueVersion: i + 1}});
        });
    };
    config(1, SPACE.prod, "feature_flags.json", 3, [[60, 2, FEATURE_FLAGS(1)], [22, 2, FEATURE_FLAGS(2)], [2, 4, FEATURE_FLAGS(3)]]);
    config(2, SPACE.prod, "log_level", 3, [[60, 2, "info"]]);
    config(3, SPACE.prod, "public_url", 4, [[58, 2, "https://shop.example.com"]]);
    config(4, SPACE.staging, "log_level", 6, [[40, 3, "info"], [8, 3, "debug"]]);
    config(5, SPACE.global, "s3_bucket", 8, [[100, 1, "acme-opendeploy-backups"]]);
    config(6, SPACE.global, "smtp_host", 0, [[90, 1, "smtp.eu.mailgun.org:587"]]);
    config(7, SPACE.dev, "database_url", 0, [[15, 3, "postgres://dev:dev@postgres.space-4.internal:5432/app"], [4, 3, "postgres://dev:dev@postgres.space-4.internal:5432/app?sslmode=disable"]]);

    // --- assets ---------------------------------------------------------------
    const adirs = [[1, SPACE.prod, "nginx"], [2, SPACE.prod, "certs"], [3, SPACE.global, "geo"], [4, SPACE.global, "models"], [5, SPACE.dev, "fixtures"]];
    for (const [id, spaceId, key] of adirs) put(T.ASSET_DIRECTORY, id, {id, spaceId, key}, {at: ago((75 - id) * DAY)});

    const enc = new TextEncoder();
    const assetContent = new Map();
    const asset = (id, spaceId, key, directoryId, versions) => {
        versions.forEach(([daysAgo, actor, content], i) => {
            const bytes = typeof content === "string" ? enc.encode(content) : content instanceof Uint8Array ? content : null;
            const sizeBytes = bytes ? bytes.length : content.size;
            if (bytes) assetContent.set(`${id}:${i + 1}`, bytes);
            put(T.ASSET, id, {
                id, fs: {key, ...(directoryId ? {directoryId} : {})}, spaceId, sizeBytes,
                sha256: bytesOf(`asset:${id}:${i}`, 32), storageKey: `a/${hex(`sk:${id}:${i}`, 32)}`,
            }, {actor, at: ago(daysAgo * DAY), meta: {valueVersion: i + 1}});
        });
    };
    asset(1, SPACE.prod, "nginx.conf", 1, [[50, 2, LOREM_NGINX(1)], [18, 2, LOREM_NGINX(2)], [1, 4, LOREM_NGINX(3)]]);
    asset(2, SPACE.prod, "ca-bundle.pem", 2, [[90, 1, CA_BUNDLE]]);
    asset(3, SPACE.global, "GeoLite2-City.mmdb", 3, [[70, 1, {size: 68_412_330}], [10, 1, {size: 68_903_114}]]);
    asset(4, SPACE.global, "sentiment-v3.onnx", 4, [[33, 4, {size: 14_221_904}]]);
    asset(5, SPACE.dev, "seed.sql", 5, [[12, 3, SEED_SQL(1)], [3, 3, SEED_SQL(2)]]);
    asset(6, SPACE.staging, "robots.txt", 0, [[25, 3, ROBOTS]]);
    asset(7, SPACE.global, "favicon.ico", 0, [[60, 1, bytesOf("favicon", 1150)]]);

    // --- deployments ----------------------------------------------------------
    const nixSource = (target, repo = "github.com/acme/platform") => ({value: {nixImageBuild: {repo, flake: "flake.nix", target}}});
    const imageSource = (image) => ({value: {remoteImage: {image}}});
    const https = (hostname, containerPort, extra = {}) => ({hostname, config: {value: {https: {containerPort, backendProtocol: 2, certSource: {value: {acme: {challenge: 1}}}, ...extra}}}});
    const deployment = ({id, name, spaceId, node, source, version, env = {}, running = true, ingress, ports, rollover = false, volume = "/data", mounts}) => ({
        id, name, spaceId,
        spec: {
            workload: {value: {container: {
                source, version,
                runtime: {envVars: env, defaultVolume: volume ? {containerPath: volume} : {disabled: true}, ...(mounts ? {assetMounts: mounts} : {})},
                upgradeStrategy: rollover ? 2 : 1,
                ...(rollover ? {readinessSignal: {timeoutSeconds: 120}} : {}),
            }}},
            networking: {mode: 1, ...(ingress ? {ingress} : {}), ...(ports ? {portForwarding: ports} : {})},
        },
        scheduling: {running, placement: {value: {dedicatedNodes: {nodes: [node]}}}},
    });

    const deployments = [];
    const instances = [];
    let instanceId = 1000;
    // versions: [{daysAgo, actor, version, spec: bool, patch}] oldest first.
    const addDeployment = (base, versions, live = []) => {
        let specVersion = 0;
        const records = versions.map((v, i) => {
            if (i === 0 || v.spec !== false) specVersion++;
            const entity = deployment({...base, ...(v.patch || {}), version: v.version ?? base.version, running: v.running ?? base.running ?? true});
            const record = {entity, version: i + 1, specVersion, at: ago(v.ago), actor: v.actor ?? SIGNED_IN_USER};
            put(T.DEPLOYMENT, base.id, entity, {actor: record.actor, at: record.at, meta: {version: i + 1, specVersion}});
            return record;
        });
        deployments.push({id: base.id, name: base.name, spaceId: base.spaceId, records});
        for (const inst of live) {
            const id = ++instanceId;
            const record = records[(inst.version || records.length) - 1];
            put(T.SCHEDULED_INSTANCE, id, {
                id, nodeId: inst.node || base.node, instanceOrdinal: inst.ordinal || 0, state: inst.state || 0, spaceId: base.spaceId,
                deployment: {deploymentId: base.id, version: record.version},
            }, {actor: 0, at: record.at + 2e3});
            const runner = inst.runner === null ? undefined : {
                deploymentSpecVersion: record.specVersion, runningPid: inst.pid || 4000 + id, runningArtifact: `sha256:${hex(`art:${base.id}:${record.version}`, 64)}`,
                status: inst.status ?? 2, numberOfRestarts: inst.restarts || 0,
                ...(inst.restarts ? {lastRestartAt: new Date(ago(inst.lastRestart || 40 * MIN))} : {}),
                ...(inst.exitCode != null ? {exitCode: inst.exitCode} : {}),
                networkDiagnostics: inst.diagnostics || [],
            };
            put(T.SCHEDULED_INSTANCE_STATUS, id, {
                scheduledInstanceId: id, updatedAt: new Date(ago(inst.updated || 20e3)),
                preparer: {deploymentSpecVersion: record.specVersion, artifact: `sha256:${hex(`art:${base.id}:${record.version}`, 64)}`, inputs: inst.inputs ?? 2, ...(inst.image === null ? {} : {image: inst.image ?? 4})},
                ...(runner ? {runner} : {}),
            }, {actor: 0, at: ago(inst.updated || 20e3)});
            instances.push({id, deploymentId: base.id, node: inst.node || base.node, version: record.version, specVersion: record.specVersion, status: inst.status ?? 2, restarts: inst.restarts || 0, runner: Boolean(runner)});
        }
    };

    const C = (seed) => commitSha(seed);

    addDeployment({id: 1, name: "api", spaceId: SPACE.prod, node: NODE.a, source: nixSource(".#api-image"), rollover: true,
        env: {DATABASE_PASSWORD: secretEnv(1, 3), STRIPE_API_KEY: secretEnv(3, 2), FEATURE_FLAGS: configEnv(1, 3), LOG_LEVEL: configEnv(2, 1), PORT: literalEnv("8080"), POSTGRES_HOST: addressEnv(4, SPACE.prod)},
        ingress: [https("api.example.com", 8080)]},
    [{ago: 40 * DAY, version: C("api-1")}, {ago: 21 * DAY, version: C("api-2"), actor: 2}, {ago: 6 * DAY, version: C("api-3"), actor: 5}, {ago: 2 * DAY, version: C("api-4"), actor: -1}, {ago: 4 * MIN, version: C("api-5")}],
    [{version: 4, state: 4, status: 2, pid: 31877}, {version: 5, state: 3, status: 4, pid: 40211, updated: 6e3}]);

    addDeployment({id: 2, name: "web", spaceId: SPACE.prod, node: NODE.a, source: imageSource("ghcr.io/acme/storefront"),
        env: {PUBLIC_URL: configEnv(3, 1), API_ORIGIN: addressEnv(1, SPACE.prod)}, ingress: [https("shop.example.com", 3000)],
        mounts: [{asset: {assetId: 1, version: 3}, containerPath: "/etc/nginx/nginx.conf", permission: 2}]},
    [{ago: 30 * DAY, version: "1.13.0"}, {ago: 9 * DAY, version: "1.14.1", actor: 2}, {ago: 3 * HOUR, version: "1.14.2", actor: 2}],
    [{status: 2}]);

    addDeployment({id: 3, name: "worker", spaceId: SPACE.prod, node: NODE.b, source: nixSource(".#worker-image"),
        env: {DATABASE_PASSWORD: secretEnv(1, 2), QUEUE_CONCURRENCY: literalEnv("8"), FEATURE_FLAGS: configEnv(1, 2)}},
    [{ago: 40 * DAY, version: C("worker-1")}, {ago: 11 * DAY, version: C("worker-2"), actor: 4}],
    [{status: 2, restarts: 1, lastRestart: 3 * DAY}]);

    addDeployment({id: 4, name: "postgres", spaceId: SPACE.prod, node: NODE.b, source: imageSource("docker.io/library/postgres"), volume: "/var/lib/postgresql/data",
        env: {POSTGRES_PASSWORD: secretEnv(1, 3), POSTGRES_DB: literalEnv("shop"), PGDATA: literalEnv("/var/lib/postgresql/data/pgdata")}},
    [{ago: 90 * DAY, version: "16.3"}, {ago: 9 * DAY, version: "16.4", actor: 4}],
    [{status: 2}]);

    addDeployment({id: 5, name: "billing-cron", spaceId: SPACE.prod, node: NODE.a, source: nixSource(".#billing-image"),
        env: {STRIPE_API_KEY: secretEnv(3, 1), STRIPE_WEBHOOK_SECRET: secretEnv(4, 1), DATABASE_PASSWORD: secretEnv(2, 1)}},
    [{ago: 33 * DAY, version: C("billing-1")}, {ago: 26 * HOUR, version: C("billing-2"), actor: 2}],
    [{status: 5, restarts: 14, lastRestart: 2 * MIN, exitCode: 1, updated: 90e3}]);

    addDeployment({id: 6, name: "redis", spaceId: SPACE.prod, node: NODE.b, source: imageSource("docker.io/library/redis")},
    [{ago: 75 * DAY, version: "7.2.5-alpine"}, {ago: 14 * DAY, version: "7.4.1-alpine", actor: 4}],
    [{status: 2}]);

    addDeployment({id: 7, name: "api", spaceId: SPACE.staging, node: NODE.c, source: nixSource(".#api-image"), rollover: true,
        env: {DATABASE_PASSWORD: secretEnv(6, 2), API_TOKEN: secretEnv(7, 1), LOG_LEVEL: configEnv(4, 2), PORT: literalEnv("8080")},
        ingress: [https("api.staging.example.net", 8080)]},
    [{ago: 30 * DAY, version: C("api-2")}, {ago: 5 * DAY, version: C("api-4"), actor: 3}, {ago: 50 * MIN, version: C("api-5"), actor: -3}],
    [{status: 2}]);

    addDeployment({id: 8, name: "web", spaceId: SPACE.staging, node: NODE.c, source: imageSource("ghcr.io/acme/storefront"),
        ingress: [https("staging.example.com", 3000)]},
    [{ago: 20 * DAY, version: "1.14.1"}, {ago: 25 * MIN, version: "1.15.0-rc.1", actor: 3}],
    [{status: 3, runner: null, image: 5, inputs: 2, updated: 22 * MIN}]);

    addDeployment({id: 9, name: "search", spaceId: SPACE.staging, node: NODE.c, source: imageSource("docker.io/opensearchproject/opensearch"), volume: "/usr/share/opensearch/data",
        env: {"discovery.type": literalEnv("single-node"), OPENSEARCH_JAVA_OPTS: literalEnv("-Xms1g -Xmx1g")}},
    [{ago: 12 * DAY, version: "2.15.0"}, {ago: 2 * MIN, version: "2.17.1", actor: 3}],
    [{status: 4, updated: 8e3}]);

    addDeployment({id: 10, name: "worker", spaceId: SPACE.staging, node: NODE.c, source: nixSource(".#worker-image"),
        env: {DATABASE_PASSWORD: secretEnv(6, 2), QUEUE_CONCURRENCY: literalEnv("2")}},
    [{ago: 18 * DAY, version: C("worker-1")}, {ago: 3 * MIN, version: C("worker-3"), actor: 3}],
    [{runner: null, image: 1, inputs: 2, updated: 10e3}]);

    addDeployment({id: 11, name: "playground", spaceId: SPACE.dev, node: NODE.b, source: imageSource("docker.io/library/nginx"), running: false},
    [{ago: 16 * DAY, version: "1.27-alpine", running: true}, {ago: 4 * DAY, version: "1.27-alpine", running: false, spec: false, actor: 3}],
    [{status: 3, state: 2, updated: 4 * DAY}]);

    addDeployment({id: 12, name: "notebook", spaceId: SPACE.dev, node: NODE.edge, source: imageSource("quay.io/jupyter/scipy-notebook"),
        env: {OPENAI_API_KEY: secretEnv(10, 1), DATABASE_URL: configEnv(7, 2)}, ports: [{protocol: 1, hostPort: 8888, containerPort: 8888, ipFilter: [{mode: 1, prefix: prefix4(10, 0, 0, 0, 8)}]}]},
    [{ago: 8 * DAY, version: "2024-10-07", actor: 3}],
    [{status: 2, updated: 9 * DAY}]);

    addDeployment({id: 13, name: "api", spaceId: SPACE.dev, node: NODE.b, source: nixSource(".#api-image"), running: false,
        env: {DATABASE_URL: configEnv(7, 2), LOG_LEVEL: literalEnv("debug")}},
    [{ago: 15 * DAY, version: C("api-3"), running: false, actor: 3}]);

    addDeployment({id: 14, name: "grafana", spaceId: SPACE.global, node: NODE.primary, source: imageSource("docker.io/grafana/grafana"),
        env: {GF_SECURITY_ADMIN_PASSWORD: secretEnv(11, 1), GF_SERVER_ROOT_URL: literalEnv("https://grafana.ops.example.net")},
        ingress: [https("grafana.ops.example.net", 3000)]},
    [{ago: 60 * DAY, version: "11.1.0"}, {ago: 7 * DAY, version: "11.2.0", actor: 4}],
    [{status: 2}]);

    addDeployment({id: 15, name: "prometheus", spaceId: SPACE.global, node: NODE.primary, source: imageSource("docker.io/prom/prometheus"), volume: "/prometheus"},
    [{ago: 60 * DAY, version: "v2.53.0"}, {ago: 7 * DAY, version: "v2.54.1", actor: 4}],
    [{status: 2, restarts: 2, lastRestart: 6 * DAY}]);

    addDeployment({id: 16, name: "docs", spaceId: SPACE.global, node: NODE.a, source: nixSource(".#docs-site", "github.com/acme/handbook"),
        ingress: [https("docs.example.net", 8080)]},
    [{ago: 44 * DAY, version: C("docs-1")}, {ago: 1 * DAY, version: C("docs-2"), actor: 2}],
    [{status: 2}]);

    const systemNodes = [NODE.a, NODE.b, NODE.c, NODE.edge, NODE.primary];
    const systemVersion = (node) => node === NODE.c ? "v0.0.620" : AGENT_VERSION;
    systemNodes.forEach((node, i) => {
        for (const [offset, name, image] of [[100, "opendeploy", "github.com/jptrs93/opsagent"], [110, "opendeploy-net", "github.com/jptrs93/opsagent"]]) {
            const v = systemVersion(node);
            addDeployment({id: offset + i + 1, name, spaceId: SPACE.system, node, source: imageSource(image), volume: null},
                [{ago: 30 * DAY, version: "v0.0.618", actor: 0}, {ago: 2 * DAY + i * HOUR, version: v, actor: 1}],
                [node === NODE.edge ? {status: 2, updated: 9 * DAY} : {status: 2}]);
        }
    });

    // --- policies -------------------------------------------------------------
    const space = (spaceId) => ({target: {value: {space: {spaceId}}}});
    const dep = (deploymentId) => ({target: {value: {deployment: {deploymentId}}}});
    const tcp = (start, end = start) => ({protocol: 1, range: {start, end}});
    put(T.NETWORK_POLICY, 1, {id: 1, action: 1, source: dep(15), destination: space(SPACE.prod), ports: [tcp(9100, 9200)]}, {actor: 4, at: ago(50 * DAY)});
    put(T.NETWORK_POLICY, 2, {id: 2, action: 1, source: space(SPACE.dev), destination: dep(9), ports: [tcp(9200)]}, {actor: 3, at: ago(11 * DAY)});
    put(T.NETWORK_POLICY, 3, {id: 3, action: 1, source: dep(14), destination: dep(15), ports: []}, {actor: 4, at: ago(48 * DAY)});
    put(T.NETWORK_POLICY, 4, {id: 4, action: 1, source: space(SPACE.staging), destination: dep(6), ports: [tcp(6379), {protocol: 2, range: {start: 6380, end: 6380}}]}, {actor: 2, at: ago(4 * DAY)});

    const domainPolicy = (id, name, effect, spaces, domains, disabled = false, actor = 1, daysAgo = 60) =>
        put(T.SPACE_DOMAIN_POLICY, id, {id, name, disabled, rule: {effect, selector: {spaces, domains}}}, {actor, at: ago(daysAgo * DAY)});
    domainPolicy(1, "default_ingress_domains", allowEffect(), {allSpacesExcluding: {values: []}}, allDomainsExcluding(), false, 0, 120);
    domainPolicy(2, "example_com_is_prod", denyEffect(), {allSpacesExcluding: {values: [SPACE.prod]}}, exactDomains(["example.com", "*.example.com"]), false, 1, 40);
    domainPolicy(3, "staging_previews", allowEffect(), {exactSpaces: {values: [SPACE.staging]}}, exactDomains(["*.preview.example.net"]), true, 3, 9);

    // --- sessions -------------------------------------------------------------
    const chromeMac = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36";
    const safariIphone = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1";
    const firefoxLinux = "Mozilla/5.0 (X11; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0";
    const userSession = (id, sessionId, extra, daysAgo) => put(T.USER_SESSION, id, {id, sessionId, userId: SIGNED_IN_USER, kind: 0, ...extra}, {actor: SIGNED_IN_USER, at: ago(daysAgo * DAY)});
    userSession(1, CURRENT_SESSION_ID, {expiresAt: new Date(now + 6 * DAY), requestingAddress: "203.0.113.50", userAgent: chromeMac}, 1);
    userSession(2, "us_19be04c6aa7f4e21", {expiresAt: new Date(now + 3 * DAY), requestingAddress: "198.51.100.88", userAgent: safariIphone}, 4);
    userSession(3, "us_c2d8a0f13e9b4c55", {expiresAt: new Date(ago(2 * DAY)), requestingAddress: "203.0.113.50", userAgent: firefoxLinux}, 9);
    userSession(4, "us_5a7e3d9b0c1f4a88", {expiresAt: new Date(now + 2 * DAY), revokedAt: new Date(ago(2 * DAY)), requestingAddress: "192.0.2.14", userAgent: chromeMac}, 5);
    put(T.USER_SESSION, 5, {id: 5, sessionId: "us_link_e1f04b7d2a", userId: 6, kind: 2, expiresAt: new Date(now + 7 * DAY - 2 * HOUR), requestingAddress: "203.0.113.50", userAgent: chromeMac, issuedBy: SIGNED_IN_USER}, {actor: SIGNED_IN_USER, at: ago(2 * HOUR)});

    const agentSession = (id, sessionId, extra, minutesAgo) => put(T.AGENT_SESSION, id, {id, sessionId, userId: SIGNED_IN_USER, ...extra}, {actor: SIGNED_IN_USER, at: ago(minutesAgo * MIN)});
    agentSession(1, "as_4c1d9e7f20b3", {status: 1, requestingAddress: "10.0.0.31", approvalCode: "K7QZ-4PXM"}, 2);
    agentSession(2, "as_b81f02a6c94e", {status: 2, approvedAt: new Date(ago(3 * HOUR)), requestingAddress: "10.0.0.31", token: {prefix: "oda_3f9c", expiresAt: new Date(now + 21 * HOUR)}}, 190);
    agentSession(3, "as_77a0e3d51f28", {status: 2, approvedAt: new Date(ago(3 * DAY)), requestingAddress: "203.0.113.50", token: {prefix: "oda_91e2", expiresAt: new Date(ago(2 * DAY))}}, 3 * 1440);
    agentSession(4, "as_0e5c6b9a8d17", {status: 4, approvedAt: new Date(ago(6 * DAY)), requestingAddress: "198.51.100.88", token: {prefix: "oda_6d04", expiresAt: new Date(now + DAY)}}, 6 * 1440);
    agentSession(5, "as_2f9d4e1c7b60", {status: 3, requestingAddress: "192.0.2.77"}, 8 * 1440);

    // --- system config ----------------------------------------------------------
    put(T.SYSTEM_CONFIG, 1, {
        id: 1,
        networkUlaPrefix: Uint8Array.from([0xfd, 0x7a, 0x11, 0x5c, 0xa1, 0xe0]),
        settings: {
            httpWeb: {enabled: literal(false), listen: literal(":80")},
            httpsWeb: {enabled: literal(true), listen: literal(":443"), tlsSelfManaged: literal(false), acmeHosts: literal("opendeploy.example.net"), acmeEmail: literal("ops@example.net")},
            cluster: {listen: literal("10.0.0.11:9443"), enrollmentListen: literal("10.0.0.11:9444")},
            repo: {githubToken: {secretId: 8, version: 2}},
            backup: {
                enabled: literal(true), s3AccessKeyId: literal("AKIA4EXAMPLE7Q2DPRIM"), s3SecretAccessKey: {secretId: 9, version: 1},
                s3Bucket: configSetting(5, 1), s3Path: literal("primary/"), s3Region: literal("ap-southeast-2"), s3Endpoint: literal("https://s3.ap-southeast-2.amazonaws.com"),
            },
            largeAssets: {useSeparateS3: literal(false), keepLocalCopy: literal(true)},
            auth: {requirePasskey: literal(false)},
        },
    }, {actor: 1, at: ago(14 * DAY)});

    const sidecars = {
        backupStatus: {
            configured: true, running: true, inSync: false, localTxid: 184231, remoteTxid: 184229,
            lastSuccessfulSyncAt: new Date(ago(40e3)), assetPending: 2, assetTargetS3: true, assetKeepLocal: true,
        },
        secretsStatus: {unlocked: true, recoveryConfigured: true},
        ingressDiagnostics: {items: [{deploymentId: 8, message: "hostname staging.example.com is refused for space staging by space domain policy example_com_is_prod"}]},
    };

    const deleted = [
        {entity: deployment({id: 40, name: "legacy-admin", spaceId: SPACE.prod, node: NODE.a, source: imageSource("ghcr.io/acme/admin"), version: "0.9.4", running: false}), version: 6, specVersion: 4, createdAt: ago(200 * DAY), deletedAt: ago(3 * DAY), actor: 2},
        {entity: deployment({id: 41, name: "load-test", spaceId: SPACE.staging, node: NODE.c, source: nixSource(".#k6-runner"), version: commitSha("k6"), running: false}), version: 3, specVersion: 2, createdAt: ago(30 * DAY), deletedAt: ago(9 * DAY), actor: 3},
        {entity: deployment({id: 42, name: "mailhog", spaceId: SPACE.dev, node: NODE.b, source: imageSource("docker.io/mailhog/mailhog"), version: "v1.0.1", running: false}), version: 2, specVersion: 1, createdAt: ago(60 * DAY), deletedAt: ago(20 * DAY), actor: 3},
    ];

    return {rows, sidecars, deployments, instances, assetContent, deleted, secretVersions};
}
