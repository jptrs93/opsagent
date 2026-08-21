// Deterministic virtual log store: ~1.3M structured events over 48 hours.
// Nothing is materialized up front — every event derives from its row index
// through an integer hash, so the dataset is free until a row is looked at
// and the same index always yields the same event. The rate varies across
// hand-placed segments so the histogram has a deploy gap and an error storm
// to find.

const MIN = 60_000;
const HOUR = 3_600_000;

// ---------------------------------------------------------------------------
// Deterministic per-index randomness.
// ---------------------------------------------------------------------------

function hash32(i, salt) {
    let h = Math.imul(i ^ 0x9e3779b1, 0x85ebca77) ^ Math.imul(salt + 1, 0xc2b2ae3d);
    h = Math.imul(h ^ (h >>> 15), 0x2c1b3c6d);
    h = Math.imul(h ^ (h >>> 12), 0x297a2d39);
    h ^= h >>> 15;
    return h >>> 0;
}

const rnd = (i, salt) => hash32(i, salt) / 4294967296;
const pick = (arr, r) => arr[Math.min(arr.length - 1, Math.floor(r * arr.length))];
const hex = (i, salt, len) => hash32(i, salt).toString(16).padStart(8, '0').slice(0, len);

function weighted(pairs, r) {
    let acc = 0;
    for (const [value, weight] of pairs) {
        acc += weight;
        if (r < acc) return value;
    }
    return pairs[pairs.length - 1][0];
}

// ---------------------------------------------------------------------------
// Rate segments. Chronological, oldest first; durations sum to ~48h.
// ---------------------------------------------------------------------------

const SEGMENTS = [
    {ms: 17.5 * HOUR, count: 420_000, mix: 'normal'},
    {ms: 0.5 * HOUR, count: 30_000, mix: 'warnSpike'},   // disk pressure on node-3
    {ms: 3.95 * HOUR, count: 95_000, mix: 'normal'},
    {ms: 3 * MIN, count: 0, mix: 'normal'},              // deploy restart gap
    {ms: 20.7 * HOUR, count: 500_000, mix: 'normal'},
    {ms: 20 * MIN, count: 120_000, mix: 'storm'},        // postgres error storm
    {ms: 5 * HOUR, count: 130_000, mix: 'normal'},
];

const TOTAL = SEGMENTS.reduce((n, s) => n + s.count, 0);
const SPAN_MS = SEGMENTS.reduce((n, s) => n + s.ms, 0);
const END_TS = Date.now();
const START_TS = END_TS - SPAN_MS;

// Absolute per-segment layout: start timestamp, first index, event spacing.
let cumCount = 0;
let cumMs = 0;
for (const seg of SEGMENTS) {
    seg.firstIdx = cumCount;
    seg.startTs = START_TS + cumMs;
    seg.spacing = seg.count > 0 ? seg.ms / seg.count : 0;
    cumCount += seg.count;
    cumMs += seg.ms;
}

const DEPLOY_TS = SEGMENTS[3].startTs;

function segmentOf(i) {
    for (let s = SEGMENTS.length - 1; s >= 0; s--) {
        if (i >= SEGMENTS[s].firstIdx && SEGMENTS[s].count > 0) return SEGMENTS[s];
    }
    return SEGMENTS[0];
}

function eventTs(i) {
    const seg = segmentOf(i);
    const k = i - seg.firstIdx;
    // Jitter stays under one spacing so timestamps remain monotonic.
    return Math.floor(seg.startTs + k * seg.spacing + rnd(i, 1) * seg.spacing * 0.9);
}

// First index with eventTs(i) >= ts.
function lowerBound(ts) {
    let lo = 0, hi = TOTAL;
    while (lo < hi) {
        const mid = (lo + hi) >> 1;
        if (eventTs(mid) < ts) lo = mid + 1;
        else hi = mid;
    }
    return lo;
}

// ---------------------------------------------------------------------------
// Event shape. Levels/services vary by segment mix.
// ---------------------------------------------------------------------------

export const LEVELS = ['ERROR', 'WARN', 'INFO', 'DEBUG'];

const LEVEL_MIX = {
    normal: [['DEBUG', 0.16], ['INFO', 0.74], ['WARN', 0.08], ['ERROR', 0.02]],
    warnSpike: [['DEBUG', 0.06], ['INFO', 0.50], ['WARN', 0.38], ['ERROR', 0.06]],
    storm: [['DEBUG', 0.03], ['INFO', 0.30], ['WARN', 0.22], ['ERROR', 0.45]],
};

const SERVICE_MIX = {
    normal: [['api', 0.34], ['worker', 0.20], ['ingress', 0.16], ['netproxy', 0.12], ['postgres', 0.10], ['scheduler', 0.08]],
    warnSpike: [['worker', 0.55], ['api', 0.15], ['ingress', 0.10], ['netproxy', 0.08], ['postgres', 0.07], ['scheduler', 0.05]],
    storm: [['api', 0.50], ['ingress', 0.22], ['postgres', 0.14], ['worker', 0.06], ['netproxy', 0.05], ['scheduler', 0.03]],
};

const SERVICE_HOSTS = {
    api: [['node-1', 0.5], ['node-2', 0.5]],
    worker: [['node-2', 0.4], ['node-3', 0.6]],
    ingress: [['node-1', 1]],
    netproxy: [['node-1', 0.34], ['node-2', 0.33], ['node-3', 0.33]],
    postgres: [['node-3', 1]],
    scheduler: [['node-1', 1]],
};

const LOGGERS = {
    api: ['http', 'db', 'auth', 'cache'],
    worker: ['jobs', 'assets', 'gc'],
    ingress: ['proxy', 'tls'],
    netproxy: ['dns', 'netmap'],
    postgres: ['wal', 'planner', 'autovacuum'],
    scheduler: ['reconcile', 'cron'],
};

const PATHS = ['/v1/deployments', '/v1/deployments/logsearch', '/v1/spaces', '/v1/assets/upload',
    '/v1/secrets', '/v1/machines', '/v1/sessions', '/app', '/v1/events', '/v1/deployments/deploy'];
const METHODS = [['GET', 0.62], ['POST', 0.28], ['PUT', 0.06], ['DELETE', 0.04]];
const USERS = ['joss', 'deploy-bot', 'agent-7'];
const JOBS = ['asset-sync', 'log-compact', 'backup', 'image-pull', 'gc'];
const NAMES = ['api.space-1', 'grafana.space-2', 'pgbouncer.space-1', 'webhookd.space-3', 'cache.space-2'];

function eventLite(i) {
    const seg = segmentOf(i);
    const level = weighted(LEVEL_MIX[seg.mix], rnd(i, 2));
    const service = weighted(SERVICE_MIX[seg.mix], rnd(i, 3));
    const host = weighted(SERVICE_HOSTS[service], rnd(i, 4));
    return {ts: eventTs(i), level, service, host, mix: seg.mix};
}

// Message + structured extras per (service, level). `storm` biases api/ingress/
// postgres errors toward the one incident so the burst reads as a single cause.
function build(i, lite) {
    const {service, level, mix} = lite;
    const r = rnd(i, 7);
    const storm = mix === 'storm';
    const ip = `203.0.113.${1 + (hash32(i, 9) % 250)}`;
    const trace = () => `${hex(i, 11, 8)}${hex(i, 12, 8)}`;
    const dur = (lo, hi) => lo + (hash32(i, 13) % (hi - lo));
    const user = rnd(i, 14) < 0.3 ? pick(USERS, rnd(i, 15)) : undefined;

    if (service === 'api') {
        if (level === 'ERROR') {
            if (storm || r < 0.34) return {
                msg: `upstream timeout after 5000ms calling postgres at 10.32.0.5:5432 (retries exhausted, circuit half-open)`,
                extra: {err: 'timeout', method: weighted(METHODS, rnd(i, 16)), path: pick(PATHS, rnd(i, 17)), status: 503, duration_ms: dur(5000, 5400), trace_id: trace(), user},
            };
            if (r < 0.67) return {
                msg: `failed to acquire db connection: pool exhausted (32 in use, waited ${dur(900, 4800)}ms); request aborted before handler ran`,
                extra: {err: 'pool_exhausted', path: pick(PATHS, rnd(i, 17)), status: 500, trace_id: trace(), user},
            };
            return {
                msg: `panic recovered in handler: runtime error: invalid memory address or nil pointer dereference [recovered] goroutine=${dur(100, 9000)} — deployment config was nil after concurrent delete, returning 500 and rolling the request back`,
                extra: {err: 'panic', path: pick(PATHS, rnd(i, 17)), status: 500, trace_id: trace()},
            };
        }
        if (level === 'WARN') {
            if (r < 0.4) return {
                msg: `slow query took ${dur(800, 3800)}ms: SELECT d.id, d.config, d.state FROM deployments d JOIN events e ON e.deployment_id = d.id WHERE e.seq > $1 ORDER BY e.seq LIMIT 500`,
                extra: {duration_ms: dur(800, 3800), trace_id: trace()},
            };
            if (r < 0.7) return {
                msg: `retrying webhook delivery attempt=${2 + (hash32(i, 18) % 3)} endpoint=hooks.example.com`,
                extra: {err: 'retry', trace_id: trace()},
            };
            return {
                msg: `request rate limited`,
                extra: {status: 429, path: pick(PATHS, rnd(i, 17)), user: user || 'agent-7', trace_id: trace()},
            };
        }
        if (level === 'DEBUG') {
            return r < 0.5
                ? {msg: `cache miss key=deployment:${hash32(i, 19) % 300}`, extra: {}}
                : {msg: `token verified aud=webui`, extra: {user}};
        }
        const method = weighted(METHODS, rnd(i, 16));
        const path = pick(PATHS, rnd(i, 17));
        const status = rnd(i, 20) < 0.94 ? 200 : 204;
        const d = dur(2, 180);
        return {
            msg: `${method} ${path} ${status} ${d}ms`,
            extra: {method, path, status, duration_ms: d, trace_id: trace(), user},
        };
    }

    if (service === 'worker') {
        const job = pick(JOBS, rnd(i, 21));
        if (level === 'ERROR') return {
            msg: `job=${job} failed: ${storm ? 'context deadline exceeded reading postgres' : pick(['checksum mismatch on release archive', 'no space left on device', 'registry returned 429'], r)}`,
            extra: {job, err: storm ? 'timeout' : 'job_failed'},
        };
        if (level === 'WARN') {
            if (mix === 'warnSpike' || r < 0.5) return {
                msg: `disk usage ${88 + (hash32(i, 22) % 9)}% on /var/lib/opendeploy/logs — retention sweep behind`,
                extra: {disk_pct: 88 + (hash32(i, 22) % 9)},
            };
            return {msg: `job=${job} retry attempt=${2 + (hash32(i, 18) % 2)}`, extra: {job, err: 'retry'}};
        }
        if (level === 'DEBUG') return {msg: `queue depth=${hash32(i, 23) % 40}`, extra: {queue: 'default'}};
        return r < 0.5
            ? {msg: `job=${job} started`, extra: {job}}
            : {msg: `job=${job} finished in ${(dur(200, 9200) / 1000).toFixed(1)}s`, extra: {job, duration_ms: dur(200, 9200)}};
    }

    if (service === 'ingress') {
        const method = weighted(METHODS, rnd(i, 16));
        const path = pick(PATHS, rnd(i, 17));
        if (level === 'ERROR') {
            if (storm) return {
                msg: `no healthy upstream for api.space-1 — all replicas failing readiness, returning 503`,
                extra: {err: 'no_upstream', status: 503, path, trace_id: trace()},
            };
            return {
                msg: `TLS handshake error from ${ip}: remote error: tls: unknown certificate`,
                extra: {err: 'tls_handshake'},
            };
        }
        if (level === 'WARN') return {
            msg: `upstream 502 for ${path}, retrying second replica`,
            extra: {status: 502, path, trace_id: trace()},
        };
        if (level === 'DEBUG') return {msg: `sni route ${pick(NAMES, rnd(i, 24))} → space-${1 + (hash32(i, 25) % 3)}`, extra: {}};
        const status = rnd(i, 20) < 0.96 ? 200 : 304;
        const d = dur(1, 220);
        return {
            msg: `${ip} "${method} ${path} HTTP/2" ${status} ${(hash32(i, 26) % 90) / 10}k ${d}ms`,
            extra: {method, path, status, duration_ms: d, trace_id: trace()},
        };
    }

    if (service === 'netproxy') {
        const name = pick(NAMES, rnd(i, 24));
        if (level === 'ERROR') return {msg: `dns upstream unreachable, serving stale answers`, extra: {err: 'dns_upstream'}};
        if (level === 'WARN') return {
            msg: `netmap seq regression (got ${1000 + (hash32(i, 27) % 50)}, have ${1051 + (hash32(i, 27) % 9)}) — ignored`,
            extra: {},
        };
        if (level === 'DEBUG') return {msg: `cache hit ${name}.internal`, extra: {}};
        return r < 0.5
            ? {msg: `resolved ${name}.internal → fd7a:115c:a1e0::${hex(i, 28, 4)}`, extra: {}}
            : {msg: `netmap applied seq=${1700000000 + hash32(i, 27) % 90000}`, extra: {}};
    }

    if (service === 'postgres') {
        if (level === 'ERROR') {
            if (storm) return {msg: `FATAL: sorry, too many clients already`, extra: {err: 'too_many_connections'}};
            return {msg: `deadlock detected pid=${2000 + hash32(i, 29) % 800}; rolling back`, extra: {err: 'deadlock'}};
        }
        if (level === 'WARN') return {
            msg: `long running transaction pid=${2000 + hash32(i, 29) % 800} duration=${dur(30, 400)}s`,
            extra: {duration_ms: dur(30, 400) * 1000},
        };
        if (level === 'DEBUG') return {msg: `autovacuum "events": removed ${hash32(i, 30) % 4000} rows`, extra: {}};
        return {msg: `checkpoint complete: wrote ${hash32(i, 30) % 9000} buffers (${(rnd(i, 31) * 4).toFixed(1)}%)`, extra: {}};
    }

    // scheduler
    if (level === 'ERROR') return {
        msg: `failed to reach worker node-2: context deadline exceeded`,
        extra: {err: 'timeout'},
    };
    if (level === 'WARN') return {msg: `reconcile lag ${dur(4, 40)}s behind schedule`, extra: {}};
    if (level === 'DEBUG') return {msg: `cron fire backup@daily`, extra: {job: 'backup'}};
    return {msg: `reconcile tick: ${hash32(i, 32) % 40} deployments checked, ${hash32(i, 33) % 4} actions`, extra: {}};
}

const fullCache = new Map();

function eventFull(i) {
    const cached = fullCache.get(i);
    if (cached) return cached;
    const lite = eventLite(i);
    const {msg, extra} = build(i, lite);
    const record = {
        idx: i,
        ts: lite.ts,
        level: lite.level,
        service: lite.service,
        host: lite.host,
        version: lite.ts < DEPLOY_TS ? 'v0.0.506' : 'v0.0.507',
        logger: pick(LOGGERS[lite.service], rnd(i, 5)),
        msg,
        extra,
    };
    // Drop undefined extras so the record and its JSON stay clean.
    for (const key of Object.keys(record.extra)) {
        if (record.extra[key] === undefined) delete record.extra[key];
    }
    if (fullCache.size > 2000) fullCache.clear();
    fullCache.set(i, record);
    return record;
}

export function recordField(record, key) {
    if (key === 'time') return record.ts;
    if (key === 'msg') return record.msg;
    if (key in record && key !== 'extra' && key !== 'idx') return record[key];
    return record.extra[key];
}

export function recordJson(record) {
    return JSON.stringify({
        timestamp: new Date(record.ts).toISOString(),
        level: record.level,
        service: record.service,
        host: record.host,
        version: record.version,
        logger: record.logger,
        ...record.extra,
        message: record.msg,
    }, null, 2);
}

// Fields promotable to columns / listed in the sidebar (msg and time are core).
export const FIELD_KEYS = ['level', 'service', 'host', 'version', 'logger', 'method', 'path',
    'status', 'duration_ms', 'trace_id', 'job', 'user', 'err'];

// ---------------------------------------------------------------------------
// Query execution: chunked scan over the index range so the UI stays live.
// ---------------------------------------------------------------------------

const LEVEL_POS = {ERROR: 0, WARN: 1, INFO: 2, DEBUG: 3};
const CHEAP_KEYS = new Set(['level', 'service', 'host']);

function matchToken(token, lite, full) {
    if (token.type === 'text') {
        return full().msg.toLowerCase().includes(token.value);
    }
    const raw = CHEAP_KEYS.has(token.key) ? lite[token.key] : recordField(full(), token.key);
    if (token.value === '*') return raw !== undefined && raw !== '';
    if (raw === undefined) return false;
    return String(raw).toLowerCase() === token.value;
}

async function runQuery({startTs, endTs, tokens = [], levels, scope, bucketN = 90, displayCap = 200_000, isCancelled = () => false, onProgress = () => {}}) {
    const t0 = performance.now();
    const lo = lowerBound(startTs);
    const hi = lowerBound(endTs + 1);
    const bucketMs = Math.max(1, (endTs - startTs) / bucketN);
    const counts = new Uint32Array(bucketN * 4);
    const idxs = [];
    // Cheap tokens (level/service/host, negations included) run before any
    // message string is built; text and field tokens only see survivors.
    const cheap = tokens.filter(t => t.type === 'pair' && CHEAP_KEYS.has(t.key));
    const costly = tokens.filter(t => !cheap.includes(t));

    const CHUNK = 150_000;
    for (let base = lo; base < hi; base += CHUNK) {
        if (isCancelled()) return null;
        const end = Math.min(hi, base + CHUNK);
        for (let i = base; i < end; i++) {
            const lite = eventLite(i);
            if (levels && !levels.has(lite.level)) continue;
            if (scope && lite.service !== scope) continue;
            let ok = true;
            for (const token of cheap) {
                if (matchToken(token, lite) === token.neg) { ok = false; break; }
            }
            if (!ok) continue;
            if (costly.length) {
                let full = null;
                const getFull = () => full || (full = eventFull(i));
                for (const token of costly) {
                    if (matchToken(token, lite, getFull) === token.neg) { ok = false; break; }
                }
                if (!ok) continue;
            }
            const bucket = Math.min(bucketN - 1, Math.floor((lite.ts - startTs) / bucketMs));
            counts[bucket * 4 + LEVEL_POS[lite.level]]++;
            idxs.push(i);
        }
        onProgress((end - lo) / Math.max(1, hi - lo));
        await new Promise(resolve => setTimeout(resolve, 0));
    }

    const capped = idxs.length > displayCap;
    return {
        idxs: capped ? idxs.slice(idxs.length - displayCap) : idxs,
        total: idxs.length,
        scanned: hi - lo,
        counts, bucketN, bucketMs, startTs, endTs,
        capped,
        tookMs: Math.round(performance.now() - t0),
    };
}

// Top values for one field over the newest part of the result set.
function fieldStats(idxs, key, sampleN = 6000) {
    const step = Math.max(1, Math.floor(idxs.length / sampleN));
    const counts = new Map();
    let present = 0, sampled = 0;
    for (let k = idxs.length - 1; k >= 0; k -= step) {
        sampled++;
        const value = recordField(eventFull(idxs[k]), key);
        if (value === undefined || value === '') continue;
        present++;
        const text = String(value);
        counts.set(text, (counts.get(text) || 0) + 1);
    }
    const top = [...counts.entries()]
        .sort((a, b) => b[1] - a[1])
        .slice(0, 5)
        .map(([value, count]) => ({value, count, frac: count / Math.max(1, present)}));
    return {coverage: sampled ? present / sampled : 0, distinct: counts.size, top, sampled};
}

export const store = {
    total: TOTAL,
    startTs: START_TS,
    endTs: END_TS,
    eventTs,
    eventLite,
    eventFull,
    lowerBound,
    runQuery,
    fieldStats,
};
