import * as model from "/src/capi/model.js";
import {Capi} from "/src/capi/capi.js";
import {AGENT_VERSION, CURRENT_SESSION_ID, NODE, SIGNED_IN_USER, SPACE, T, buildDataset, bytesOf, commitSha} from "./data.js";
import {runSpaces, writePolicies} from "/fixtures/nodeAccess.js";

const FIELDS = {
    [T.DEPLOYMENT]: "deployment", [T.SCHEDULED_INSTANCE]: "scheduledInstance", [T.NODE]: "node", [T.SECRET]: "secret", [T.CONFIG]: "config",
    [T.ASSET]: "asset", [T.NETWORK_POLICY]: "networkPolicy", [T.SPACE]: "space", [T.USER]: "user", [T.VALUE_DIRECTORY]: "valueDirectory",
    [T.ASSET_DIRECTORY]: "assetDirectory", [T.AUTHZ_GRANT_TEMPLATE]: "authzGrantTemplate", [T.AUTHZ_GRANT]: "authzGrant",
    [T.AUTHZ_GLOBAL_RULE]: "authzGlobalRule", [T.SYSTEM_CONFIG]: "systemConfig", [T.SCHEDULED_INSTANCE_STATUS]: "scheduledInstanceStatus",
    [T.NODE_STATUS]: "nodeStatus", [T.AGENT_SESSION]: "agentSession", [T.USER_SESSION]: "userSession", [T.SPACE_DOMAIN_POLICY]: "spaceDomainPolicy",
    [T.NODE_ACCESS_POLICY]: "nodeAccessPolicy",
};
const NODE_ACCESS_INPUTS = new Set([T.NODE, T.SPACE, T.NODE_ACCESS_POLICY]);
const VERSIONED = new Set([T.DEPLOYMENT, T.SECRET, T.CONFIG, T.ASSET]);
const MIN = 60e3, HOUR = 3600e3, DAY = 86400e3;

export const rt = (name, value) => model[`decode${name}`](model[`encode${name}`](value).buffer);
const clone = (value) => structuredClone(value);
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const refuse = (status, displayErr) => Object.assign(new Error(displayErr), {status, apiErr: {displayErr}});
const abortError = () => new DOMException("The operation was aborted.", "AbortError");

// --- store ----------------------------------------------------------------------

class Store {
    constructor(dataset) {
        this.seq = 0;
        this.entries = new Map();
        this.subscribers = new Set();
        this.sidecars = dataset.sidecars;
        for (const row of dataset.rows) this.#record(row.type, row.id, row.entity, row.actor, row.at, row.meta);
    }

    key(type, id) { return `${type}:${id}`; }

    #record(type, id, entity, actor, at, extra = {}) {
        const key = this.key(type, id);
        const list = this.entries.get(key) || [];
        const first = list[0]?.meta;
        const meta = {createdTime: first ? first.createdTime : at, updatedTime: at, updatedSeq: ++this.seq, updatedActor: actor, ...extra};
        const item = {type, id, entity: clone(entity), meta};
        if (VERSIONED.has(type)) list.push(item);
        else list.splice(0, list.length, item);
        this.entries.set(key, list);
        return item;
    }

    latest(type, id) {
        const list = this.entries.get(this.key(type, id));
        return list ? list[list.length - 1] : undefined;
    }

    get(type, id) {
        const item = this.latest(type, id);
        return item ? clone(item.entity) : undefined;
    }

    all(type) {
        const out = [];
        for (const list of this.entries.values()) if (list[0].type === type) out.push(list[list.length - 1]);
        return out;
    }

    history(type, id) { return this.entries.get(this.key(type, id)) || []; }

    nextId(type) { return this.all(type).reduce((max, item) => Math.max(max, Number(item.id)), 0) + 1; }

    snapshot() {
        const entities = [];
        for (const list of this.entries.values()) {
            for (const item of VERSIONED.has(list[0].type) ? list : [list[list.length - 1]]) {
                entities.push({entityType: item.type, entityId: item.id, entity: {value: {[FIELDS[item.type]]: item.entity}}, meta: item.meta});
            }
        }
        return {seq: this.seq, entities};
    }

    // ops: [{op: "create"|"update"|"delete", type, id, entity, specChanged, valueChanged}]
    commit(actor, ops) {
        const time = Date.now();
        const mutations = [];
        let seq = this.seq;
        for (const op of ops) {
            if (op.op === "delete") {
                this.entries.delete(this.key(op.type, op.id));
                mutations.push({value: {delete: {entityType: op.type, entityId: op.id}}});
                seq = ++this.seq;
                continue;
            }
            const prev = this.latest(op.type, op.id)?.meta || {};
            const extra = {};
            if (op.type === T.DEPLOYMENT) {
                extra.version = Number(prev.version || 0) + 1;
                extra.specVersion = Number(prev.specVersion || 0) + (op.specChanged === false && prev.specVersion ? 0 : 1);
            }
            if (op.type === T.SECRET || op.type === T.CONFIG || op.type === T.ASSET) {
                extra.valueVersion = Number(prev.valueVersion || 0) + (op.valueChanged === false && prev.valueVersion ? 0 : 1);
            }
            const item = this.#record(op.type, op.id, op.entity, actor, time, extra);
            seq = item.meta.updatedSeq;
            const kind = op.op === "create" ? "create" : "update";
            mutations.push({value: {[kind]: {entityType: op.type, entityId: op.id, entity: {value: {[FIELDS[op.type]]: item.entity}}, meta: item.meta}}});
        }
        const update = rt("CoreWriteUpdate", {seq, time, actor, mutations});
        const message = {events: [update], seq};
        if (ops.some((op) => NODE_ACCESS_INPUTS.has(op.type))) {
            this.sidecars.nodeAccess = this.nodeAccess();
            message.nodeAccess = this.sidecars.nodeAccess;
        }
        this.publish(message);
        return update;
    }

    nodeAccess() {
        const policies = this.all(T.NODE_ACCESS_POLICY).map((item) => item.entity);
        const spaceIds = this.all(T.SPACE).map((item) => Number(item.id));
        return {items: this.all(T.NODE).map((item) => ({nodeId: item.id, runDeploymentSpaces: runSpaces(policies, item.id, spaceIds)}))};
    }

    publish(message) {
        const decoded = rt("EventStreamMsg", message);
        for (const push of this.subscribers) push(decoded);
    }

    publishSidecar(name, value) {
        this.sidecars[name] = value;
        this.publish({[name]: value, seq: this.seq});
    }

    stream(signal) {
        const store = this;
        return (async function* () {
            const queue = [];
            let wake = null;
            const push = (message) => { queue.push(message); wake?.(); };
            store.subscribers.add(push);
            try {
                yield rt("EventStreamMsg", {snapshot: store.snapshot(), seq: store.seq, synced: true, ...store.sidecars});
                while (true) {
                    if (signal?.aborted) throw abortError();
                    if (!queue.length) {
                        await new Promise((resolve) => {
                            const timer = setTimeout(done, 3000);
                            function done() { clearTimeout(timer); signal?.removeEventListener("abort", done); wake = null; resolve(); }
                            wake = done;
                            signal?.addEventListener("abort", done, {once: true});
                        });
                    }
                    if (signal?.aborted) throw abortError();
                    yield queue.length ? queue.shift() : rt("EventStreamMsg", {heartbeat: true, seq: store.seq});
                }
            } finally {
                store.subscribers.delete(push);
            }
        })();
    }
}

// --- generated content ------------------------------------------------------------

const AUTHORS = ["alice", "bob", "priya", "joss", "deploy-bot"];
const MESSAGES = [
    "Bump nixpkgs to 24.11", "checkout: retry idempotent Stripe calls", "Add request id to access logs", "worker: drain queue on SIGTERM",
    "Fix N+1 in order history endpoint", "search: tune suggestion cutoff", "Move feature flags to config", "api: expose /healthz/ready",
    "Upgrade Go toolchain to 1.23.4", "billing: handle proration on downgrade", "Pin postgres client to 16", "Cache product images for 1h",
    "Drop legacy v1 cart endpoints", "web: lazy-load recommendations", "Add OpenTelemetry spans to checkout",
];

const commitsFor = (seed, current, now) => {
    const list = Array.from({length: 18}, (_, i) => ({
        id: i === 3 && current ? current : commitSha(`${seed}-c${i}`),
        label: MESSAGES[(i + seed.length) % MESSAGES.length],
        author: AUTHORS[(i * 3 + seed.length) % AUTHORS.length],
        time: new Date(now - (i * 7 + 2) * HOUR),
    }));
    if (current && !list.some((c) => c.id === current)) list[3].id = current;
    return list;
};

const tagsFor = (image, current, now) => {
    const known = {
        "ghcr.io/acme/storefront": ["1.15.0-rc.1", "1.14.2", "1.14.1", "1.14.0", "1.13.2", "1.13.0", "1.12.4"],
        "docker.io/library/postgres": ["17.2", "17.1", "16.6", "16.5", "16.4", "16.3", "15.10"],
        "docker.io/library/redis": ["7.4.2-alpine", "7.4.1-alpine", "7.4.0-alpine", "7.2.6-alpine", "7.2.5-alpine"],
        "docker.io/grafana/grafana": ["11.4.0", "11.3.1", "11.2.0", "11.1.0", "10.4.12"],
        "docker.io/prom/prometheus": ["v3.0.1", "v2.55.1", "v2.54.1", "v2.53.0"],
    }[image] || [current || "latest", "stable", "1.0.0"];
    const tags = known.includes(current) || !current ? known : [current, ...known];
    return tags.map((tag, i) => ({id: tag, label: tag, author: "", time: new Date(now - (i * 9 + 1) * DAY)}));
};

const releases = (now) => ["v0.0.621", "v0.0.620", "v0.0.619", "v0.0.618", "v0.0.617", "v0.0.616", "v0.0.615"]
    .map((tag, i) => ({id: tag, label: tag, author: "jptrs93", time: new Date(now - (i * 3 + 1) * DAY)}));

const LOG_MESSAGES = {
    INFO: [
        ["request completed", (r) => ({method: r.pick(["GET", "GET", "GET", "POST", "PUT"]), path: r.pick(["/api/v1/orders", "/api/v1/cart", "/api/v1/products", "/healthz", "/api/v1/checkout"]), status: r.pick(["200", "200", "200", "201", "204", "304"]), duration_ms: String(r.int(2, 180)), request_id: r.hex(12)})],
        ["job finished", (r) => ({job: r.pick(["send_receipt", "sync_inventory", "reindex_product", "expire_carts"]), duration_ms: String(r.int(20, 4000)), attempt: "1"})],
        ["cache refreshed", (r) => ({cache: r.pick(["products", "prices", "shipping_zones"]), entries: String(r.int(100, 9000))})],
        ["connected to database", () => ({host: "postgres.space-2.internal", pool_size: "20"})],
    ],
    DEBUG: [
        ["sql query", (r) => ({query: r.pick(["SELECT * FROM orders WHERE user_id = $1", "UPDATE carts SET updated_at = now() WHERE id = $1", "SELECT id, sku FROM products LIMIT 50"]), rows: String(r.int(0, 50)), duration_ms: String(r.int(1, 30))})],
        ["cache hit", (r) => ({key: `product:${r.int(1000, 9999)}`})],
    ],
    WARN: [
        ["slow request", (r) => ({path: "/api/v1/checkout", duration_ms: String(r.int(1200, 4800)), request_id: r.hex(12)})],
        ["retrying upstream call", (r) => ({upstream: "api.stripe.com", attempt: String(r.int(2, 4)), err: "context deadline exceeded"})],
        ["connection pool nearly exhausted", (r) => ({in_use: String(r.int(17, 19)), max: "20"})],
    ],
    ERROR: [
        ["payment intent failed", (r) => ({err: "card_declined", order_id: `ord_${r.hex(10)}`, request_id: r.hex(12)})],
        ["upstream returned 502", (r) => ({upstream: "search.space-3.internal:9200", path: "/products/_search"})],
        ["pool exhausted", () => ({err: "timeout acquiring connection after 5s"})],
    ],
};
const PLAIN_LINES = ["Listening on 0.0.0.0:8080", "Received SIGHUP, reloading configuration", "goroutine profile written to /tmp/pprof", "GC forced"];

const rng = (seed) => {
    let s = seed >>> 0 || 1;
    const next = () => { s = (Math.imul(s ^ (s >>> 15), 2246822519) + 1013904223) >>> 0; return s / 4294967296; };
    return {
        next,
        int: (a, b) => a + Math.floor(next() * (b - a + 1)),
        pick: (list) => list[Math.floor(next() * list.length)],
        hex: (n) => Array.from({length: n}, () => Math.floor(next() * 16).toString(16)).join(""),
    };
};

function buildLogs(store, req) {
    const end = req.timeEnd instanceof Date ? req.timeEnd.getTime() : Date.now();
    const start = req.timeStart instanceof Date ? req.timeStart.getTime() : end - 12 * HOUR;
    const deploymentId = Number(req.deploymentId || 0);
    const r = rng(deploymentId * 7919 + 17);
    const latest = store.latest(T.DEPLOYMENT, deploymentId);
    const version = Number(latest?.meta.version || 1);
    const nodeId = Number(latest?.entity.scheduling?.placement?.value?.dedicatedNodes?.nodes?.[0] || req.targetNodeId || 1);
    const crashy = latest?.entity.name === "billing-cron";
    const total = 420;
    const records = [];
    for (let i = 0; i < total; i++) {
        const t = start + (end - start) * Math.pow(i / total, 0.85) + r.int(0, 900);
        const roll = r.next();
        const level = crashy ? (roll < 0.35 ? "ERROR" : roll < 0.55 ? "WARN" : "INFO") : roll < 0.04 ? "ERROR" : roll < 0.13 ? "WARN" : roll < 0.38 ? "DEBUG" : roll < 0.97 ? "INFO" : "";
        let msg, fields = {};
        if (!level) msg = r.pick(PLAIN_LINES);
        else {
            const [text, make] = r.pick(LOG_MESSAGES[level]);
            msg = text;
            fields = make(r);
        }
        if (level === "ERROR" && r.next() < 0.3) msg += "\n    at handler (/app/internal/checkout/handler.go:142)\n    at serve (/app/internal/http/server.go:88)";
        const run = crashy ? 1 + Math.floor(i / 30) : i > total * 0.7 ? 2 : 1;
        records.push({
            time: BigInt(Math.round(t)) * 1000000n, level, msg, fields: {...fields, ...(level ? {logger: r.pick(["http", "db", "jobs", "main"])} : {})},
            version: i > total * 0.8 ? version : Math.max(1, version - 1), stream: level === "ERROR" ? 1 : 0, instanceOrdinal: 0, run, node: nodeId, seq: i + 1,
        });
    }
    const filters = req.filters || [];
    const matches = (rec) => filters.every((f) => {
        const value = f.field === "level" ? rec.level : f.field === "msg" || !f.field ? rec.msg : f.field === "run" ? String(rec.run) : f.field === "instance" ? String(rec.instanceOrdinal) : rec.fields[f.field];
        const v = String(value ?? "").toLowerCase();
        switch (f.op) {
            case "eq": return v === String(f.value).toLowerCase();
            case "neq": return v !== String(f.value).toLowerCase();
            case "in": return (f.values || []).map((x) => x.toLowerCase()).includes(v);
            case "exists": return value != null && value !== "";
            case "not_exists": return value == null || value === "";
            case "contains": return v.includes(String(f.value).toLowerCase());
            case "not_contains": return !v.includes(String(f.value).toLowerCase());
            default: return true;
        }
    });
    const versionFilter = Number(req.deploymentVersion || 0);
    const matched = records.filter((rec) => (!versionFilter || rec.version === versionFilter) && matches(rec));
    const buckets = Number(req.histogramBuckets || 0);
    const bucketMs = buckets ? Math.ceil((end - start) / buckets) : 0;
    const series = buckets ? ["DEBUG", "INFO", "WARN", "ERROR", "OTHER", ""].map((level) => ({level, counts: new Array(buckets).fill(0)})) : [];
    for (const rec of matched) {
        if (!buckets) break;
        const b = Math.min(buckets - 1, Math.floor((Number(rec.time / 1000000n) - start) / bucketMs));
        const s = series.find((x) => x.level === rec.level) || series.find((x) => x.level === "OTHER");
        s.counts[b] = (s.counts[b] || 0) * 1 + 1;
    }
    const fieldNames = [...new Set(matched.flatMap((rec) => Object.keys(rec.fields)))].sort();
    const fields = [...fieldNames, "run"].map((field) => {
        const counts = new Map();
        let present = 0;
        for (const rec of matched) {
            const value = field === "run" ? String(rec.run) : rec.fields[field];
            if (value == null) continue;
            present++;
            counts.set(value, (counts.get(value) || 0) + 1);
        }
        const top = [...counts.entries()].sort((a, b) => b[1] - a[1]).slice(0, 8).map(([value, count]) => ({value, count}));
        return {field, coverage: matched.length ? present / matched.length : 0, distinct: counts.size, top, other: present - top.reduce((n, t) => n + t.count, 0)};
    });
    const limit = Number(req.limit || 0);
    const kept = limit < 0 ? [] : matched.slice(-(limit || 10000)).reverse();
    return rt("LogQueryResponse", {
        stats: {timeStart: new Date(start), timeEnd: new Date(end), scannedRows: total * 37, matchedRows: matched.length, returnedRows: kept.length, truncated: false, tookMs: 23 + (deploymentId % 9), sampledRows: matched.length},
        histogram: buckets ? {bucketMs, startTime: new Date(start), series} : undefined,
        fields,
        records: kept,
    });
}

const METRIC_SHAPES = {
    cpu_usage_usec: [0.32e6, 0.12e6], cpu_user_usec: [0.24e6, 0.09e6], cpu_system_usec: [0.07e6, 0.03e6], cpu_throttled_usec: [0.004e6, 0.006e6],
    mem_current: [412e6, 40e6], mem_anon: [298e6, 30e6], mem_file: [96e6, 12e6],
    net_rx_bytes: [184e3, 90e3], net_tx_bytes: [412e3, 160e3], io_read_bytes: [24e3, 30e3], io_write_bytes: [96e3, 70e3],
    pids: [34, 4], open_fds: [118, 12], tcp_established: [46, 14], tcp_time_wait: [12, 6], tcp_close_wait: [1, 1],
    psi_cpu_some_avg10: [2.1, 1.6], psi_mem_some_avg10: [0.3, 0.3], psi_io_some_avg10: [0.8, 0.7],
    mem_oom_kill: [0, 0], cpu_nr_throttled: [0.4, 0.5], net_rx_dropped: [0.01, 0.02], net_tx_dropped: [0, 0.005],
};
const GAUGES = new Set(["mem_current", "mem_anon", "mem_file", "pids", "open_fds", "tcp_established", "tcp_time_wait", "tcp_close_wait"]);
const KERNEL = new Set(["psi_cpu_some_avg10", "psi_mem_some_avg10", "psi_io_some_avg10"]);

function buildMetrics(store, req) {
    const end = req.timeEnd instanceof Date ? req.timeEnd.getTime() : Date.now();
    const start = req.timeStart instanceof Date ? req.timeStart.getTime() : end - HOUR;
    const stepMs = Math.max(30e3, Math.ceil((end - start) / 120 / 30e3) * 30e3);
    const buckets = Math.ceil((end - start) / stepMs);
    const id = Number(req.deploymentId || 0);
    const latest = store.latest(T.DEPLOYMENT, id);
    const version = Number(latest?.meta.version || 1);
    const nodeId = Number(latest?.entity.scheduling?.placement?.value?.dedicatedNodes?.nodes?.[0] || 1);
    const instances = store.all(T.SCHEDULED_INSTANCE).filter((i) => Number(i.entity.deployment?.deploymentId) === id);
    const instanceId = Number(instances[0]?.id || 1);
    const scale = 0.6 + (id % 5) * 0.25;
    const split = Math.floor(buckets * 0.58);
    const runs = [
        {run: 1, deploymentVersion: Math.max(1, version - 1), from: 0, to: split},
        {run: 2, deploymentVersion: version, from: split, to: buckets},
    ];
    const fields = req.fields?.length ? req.fields : Object.keys(METRIC_SHAPES);
    const series = [];
    for (const run of runs) {
        const r = rng(id * 131 + run.run * 17);
        for (const field of fields) {
            const [base, jitter] = METRIC_SHAPES[field] || [1, 1];
            const values = Array.from({length: buckets}, (_, b) => {
                if (b < run.from || b >= run.to) return NaN;
                const wave = Math.sin((b + id * 3) / 9) * 0.5 + Math.sin(b / 3.1) * 0.2;
                const ramp = field.startsWith("mem") ? (b - run.from) / Math.max(1, run.to - run.from) * 0.35 : 0;
                const spike = field === "mem_oom_kill" ? (b === split - 1 ? 1 : 0) : 0;
                return Math.max(0, base * scale * (1 + ramp) + jitter * scale * (wave + (r.next() - 0.5))) + spike;
            });
            series.push({scheduledInstanceId: instanceId, ordinal: 0, deploymentVersion: run.deploymentVersion, run: run.run, nodeId, field, kind: GAUGES.has(field) ? 1 : KERNEL.has(field) ? 2 : 0, values});
        }
    }
    return rt("MetricsQueryResponse", {timeStart: new Date(start - (start % stepMs)), stepMs, buckets, series, scannedRows: buckets * runs.length * 6, tookMs: 18});
}

function buildLatest(store) {
    const now = Date.now();
    const entries = [];
    for (const inst of store.all(T.SCHEDULED_INSTANCE)) {
        const status = store.latest(T.SCHEDULED_INSTANCE_STATUS, inst.id)?.entity;
        if (!status?.runner || ![2, 4].includes(status.runner.status)) continue;
        const deploymentId = Number(inst.entity.deployment.deploymentId);
        const dep = store.latest(T.DEPLOYMENT, deploymentId);
        if (!dep || Number(dep.entity.spaceId) === SPACE.system) continue;
        const r = rng(deploymentId * 97 + inst.id);
        const scale = 0.4 + r.next() * 2.2;
        const nodeStatus = store.latest(T.NODE_STATUS, Number(inst.entity.nodeId))?.entity;
        const age = nodeStatus?.isConnected === false ? 9 * 60 + r.int(0, 50) : r.int(1, 9);
        entries.push({
            sample: {
                time: now - age * 1000, deploymentId, scheduledInstanceId: inst.id, ordinal: 0, deploymentVersion: Number(inst.entity.deployment.version),
                run: 1 + (status.runner.numberOfRestarts || 0), nodeId: Number(inst.entity.nodeId),
                memCurrent: Math.round(METRIC_SHAPES.mem_current[0] * scale), memAnon: Math.round(METRIC_SHAPES.mem_anon[0] * scale),
                pids: r.int(6, 80), tcpEstablished: r.int(0, 120), openFds: r.int(20, 400),
            },
            rates: [
                {field: "cpu_usage_usec", perSecond: METRIC_SHAPES.cpu_usage_usec[0] * scale * (0.3 + r.next())},
                {field: "net_rx_bytes", perSecond: METRIC_SHAPES.net_rx_bytes[0] * scale * r.next() * 2},
                {field: "net_tx_bytes", perSecond: METRIC_SHAPES.net_tx_bytes[0] * scale * r.next() * 2},
                {field: "io_read_bytes", perSecond: METRIC_SHAPES.io_read_bytes[0] * scale * r.next()},
                {field: "io_write_bytes", perSecond: METRIC_SHAPES.io_write_bytes[0] * scale * r.next() * 3},
            ],
        });
    }
    return rt("MetricsLatestResponse", {entries, warnings: ["edge-1: no samples for 9m (node disconnected)"]});
}

const NIX_BUILD_LOG = (name, version) => [
    `building '/nix/store/${commitSha(`drv-${name}`).slice(0, 32)}-${name}-image.json.drv'...`,
    `fetching git input 'git+https://github.com/acme/platform?rev=${version}'`,
    "copying path '/nix/store/4x8kq2-go-1.23.4' from 'https://cache.nixos.org'...",
    "copying path '/nix/store/9pz1mf-cacert-3.101' from 'https://cache.nixos.org'...",
    `building '/nix/store/a7c2-${name}-vendor.drv'...`,
    `${name}-vendor> go: downloading github.com/jackc/pgx/v5 v5.7.1`,
    `${name}-vendor> go: downloading go.opentelemetry.io/otel v1.32.0`,
    `building '/nix/store/f1e9-${name}-0.1.0.drv'...`,
    `${name}> Running phase: unpackPhase`,
    `${name}> Running phase: buildPhase`,
    `${name}> Building subPackage ./cmd/${name}`,
    `${name}> Running phase: checkPhase`,
    `${name}> ok   github.com/acme/platform/internal/checkout   1.284s`,
    `${name}> ok   github.com/acme/platform/internal/orders     0.912s`,
    `${name}> Running phase: installPhase`,
    `building '/nix/store/0d3b-${name}-image.json.drv'...`,
    "nix2container: 4 layers, 38.2 MiB",
    "image ready: sha256:" + commitSha(`img-${name}`) + commitSha(`img2-${name}`).slice(0, 24),
];

// --- capi -------------------------------------------------------------------------

export function installServer(capi, {log = console} = {}) {
    const now = Date.now();
    const dataset = buildDataset(now);
    const store = new Store(dataset);
    const assetContent = dataset.assetContent;
    const secretValues = new Map();
    const deletedDeployments = dataset.deleted.map((d) => ({
        deployment: d.entity,
        meta: {createdTime: d.createdAt, updatedTime: d.deletedAt, updatedSeq: 0, updatedActor: d.actor, version: d.version, specVersion: d.specVersion, deleted: true},
    }));
    let account = {
        userId: SIGNED_IN_USER, name: "joss", email: "joss@example.net", hasPassword: true, activatedAt: new Date(now - 120 * DAY),
        passkeys: [
            {id: bytesOf("pk1", 16), label: "MacBook Pro Touch ID", createdAt: new Date(now - 80 * DAY), lastUsedAt: new Date(now - DAY)},
            {id: bytesOf("pk2", 16), label: "YubiKey 5C", createdAt: new Date(now - 40 * DAY)},
        ],
    };

    const loginResponse = (userId = SIGNED_IN_USER, kind = 0, sessionId = CURRENT_SESSION_ID) => rt("LoginResponse", {
        token: `fixture-${kind}-${userId}`, userId, name: store.get(T.USER, userId)?.name || "", expiry: new Date(Date.now() + 6 * DAY), sessionId, kind,
    });

    const deploymentOf = (id) => {
        const item = store.latest(T.DEPLOYMENT, Number(id));
        if (!item) throw refuse(404, "Deployment not found");
        return item;
    };
    const instancesOf = (deploymentId) => store.all(T.SCHEDULED_INSTANCE).filter((i) => Number(i.entity.deployment?.deploymentId) === Number(deploymentId) && i.entity.state !== 2);
    const statusOf = (instanceId) => store.get(T.SCHEDULED_INSTANCE_STATUS, instanceId);

    const rollout = async (deploymentId) => {
        const dep = store.latest(T.DEPLOYMENT, deploymentId);
        if (!dep) return;
        const {version, specVersion} = dep.meta;
        const running = Boolean(dep.entity.scheduling?.running);
        const nodeId = Number(dep.entity.scheduling?.placement?.value?.dedicatedNodes?.nodes?.[0] || NODE.primary);
        let instances = instancesOf(deploymentId);
        if (!instances.length && running) {
            const id = store.nextId(T.SCHEDULED_INSTANCE) + 1000;
            store.commit(0, [{op: "create", type: T.SCHEDULED_INSTANCE, id, entity: {id, nodeId, instanceOrdinal: 0, state: 0, spaceId: dep.entity.spaceId, deployment: {deploymentId, version}}}]);
            instances = instancesOf(deploymentId);
        }
        const artifact = `sha256:${commitSha(`art-${deploymentId}-${version}`)}`;
        const setStatus = (inst, preparer, runner) => store.commit(0, [{op: "update", type: T.SCHEDULED_INSTANCE_STATUS, id: inst.id, entity: {
            scheduledInstanceId: inst.id, updatedAt: new Date(), preparer: {deploymentSpecVersion: specVersion, artifact, ...preparer},
            ...(runner ? {runner: {deploymentSpecVersion: specVersion, runningArtifact: artifact, runningPid: 5000 + inst.id, numberOfRestarts: 0, ...runner}} : {}),
        }}]);
        for (const inst of instances) {
            store.commit(0, [{op: "update", type: T.SCHEDULED_INSTANCE, id: inst.id, entity: {...inst.entity, deployment: {deploymentId, version}}}]);
            const previous = statusOf(inst.id);
            if (!running) {
                setStatus(inst, {inputs: 2, image: 4}, {...(previous?.runner || {}), status: 3});
                continue;
            }
            setStatus(inst, {inputs: 2, image: 1}, null);
            await sleep(1500);
            setStatus(inst, {inputs: 2, image: 4}, {status: 4});
            await sleep(2500);
            setStatus(inst, {inputs: 2, image: 4}, {status: 2});
        }
    };

    const historyFor = (deploymentId) => {
        const versions = store.history(T.DEPLOYMENT, deploymentId);
        const entries = versions.map((v) => ({value: {deployment: {deployment: v.entity, meta: v.meta}}}));
        versions.forEach((v, i) => {
            const at = v.meta.updatedTime;
            const spec = v.meta.specVersion;
            const base = {scheduledInstanceId: 1, preparer: {deploymentSpecVersion: spec, inputs: 2, image: 4}};
            entries.push({value: {status: {...base, updatedAt: new Date(at + 20e3), preparer: {deploymentSpecVersion: spec, inputs: 2, image: 1}}}});
            entries.push({value: {status: {...base, updatedAt: new Date(at + 65e3), runner: {deploymentSpecVersion: spec, status: 4}}}});
            if (i < versions.length - 1 || v.entity.scheduling?.running) {
                entries.push({value: {status: {...base, updatedAt: new Date(at + 80e3), runner: {deploymentSpecVersion: spec, status: 2, runningPid: 3100 + i}}}});
            }
        });
        for (const inst of instancesOf(deploymentId)) {
            const s = statusOf(inst.id);
            if (s) entries.push({value: {status: s}});
        }
        const time = (e) => e.value.deployment ? Number(e.value.deployment.meta.updatedTime) : e.value.status.updatedAt.getTime();
        entries.sort((a, b) => time(b) - time(a));
        return rt("DeploymentHistory", {entries});
    };

    const envRefs = (entity, kind, id) => Object.entries(entity.spec?.workload?.value?.container?.runtime?.envVars || {})
        .filter(([, env]) => Number(kind === "secret" ? env.value?.secret?.secret?.secretId : env.value?.config?.config?.configId) === Number(id));

    const repointDeployments = (kind, id, version) => {
        const ops = [];
        for (const dep of store.all(T.DEPLOYMENT)) {
            const refs = envRefs(dep.entity, kind, id);
            if (!refs.length) continue;
            const entity = clone(dep.entity);
            for (const [name] of refs) {
                const env = entity.spec.workload.value.container.runtime.envVars[name];
                if (kind === "secret") env.value.secret.secret.version = version;
                else env.value.config.config.version = version;
            }
            ops.push({op: "update", type: T.DEPLOYMENT, id: dep.id, entity});
        }
        return ops;
    };

    const valueOps = (type, field) => ({
        create: (request, value) => {
            const id = store.nextId(type);
            const entity = {id, fs: {key: request.key, ...(request.valueDirectoryId ? {directoryId: Number(request.valueDirectoryId)} : {})}, spaceId: Number(request.spaceId || 0), ...(field ? {[field]: value} : {})};
            return {id, update: store.commit(SIGNED_IN_USER, [{op: "create", type, id, entity}])};
        },
        set: (id, value, repoint, kind) => {
            const entity = store.get(type, id);
            if (!entity) throw refuse(404, "Not found");
            if (field) entity[field] = value;
            const ops = [{op: "update", type, id, entity, valueChanged: true}];
            const nextVersion = Number(store.latest(type, id).meta.valueVersion || 0) + 1;
            if (repoint) ops.push(...repointDeployments(kind, id, nextVersion));
            return store.commit(SIGNED_IN_USER, ops);
        },
        rename: (id, newKey) => {
            const entity = store.get(type, id);
            entity.fs = {...entity.fs, key: newKey};
            return store.commit(SIGNED_IN_USER, [{op: "update", type, id, entity, valueChanged: false}]);
        },
        move: (id, directoryId, spaceId) => {
            const entity = store.get(type, id);
            entity.fs = {key: entity.fs.key, ...(directoryId ? {directoryId: Number(directoryId)} : {})};
            if (spaceId != null) entity.spaceId = Number(spaceId);
            return store.commit(SIGNED_IN_USER, [{op: "update", type, id, entity, valueChanged: false}]);
        },
        remove: (id) => { store.commit(SIGNED_IN_USER, [{op: "delete", type, id}]); },
    });
    const secrets = valueOps(T.SECRET, null);
    const configs = valueOps(T.CONFIG, "value");
    const assets = valueOps(T.ASSET, null);

    const dirOps = (type, decodeName) => ({
        create: ({spaceId, parentId, key}) => {
            const id = store.nextId(type);
            const entity = {id, spaceId: Number(spaceId || 0), key, ...(parentId ? {parentId: Number(parentId)} : {})};
            store.commit(SIGNED_IN_USER, [{op: "create", type, id, entity}]);
            return rt(decodeName, entity);
        },
        rename: ({directoryId, newKey}) => {
            const entity = {...store.get(type, directoryId), key: newKey};
            store.commit(SIGNED_IN_USER, [{op: "update", type, id: Number(directoryId), entity}]);
            return rt(decodeName, entity);
        },
        move: ({directoryId, newParentId, spaceId}) => {
            const current = store.get(type, directoryId);
            const entity = {id: current.id, key: current.key, spaceId: spaceId != null ? Number(spaceId) : current.spaceId, ...(newParentId ? {parentId: Number(newParentId)} : {})};
            store.commit(SIGNED_IN_USER, [{op: "update", type, id: Number(directoryId), entity}]);
            return rt(decodeName, entity);
        },
        remove: ({directoryId}) => { store.commit(SIGNED_IN_USER, [{op: "delete", type, id: Number(directoryId)}]); },
    });
    const valueDirs = dirOps(T.VALUE_DIRECTORY, "ValueDirectory");
    const assetDirs = dirOps(T.ASSET_DIRECTORY, "AssetDirectory");

    const nodeByIdentifier = (identifier) => {
        const item = store.all(T.NODE).find((n) => n.entity.reported?.identifier === identifier);
        if (!item) throw refuse(404, "Node not found");
        return item;
    };
    const updateNode = (identifier, patch) => {
        const item = nodeByIdentifier(identifier);
        const entity = patch(clone(item.entity));
        return store.commit(SIGNED_IN_USER, [{op: "update", type: T.NODE, id: item.id, entity}]);
    };

    const checkNodeAccessWrite = (write) => {
        const policies = store.all(T.NODE_ACCESS_POLICY).map((item) => item.entity);
        const placements = store.all(T.DEPLOYMENT).map((d) => {
            const nodeId = Number(d.entity.scheduling?.placement?.value?.dedicatedNodes?.nodes?.[0] || 0);
            const spaceId = Number(d.entity.spaceId || 0);
            return {deployment: d.entity.name, spaceId, nodeId, space: store.get(T.SPACE, spaceId)?.name, node: store.get(T.NODE, nodeId)?.operator?.name};
        }).filter((d) => d.nodeId);
        writePolicies(policies, write, placements, refuse);
    };

    const revealDefault = (secretId, version) => {
        const key = store.get(T.SECRET, secretId)?.fs?.key || "secret";
        if (key === "github_token") return `ghp_${commitSha(`gh${version}`).slice(0, 36)}`;
        if (key.includes("api_key")) return `sk_live_${commitSha(`${key}${version}`).slice(0, 32)}`;
        return `${commitSha(`${key}:${version}`).slice(0, 8)}-${commitSha(`${key}:${version}:b`).slice(0, 16)}`;
    };

    const handlers = {
        postV1GlobalEventStream: (_, options = {}) => store.stream(options.signal),

        async getV1AuthMethods() { return rt("AuthMethodsResponse", {passkeyLoginEnabled: true, requirePasskey: false, localCaAvailable: true}); },
        async getV1AuthCurrentSession() { return loginResponse(); },
        async postV1AuthPasswordLogin({username}) {
            await sleep(300);
            const user = store.all(T.USER).find((u) => u.entity.name === username);
            if (!user) throw refuse(401, "Invalid username or password");
            return loginResponse(user.id);
        },
        async postV1AuthPasskeyLoginStart() { throw refuse(400, "Passkeys are not available in the fixture"); },
        async postV1AuthPasskeyRegisterStart() { throw refuse(400, "Passkeys are not available in the fixture"); },
        async postV1AuthMaster({password}) {
            await sleep(300);
            if (!password) throw refuse(401, "Wrong master password");
            return rt("LoginResponse", {token: "fixture-root", userId: 0, name: "", expiry: new Date(Date.now() + 10 * MIN), sessionId: "us_root_fixture", kind: 1});
        },
        async postV1AuthOpenSession({userId}) { return loginResponse(Number(userId)); },
        async postV1AuthLinkRedeem() {
            await sleep(200);
            return rt("LinkRedeemResponse", {session: loginResponse(6, 0, "us_landing_fixture"), nameSet: false, passwordSet: false});
        },
        async postV1AuthMasterPasswordVerify({password}) { await sleep(300); if (!password) throw refuse(401, "Wrong master password"); },
        async postV1AuthMasterPasswordSave() { await sleep(300); },

        async getV1Account() { return rt("AccountView", account); },
        async postV1AccountRename({name}) {
            const user = {...store.get(T.USER, account.userId), name};
            account = {...account, name};
            return store.commit(account.userId, [{op: "update", type: T.USER, id: account.userId, entity: user}]);
        },
        async postV1AccountPassword({next}) { await sleep(200); account = {...account, hasPassword: Boolean(next)}; },
        async postV1AccountPasskeysDelete({id}) {
            const key = (bytes) => Array.from(bytes || []).join(",");
            account = {...account, passkeys: account.passkeys.filter((p) => key(p.id) !== key(id))};
        },

        async postV1UsersCreate({name, grants}) {
            const id = store.nextId(T.USER);
            const ops = [{op: "create", type: T.USER, id, entity: {id, ...(name ? {name} : {})}}];
            let grantId = store.nextId(T.AUTHZ_GRANT);
            for (const grant of grants || []) ops.push({op: "create", type: T.AUTHZ_GRANT, id: grantId, entity: {id: grantId++, userId: id, grant}});
            return store.commit(SIGNED_IN_USER, ops);
        },
        async postV1UsersLink({userId}) {
            const id = store.nextId(T.USER_SESSION);
            const sessionId = `us_link_${commitSha(`link${id}`).slice(0, 10)}`;
            const expiresAt = new Date(Date.now() + 7 * DAY);
            store.commit(SIGNED_IN_USER, [{op: "create", type: T.USER_SESSION, id, entity: {id, sessionId, userId: Number(userId), kind: 2, expiresAt, requestingAddress: "203.0.113.50", issuedBy: SIGNED_IN_USER}}]);
            return rt("UserLinkResponse", {token: `l_${commitSha(`tok${id}`).slice(0, 32)}`, sessionId, expiresAt});
        },

        async postV1AccessGrantTemplatesCreate({name, spec}) {
            const id = store.nextId(T.AUTHZ_GRANT_TEMPLATE);
            return store.commit(SIGNED_IN_USER, [{op: "create", type: T.AUTHZ_GRANT_TEMPLATE, id, entity: {id, name, builtin: false, spec}}]);
        },
        async postV1AccessGrantTemplatesUpdate({id, name, spec}) {
            const current = store.get(T.AUTHZ_GRANT_TEMPLATE, id);
            if (current?.builtin) throw refuse(409, "Built-in roles cannot be changed");
            return store.commit(SIGNED_IN_USER, [{op: "update", type: T.AUTHZ_GRANT_TEMPLATE, id: Number(id), entity: {...current, name, spec}}]);
        },
        async postV1AccessGrantTemplatesDelete({id}) {
            if (store.get(T.AUTHZ_GRANT_TEMPLATE, id)?.builtin) throw refuse(409, "Built-in roles cannot be deleted");
            if (store.all(T.AUTHZ_GRANT).some((g) => Number(g.entity.grant?.value?.template?.templateId) === Number(id))) throw refuse(409, "Roles referenced by grants cannot be deleted");
            store.commit(SIGNED_IN_USER, [{op: "delete", type: T.AUTHZ_GRANT_TEMPLATE, id: Number(id)}]);
        },
        async postV1AccessGrantsCreate({userId, grant}) {
            const id = store.nextId(T.AUTHZ_GRANT);
            return store.commit(SIGNED_IN_USER, [{op: "create", type: T.AUTHZ_GRANT, id, entity: {id, userId: Number(userId), grant}}]);
        },
        async postV1AccessGrantsDelete({id}) { store.commit(SIGNED_IN_USER, [{op: "delete", type: T.AUTHZ_GRANT, id: Number(id)}]); },
        async postV1AccessGlobalRulesCreate({name, rule}) {
            const id = store.nextId(T.AUTHZ_GLOBAL_RULE);
            return store.commit(SIGNED_IN_USER, [{op: "create", type: T.AUTHZ_GLOBAL_RULE, id, entity: {id, name, rule}}]);
        },
        async postV1AccessGlobalRulesDelete({id}) { store.commit(SIGNED_IN_USER, [{op: "delete", type: T.AUTHZ_GLOBAL_RULE, id: Number(id)}]); },

        async postV1AgentSessionsApprove({sessionId}) {
            const item = store.all(T.AGENT_SESSION).find((s) => s.entity.sessionId === sessionId);
            const entity = {...item.entity, status: 2, approvedAt: new Date()};
            delete entity.approvalCode;
            store.commit(SIGNED_IN_USER, [{op: "update", type: T.AGENT_SESSION, id: item.id, entity}]);
            return rt("AgentSession", entity);
        },
        async postV1AgentSessionsRevoke({sessionId}) {
            const item = store.all(T.AGENT_SESSION).find((s) => s.entity.sessionId === sessionId);
            store.commit(SIGNED_IN_USER, [{op: "update", type: T.AGENT_SESSION, id: item.id, entity: {...item.entity, status: item.entity.status === 1 ? 3 : 4}}]);
        },
        async postV1UserSessionsRevoke({sessionId}) {
            const item = store.all(T.USER_SESSION).find((s) => s.entity.sessionId === sessionId);
            store.commit(SIGNED_IN_USER, [{op: "update", type: T.USER_SESSION, id: item.id, entity: {...item.entity, revokedAt: new Date()}}]);
        },

        async postV1DeploymentsCreate({name, spaceId, spec, scheduling}) {
            const id = store.nextId(T.DEPLOYMENT);
            const update = store.commit(SIGNED_IN_USER, [{op: "create", type: T.DEPLOYMENT, id, entity: {id, name, spaceId: Number(spaceId || 0), spec, scheduling}}]);
            void rollout(id);
            return update;
        },
        async postV1DeploymentsUpdate({deploymentId, expectedSeq, update: change}) {
            const item = deploymentOf(deploymentId);
            if (expectedSeq && Number(expectedSeq) < Number(item.meta.updatedSeq)) throw refuse(409, "Deployment was modified concurrently");
            const entity = clone(item.entity);
            let specChanged = true;
            if (change.versionOnly) {
                entity.spec.workload.value.container.version = change.versionOnly.targetVersion;
                entity.scheduling.running = true;
            } else if (change.runningOnly) {
                entity.scheduling.running = Boolean(change.runningOnly.desiredRunning);
                specChanged = false;
            } else if (change.spec) {
                entity.spec = change.spec.spec;
            } else if (change.assignedSpace) {
                entity.spaceId = Number(change.assignedSpace.spaceId);
                specChanged = false;
            } else if (change.restart) {
                entity.scheduling.restartGeneration = Number(entity.scheduling.restartGeneration || 0) + 1;
                specChanged = false;
            }
            const receipt = store.commit(SIGNED_IN_USER, [{op: "update", type: T.DEPLOYMENT, id: item.id, entity, specChanged}]);
            setTimeout(() => void rollout(item.id), 600);
            return receipt;
        },
        async postV1DeploymentsDelete({deploymentId}) {
            const item = deploymentOf(deploymentId);
            const ops = instancesOf(item.id).map((inst) => ({op: "update", type: T.SCHEDULED_INSTANCE, id: inst.id, entity: {...inst.entity, state: 2}}));
            ops.push({op: "delete", type: T.DEPLOYMENT, id: item.id});
            deletedDeployments.unshift({deployment: item.entity, meta: {...item.meta, updatedTime: Date.now(), deleted: true}});
            store.commit(SIGNED_IN_USER, ops);
        },
        async postV1DeploymentsRecentlyDeleted() { return rt("RecentlyDeletedDeployments", {items: deletedDeployments}); },
        async postV1DeploymentsHistory({deploymentId}) { await sleep(120); return historyFor(Number(deploymentId)); },
        async postV1DeploymentsVersions({deploymentId, selectedBranch}) {
            await sleep(250);
            const item = deploymentOf(deploymentId);
            const container = item.entity.spec?.workload?.value?.container || {};
            const source = container.source?.value || {};
            if (Number(item.entity.spaceId) === SPACE.system) return rt("DeploymentVersions", {deploymentId, source: {githubRelease: {releases: releases(Date.now())}}});
            if (source.nixImageBuild) {
                const branch = selectedBranch || "main";
                return rt("DeploymentVersions", {deploymentId, source: {nixImageBuild: {branches: ["main", "release/2026-11", "feat/checkout-v2", "fix/pool-timeouts"], selectedBranch: branch, commits: commitsFor(`${source.nixImageBuild.repo}:${branch}`, branch === "main" ? container.version : "", Date.now())}}});
            }
            return rt("DeploymentVersions", {deploymentId, source: {containerImage: {tags: tagsFor(source.remoteImage?.image, container.version, Date.now())}}});
        },
        async postV1ReposValidate(request) {
            await sleep(350);
            const ok = (message) => ({checked: true, ok: true, message});
            if (request.source?.containerImage) {
                const image = request.source.containerImage.image;
                return rt("RepoValidateResponse", {source: {containerImage: {image: ok(`${image} is reachable`), tags: request.source.containerImage.refreshVersions ? tagsFor(image, "", Date.now()) : []}}});
            }
            const nix = request.source?.nixImageBuild || {};
            const branch = nix.selectedBranch || "main";
            const commits = commitsFor(`${nix.repoUrl}:${branch}`, "", Date.now());
            return rt("RepoValidateResponse", {source: {nixImageBuild: {
                checkedRepoUrl: nix.repoUrl, gitRepository: nix.checkRepo ? ok("Repository is accessible") : {},
                checkedBranch: branch, branchCheck: nix.checkBranch ? ok(`Branch ${branch} exists`) : {},
                checkedCommit: nix.selectedCommit, commitCheck: nix.checkCommit ? ok("Commit found") : {},
                checkedFlakePath: nix.selectedFlakePath || "", nixFlakeFile: nix.checkFlakePath ? ok("flake.nix found") : {},
                availableBranches: nix.refreshAvailableBranches ? {loaded: true, branches: ["main", "release/2026-11", "feat/checkout-v2", "fix/pool-timeouts"]} : {},
                availableCommits: nix.refreshAvailableCommits ? {loaded: true, branch, commits} : {},
            }}});
        },
        postV1DeploymentsPrepareOutput({deploymentId}, options = {}) {
            const item = store.latest(T.DEPLOYMENT, Number(deploymentId));
            const lines = NIX_BUILD_LOG(item?.entity.name || "app", item?.entity.spec?.workload?.value?.container?.version || "");
            const enc = new TextEncoder();
            return (async function* () {
                for (const line of lines) {
                    if (options.signal?.aborted) throw abortError();
                    await sleep(40);
                    yield rt("PrepareOutputChunk", {data: enc.encode(`${line}\n`)});
                }
            })();
        },
        async postV1DeploymentsRunReport({scheduledInstanceId, run}) {
            await sleep(150);
            const inst = store.latest(T.SCHEDULED_INSTANCE, Number(scheduledInstanceId));
            const dep = store.latest(T.DEPLOYMENT, Number(inst?.entity.deployment?.deploymentId));
            const status = statusOf(Number(scheduledInstanceId))?.runner || {};
            const current = (status.numberOfRestarts || 0) + 1;
            const running = Number(run) === current && [2, 4].includes(status.status);
            const crashed = dep?.entity.name === "billing-cron";
            const started = Date.now() - (current - Number(run) + 1) * 6 * MIN;
            return rt("DeploymentRunReport", {
                deploymentId: dep?.id, deploymentVersion: Number(inst?.entity.deployment?.version || 1), deploymentSpecVersion: Number(dep?.meta.specVersion || 1),
                nodeId: Number(inst?.entity.nodeId || 1), instanceOrdinal: 0, run: Number(run), running, startedAt: new Date(started),
                ...(running ? {} : {stoppedAt: new Date(started + 5 * MIN), exitCode: crashed ? 1 : 0}),
                status: running ? 2 : crashed ? 5 : 3,
                logLines: crashed
                    ? ["level=INFO msg=\"billing run starting\" period=2026-11", "level=INFO msg=\"loaded 1,284 subscriptions\"", "level=ERROR msg=\"stripe: invalid API key provided\" code=401", "panic: billing: cannot continue without payment provider", "", "goroutine 1 [running]:", "main.main()", "\t/app/cmd/billing/main.go:88 +0x1f4", "exit status 1"]
                    : ["level=INFO msg=\"listening\" addr=:8080", "level=INFO msg=\"connected to database\"", "level=INFO msg=\"ready\""],
                warnings: [],
            });
        },
        async postV1DeploymentsLogQuery(request) { await sleep(180); return buildLogs(store, request); },
        async postV1MetricsLatest() { await sleep(150); return buildLatest(store); },
        async postV1MetricsQuery(request) { await sleep(220); return buildMetrics(store, request); },
        async postV1NixStoreReset() { await sleep(300); },
        async postV1GlobalExportedConfig() { return rt("ExportedConfigBlob", {blob: bytesOf("export", 2048)}); },

        async postV1SecretsCreate(request) { return secrets.create(request).update; },
        async postV1SecretsGenerate(request) { return secrets.create(request).update; },
        async postV1SecretsSet({secretId, value, updateReferencingDeployments}) {
            const update = secrets.set(Number(secretId), null, updateReferencingDeployments, "secret");
            secretValues.set(`${secretId}:${store.latest(T.SECRET, Number(secretId)).meta.valueVersion}`, new TextDecoder().decode(value || new Uint8Array()));
            return update;
        },
        async postV1SecretsRename({secretId, newKey}) { return secrets.rename(Number(secretId), newKey); },
        async postV1SecretsMove({secretId, valueDirectoryId, spaceId}) { return secrets.move(Number(secretId), valueDirectoryId, spaceId); },
        async postV1SecretsDelete({secretId}) { secrets.remove(Number(secretId)); },
        async postV1SecretsReveal({secretId, version}) {
            await sleep(150);
            const value = secretValues.get(`${secretId}:${version}`) ?? revealDefault(Number(secretId), Number(version));
            return rt("SecretRevealResponse", {value: new TextEncoder().encode(value)});
        },
        async postV1SecretsRotateRecoveryCode() {
            await sleep(400);
            store.publishSidecar("secretsStatus", {unlocked: true, recoveryConfigured: true});
            return rt("SecretRecoveryCodeResponse", {code: "orbit candle maple vessel harbor crisp lantern quiet velvet ember glide summit"});
        },
        async postV1SecretsUnlock() {
            store.publishSidecar("secretsStatus", {unlocked: true, recoveryConfigured: true});
            return rt("SecretsStatusResponse", {unlocked: true, recoveryConfigured: true});
        },
        async postV1ConfigsCreate(request) { return configs.create(request, request.value).update; },
        async postV1ConfigsSet({configId, value, updateReferencingDeployments}) { return configs.set(Number(configId), value, updateReferencingDeployments, "config"); },
        async postV1ConfigsRename({configId, newKey}) { return configs.rename(Number(configId), newKey); },
        async postV1ConfigsMove({configId, valueDirectoryId, spaceId}) { return configs.move(Number(configId), valueDirectoryId, spaceId); },
        async postV1ConfigsDelete({configId}) { configs.remove(Number(configId)); },
        async postV1ValueDirectoriesCreate(request) { return valueDirs.create(request); },
        async postV1ValueDirectoriesRename(request) { return valueDirs.rename(request); },
        async postV1ValueDirectoriesMove(request) { return valueDirs.move(request); },
        async postV1ValueDirectoriesDelete(request) { valueDirs.remove(request); },

        async postV1AssetsRename({assetId, newKey}) { return assets.rename(Number(assetId), newKey); },
        async postV1AssetsMove({assetId, assetDirectoryId, spaceId}) { return assets.move(Number(assetId), assetDirectoryId, spaceId); },
        async postV1AssetsDelete({assetId}) { assets.remove(Number(assetId)); },
        async postV1AssetDirectoriesCreate(request) { return assetDirs.create(request); },
        async postV1AssetDirectoriesRename(request) { return assetDirs.rename(request); },
        async postV1AssetDirectoriesMove(request) { return assetDirs.move(request); },
        async postV1AssetDirectoriesDelete(request) { assetDirs.remove(request); },

        async postV1NetworkPoliciesCreate({action, source, destination, ports}) {
            const id = store.nextId(T.NETWORK_POLICY);
            return store.commit(SIGNED_IN_USER, [{op: "create", type: T.NETWORK_POLICY, id, entity: {id, action: action || 1, source, destination, ports: ports || []}}]);
        },
        async postV1NetworkPoliciesUpdate({id, action, source, destination, ports}) {
            return store.commit(SIGNED_IN_USER, [{op: "update", type: T.NETWORK_POLICY, id: Number(id), entity: {id: Number(id), action: action || 1, source, destination, ports: ports || []}}]);
        },
        async postV1NetworkPoliciesDelete({id}) { store.commit(SIGNED_IN_USER, [{op: "delete", type: T.NETWORK_POLICY, id: Number(id)}]); },
        async postV1SpaceDomainPoliciesCreate({name, disabled, rule}) {
            const id = store.nextId(T.SPACE_DOMAIN_POLICY);
            return store.commit(SIGNED_IN_USER, [{op: "create", type: T.SPACE_DOMAIN_POLICY, id, entity: {id, name, disabled, rule}}]);
        },
        async postV1SpaceDomainPoliciesUpdate({id, name, disabled, rule}) {
            await sleep(200);
            return store.commit(SIGNED_IN_USER, [{op: "update", type: T.SPACE_DOMAIN_POLICY, id: Number(id), entity: {id: Number(id), name, disabled, rule}}]);
        },
        async postV1SpaceDomainPoliciesDelete({id}) { store.commit(SIGNED_IN_USER, [{op: "delete", type: T.SPACE_DOMAIN_POLICY, id: Number(id)}]); },

        async postV1NodeAccessPoliciesCreate({name, disabled, nodes, rule}) {
            const id = store.nextId(T.NODE_ACCESS_POLICY);
            const entity = {id, name, disabled: Boolean(disabled), builtin: 0, nodes, rule};
            checkNodeAccessWrite({op: "create", id, policy: entity});
            return store.commit(SIGNED_IN_USER, [{op: "create", type: T.NODE_ACCESS_POLICY, id, entity}]);
        },
        async postV1NodeAccessPoliciesUpdate({id, name, disabled, nodes, rule}) {
            const builtin = store.get(T.NODE_ACCESS_POLICY, Number(id))?.builtin || 0;
            const entity = {id: Number(id), name, disabled: Boolean(disabled), builtin, nodes, rule};
            checkNodeAccessWrite({op: "update", id: Number(id), policy: entity});
            return store.commit(SIGNED_IN_USER, [{op: "update", type: T.NODE_ACCESS_POLICY, id: Number(id), entity}]);
        },
        async postV1NodeAccessPoliciesDelete({id}) {
            checkNodeAccessWrite({op: "delete", id: Number(id)});
            store.commit(SIGNED_IN_USER, [{op: "delete", type: T.NODE_ACCESS_POLICY, id: Number(id)}]);
        },

        async postV1SpacesCreate({name}) {
            if (store.all(T.SPACE).some((s) => s.entity.name === name)) throw refuse(409, `A space named ${name} already exists`);
            const id = store.nextId(T.SPACE);
            store.commit(SIGNED_IN_USER, [{op: "create", type: T.SPACE, id, entity: {id, name}}]);
            return rt("Space", {id, name});
        },
        async postV1SpacesUpdate({id, name}) {
            store.commit(SIGNED_IN_USER, [{op: "update", type: T.SPACE, id: Number(id), entity: {id: Number(id), name}}]);
            return rt("Space", {id: Number(id), name});
        },
        async postV1SpacesDelete({id}) {
            if (store.all(T.DEPLOYMENT).some((d) => Number(d.entity.spaceId) === Number(id))) throw refuse(409, "The space still holds deployments");
            store.commit(SIGNED_IN_USER, [{op: "delete", type: T.SPACE, id: Number(id)}]);
        },

        async getV1NodesEnrollmentsInfo() { return rt("NodeEnrollmentInfo", {enrollmentTlsSpkiSha256: "4f2a9c1e7b3d5a8f0c6e2b9d1a7f3c5e8b0d2f4a6c8e0b1d3f5a7c9e1b3d5f7a"}); },
        async postV1NodesRename({identifier, name}) { return updateNode(identifier, (n) => ({...n, operator: {...n.operator, name}})); },
        async postV1NodesDrain({identifier, draining}) { return updateNode(identifier, (n) => ({...n, status: draining ? 6 : 4})); },
        async postV1NodesEvict({identifier}) {
            const item = nodeByIdentifier(identifier);
            const update = updateNode(identifier, (n) => ({...n, status: 8}));
            store.commit(0, [{op: "update", type: T.NODE_STATUS, id: item.id, entity: {...store.get(T.NODE_STATUS, item.id), updatedAt: new Date(), isConnected: false}}]);
            return update;
        },
        async postV1NodesExposure({identifier}) {
            const item = nodeByIdentifier(identifier);
            const held = store.all(T.DEPLOYMENT).filter((d) => Number(d.entity.scheduling?.placement?.value?.dedicatedNodes?.nodes?.[0]) === item.id);
            return rt("NodeExposure", {
                nodeId: item.id,
                deployments: held.map((d) => ({id: d.id, name: d.entity.name, spaceId: d.entity.spaceId, version: d.meta.version})),
                secrets: [{id: 1, name: "postgres_password", spaceId: SPACE.prod, version: 3}],
                configs: [{id: 1, name: "feature_flags.json", spaceId: SPACE.prod, version: 3}],
                acmeHostnames: ["api.example.com"], githubToken: true,
            });
        },
        async postV1NodesEnrollmentsAccept({id, nodeName}) {
            const item = store.latest(T.NODE, Number(id));
            if (!item) throw refuse(404, "Enrollment request not found");
            const entity = {...clone(item.entity), status: 4, operator: {...item.entity.operator, name: nodeName, enrolledTime: new Date()}};
            delete entity.enrollmentRequestedAt;
            store.commit(SIGNED_IN_USER, [{op: "update", type: T.NODE, id: item.id, entity}]);
            return rt("EnrollmentRequestStatus", {id: item.id, createdAt: new Date(), requestingMachineId: entity.reported.identifier, opendeployVersion: AGENT_VERSION, status: 4, isConnected: true});
        },

        async postV1ClusterSettingsUpdate(settings) {
            const current = store.get(T.SYSTEM_CONFIG, 1);
            store.commit(SIGNED_IN_USER, [{op: "update", type: T.SYSTEM_CONFIG, id: 1, entity: {...current, settings}}]);
            return rt("ClusterSettings", settings);
        },
    };

    const assetVersion = (assetId, version) => {
        const history = store.history(T.ASSET, Number(assetId));
        return history.find((h) => Number(h.meta.valueVersion) === Number(version)) || history[history.length - 1];
    };

    installFetch(async (url, init) => {
        const u = new URL(url, location.origin);
        if (u.pathname === "/v1/assets/content") {
            const version = assetVersion(u.searchParams.get("asset_id"), u.searchParams.get("version"));
            const bytes = assetContent.get(`${version?.id}:${version?.meta.valueVersion}`) || bytesOf(`asset-${version?.id}`, Math.min(4096, Number(version?.entity.sizeBytes || 64)));
            return new Response(bytes, {status: 200, headers: {"Content-Type": "application/octet-stream"}});
        }
        if (u.pathname === "/v1/tls/ca.crt") {
            return new Response(dataset.rows.length ? "-----BEGIN CERTIFICATE-----\nMIIBszCCAVmgAwIBAgIUFixtureLocalWebUiCA\n-----END CERTIFICATE-----\n" : "", {status: 200});
        }
        if (u.pathname === "/v1/assets/upload") {
            const body = new Uint8Array(await new Response(init?.body).arrayBuffer());
            return new Response(await upload(u.searchParams, body), {status: 200});
        }
        return null;
    }, log);

    const upload = async (params, bytes) => {
        const key = params.get("key");
        const spaceId = Number(params.get("space_id") || 0);
        const directoryId = Number(params.get("directory_id") || 0);
        const existing = store.all(T.ASSET).find((a) => a.entity.fs.key === key && Number(a.entity.spaceId) === spaceId && Number(a.entity.fs.directoryId || 0) === directoryId);
        const id = existing ? existing.id : store.nextId(T.ASSET);
        const sha256 = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
        const entity = {id, fs: {key, ...(directoryId ? {directoryId} : {})}, spaceId, sizeBytes: bytes.length, sha256, storageKey: `a/${commitSha(`up${id}${Date.now()}`).slice(0, 32)}`};
        const update = store.commit(SIGNED_IN_USER, [{op: existing ? "update" : "create", type: T.ASSET, id, entity, valueChanged: true}]);
        assetContent.set(`${id}:${store.latest(T.ASSET, id).meta.valueVersion}`, bytes);
        return model.encodeCoreWriteUpdate(update);
    };
    installXhr(upload);

    const unmocked = new Set();
    for (const name of Object.getOwnPropertyNames(Capi.prototype)) {
        if (name === "constructor" || typeof Capi.prototype[name] !== "function") continue;
        if (handlers[name]) {
            capi[name] = handlers[name];
            continue;
        }
        capi[name] = (...args) => {
            const message = `not mocked in fixture: ${name}`;
            if (!unmocked.has(name)) log.error(message, args[0]);
            unmocked.add(name);
            return Promise.reject(new Error(message));
        };
    }

    setInterval(() => {
        const crash = dataset.instances.find((i) => i.deploymentId === 5);
        const current = crash && store.get(T.SCHEDULED_INSTANCE_STATUS, crash.id);
        if (!current?.runner) return;
        const restarting = current.runner.status === 5;
        const runner = {...current.runner, status: restarting ? 4 : 5, ...(restarting ? {numberOfRestarts: current.runner.numberOfRestarts + 1, lastRestartAt: new Date()} : {exitCode: 1})};
        const nodeStatus = store.get(T.NODE_STATUS, NODE.a);
        store.commit(0, [
            {op: "update", type: T.SCHEDULED_INSTANCE_STATUS, id: crash.id, entity: {...current, updatedAt: new Date(), runner}},
            {op: "update", type: T.NODE_STATUS, id: NODE.a, entity: {...nodeStatus, updatedAt: new Date()}},
        ]);
        store.publishSidecar("backupStatus", {...store.sidecars.backupStatus, localTxid: store.seq + 180000, remoteTxid: store.seq + 180000, inSync: true, lastSuccessfulSyncAt: new Date()});
    }, 20000);

    return {store, unmocked};
}

function installFetch(handle, log) {
    const realFetch = window.fetch.bind(window);
    window.fetch = async (input, init) => {
        const url = typeof input === "string" ? input : input?.url || String(input);
        const path = new URL(url, location.origin).pathname;
        if (!path.startsWith("/v1/")) return realFetch(input, init);
        const response = await handle(url, init);
        if (response) return response;
        const message = `not mocked in fixture: fetch ${path}`;
        log.error(message);
        throw new Error(message);
    };
}

function installXhr(upload) {
    const RealXHR = window.XMLHttpRequest;
    window.XMLHttpRequest = class FixtureXHR extends RealXHR {
        open(method, url, ...rest) {
            this.fixtureUrl = new URL(url, location.origin);
            if (!this.fixtureUrl.pathname.startsWith("/v1/")) return super.open(method, url, ...rest);
        }
        setRequestHeader(name, value) {
            if (!this.fixtureUrl?.pathname.startsWith("/v1/")) super.setRequestHeader(name, value);
        }
        send(body) {
            if (!this.fixtureUrl?.pathname.startsWith("/v1/")) return super.send(body);
            (async () => {
                const bytes = new Uint8Array(await new Response(body).arrayBuffer());
                for (let loaded = 0; loaded < bytes.length; loaded += Math.max(1, Math.ceil(bytes.length / 5))) {
                    this.upload.onprogress?.({loaded, total: bytes.length, lengthComputable: true});
                    await sleep(120);
                }
                const encoded = this.fixtureUrl.pathname === "/v1/assets/upload" ? await upload(this.fixtureUrl.searchParams, bytes) : null;
                Object.defineProperty(this, "status", {value: encoded ? 200 : 404});
                Object.defineProperty(this, "response", {value: encoded ? encoded.buffer : new ArrayBuffer(0)});
                this.onload?.();
            })();
        }
    };
}
