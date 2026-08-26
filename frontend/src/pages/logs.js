import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {
    caretRightIcon,
    checkIcon,
    chevronDownIcon,
    columnsIcon,
    copyIcon,
    searchIcon,
    xIcon,
} from "../lib/icons.js";
import {loginS} from "../state/login.js";
import {deploymentsS, machinesS} from "../state/deployments.js";
import {nodeDisplayName} from "../lib/machines.js";
import {logScopePicker} from "../components/logScopePicker.js";
import {spacesFilter} from "../components/spacesFilter.js";

const {button, div, input, option, p, pre, select, span} = van.tags;
const {svg: svgEl, rect, line: svgLine} = van.tags("http://www.w3.org/2000/svg");

const SYSTEM_SPACE_ID = 0;
const SYSTEM_DEPLOYMENT_NAME = 'opendeploy';
const LOG_LINE_LIMIT = 5000;
const HISTOGRAM_BUCKETS = 90;
const MIN = 60_000;
const HOUR = 3_600_000;
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const pad2 = (n) => String(n).padStart(2, '0');

// The last entry ('') collects records with no parsed level.
const LEVELS = ['ERROR', 'WARN', 'INFO', 'DEBUG', ''];
const NL = LEVELS.length;

// Histogram fills validated for CVD separation and contrast on this surface
// (dataviz six-check validator); row/legend text stays on text tokens.
const LEVEL_META = {
    ERROR: {fill: '#c42121', text: 'text-red-400', label: 'ERROR'},
    WARN: {fill: '#c67b04', text: 'text-amber-400', label: 'WARN'},
    INFO: {fill: '#3b82f6', text: 'text-blue-400', label: 'INFO'},
    DEBUG: {fill: '#0e9488', text: 'text-teal-500', label: 'DEBUG'},
    '': {fill: '#6b7280', text: 'text-gray-400', label: 'none'},
};
const levelMeta = (level) => LEVEL_META[level] || LEVEL_META[''];

// Width hints for well-known fields; anything else gets a generic column.
const COLUMN_DEFS = {
    time: {label: 'time', px: 152},
    level: {label: 'level', px: 58},
    msg: {label: 'message', flex: true, minPx: 320, mono: true},
    service: {label: 'service', px: 90},
    host: {label: 'host', px: 74},
    version: {label: 'version', px: 76},
    node: {label: 'node', px: 52, num: true},
    run: {label: 'run', px: 46, num: true},
    instance: {label: 'inst', px: 46, num: true},
    stream: {label: 'stream', px: 60},
    logger: {label: 'logger', px: 100},
    method: {label: 'method', px: 68},
    path: {label: 'path', px: 170, mono: true},
    status: {label: 'status', px: 58, num: true},
    duration_ms: {label: 'duration', px: 78, num: true},
    trace_id: {label: 'trace', px: 136, mono: true},
    err: {label: 'err', px: 130, mono: true},
};

const META_FIELDS = new Set(['version', 'node', 'run', 'instance', 'stream']);

const DAY = 24 * HOUR;
// Ordered column-major for the two-column picker: minutes/hours down the left
// column, days down the right.
const PRESETS = [
    {key: '5m', label: 'Last 5 minutes', ms: 5 * MIN},
    {key: '15m', label: 'Last 15 minutes', ms: 15 * MIN},
    {key: '30m', label: 'Last 30 minutes', ms: 30 * MIN},
    {key: '1h', label: 'Last hour', ms: HOUR},
    {key: '3h', label: 'Last 3 hours', ms: 3 * HOUR},
    {key: '6h', label: 'Last 6 hours', ms: 6 * HOUR},
    {key: '12h', label: 'Last 12 hours', ms: 12 * HOUR},
    {key: '24h', label: 'Last 24 hours', ms: 24 * HOUR},
    {key: '2d', label: 'Last 2 days', ms: 2 * DAY},
    {key: '4d', label: 'Last 4 days', ms: 4 * DAY},
    {key: '7d', label: 'Last 7 days', ms: 7 * DAY},
    {key: '14d', label: 'Last 14 days', ms: 14 * DAY},
    {key: '21d', label: 'Last 21 days', ms: 21 * DAY},
    {key: '30d', label: 'Last 30 days', ms: 30 * DAY},
];
const DEFAULT_PRESET = '12h';

// A row is as tall as the lines its message occupies: leading per line plus the
// cell's py-[3px] and the row's border-b. Embedded newlines always count;
// wrapping adds the soft-wrapped lines on top. Either way a row runs from one
// line up to WRAP_MAX_LINES.
const LINE_H = 15;
const CELL_PAD_Y = 6;
const ROW_BORDER = 1;
const WRAP_MAX_LINES = 3;
const rowHeight = (lines) => lines * LINE_H + CELL_PAD_Y + ROW_BORDER;
const DETAIL_H = 320;
const HEADER_H = 25;
const OVERSCAN = 12;

const CONTEXT_WINDOW = 30 * MIN;
const CONTEXT_LIMIT = 100;

const HIDDEN_SPACES_KEY = 'opsagent_logs_hidden_spaces';

function loadHiddenSpaces() {
    try {
        const raw = localStorage.getItem(HIDDEN_SPACES_KEY);
        if (raw) return new Set(JSON.parse(raw).map(Number));
    } catch {}
    return new Set();
}

function saveHiddenSpaces(set) {
    try { localStorage.setItem(HIDDEN_SPACES_KEY, JSON.stringify([...set])); } catch {}
}

function fmtRowTime(ts) {
    const d = new Date(ts);
    return `${MONTHS[d.getMonth()]} ${pad2(d.getDate())} ${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}.${String(d.getMilliseconds()).padStart(3, '0')}`;
}

function fmtClock(ts) {
    const d = new Date(ts);
    return `${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}.${String(d.getMilliseconds()).padStart(3, '0')}`;
}

function fmtShort(ts) {
    const d = new Date(ts);
    return `${MONTHS[d.getMonth()]} ${d.getDate()} ${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

function fmtNum(n) {
    if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
    if (n >= 1e4) return `${Math.round(n / 1e3)}k`;
    return n.toLocaleString();
}

function toLocalInputValue(ts) {
    const d = new Date(ts);
    return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}T${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

function deploymentLabel(item, machines) {
    const cfg = item?.config || {};
    const node = nodeDisplayName(cfg.nodeId, machines);
    return [node, cfg.name].filter(Boolean).join(' / ') || `#${cfg.id}`;
}

function deploymentSpaceID(item) {
    return item?.config?.spaceId || 0;
}

function selectedDeployment(items, id) {
    return items.find(item => item.config?.id === id) || null;
}

function isSystemDeployment(item) {
    return item?.config?.name === SYSTEM_DEPLOYMENT_NAME && (
        deploymentSpaceID(item) === SYSTEM_SPACE_ID || Boolean(item?.config?.spec?.opendeploySpec)
    );
}

// A LogRecord's int64 nano timestamp arrives as a JS number; only its
// millisecond precision is reliable, which is all the UI renders.
function wrapRecord(r) {
    return {
        ts: Number(r.time) / 1e6,
        level: r.level || '',
        msg: r.msg || '',
        fields: r.fields || {},
        version: Number(r.version || 0),
        stream: Number(r.stream || 0) === 1 ? 'stderr' : 'stdout',
        node: Number(r.node || 0),
        run: Number(r.run || 0),
        instance: Number(r.instanceOrdinal || 0),
        seq: Number(r.seq || 0),
    };
}

// Unwrapped cells still break on embedded newlines — only soft-wrapping of
// long lines is opt-in — so their height is the newline count, capped like
// wrapped rows.
const newlineLines = (text) => {
    let n = 1, i = -1;
    while (n < WRAP_MAX_LINES && (i = text.indexOf('\n', i + 1)) !== -1) n++;
    return n;
};

// Wrapped row heights are computed rather than measured: the message column is
// monospaced and breaks on any character (break-all), so a record's line count
// follows from its text and the column width in characters. Measuring 10k rows
// in the DOM is not an option, and a uniform worst-case height would make every
// row three lines tall.
function segColumns(seg) {
    if (!seg.includes('\t')) return seg.length;
    let w = 0;
    for (const ch of seg) w += ch === '\t' ? 8 - (w % 8) : 1;
    return w;
}

function wrappedLines(text, cols) {
    if (cols <= 0) return 1;
    let lines = 0;
    for (const seg of text.split('\n')) {
        lines += Math.max(1, Math.ceil(segColumns(seg) / cols));
        if (lines >= WRAP_MAX_LINES) return WRAP_MAX_LINES;
    }
    return Math.max(1, lines);
}

// posAt returns the last row whose top offset is at or above y.
function posAt(offsets, len, y) {
    let lo = 0, hi = len - 1;
    while (lo < hi) {
        const mid = (lo + hi + 1) >> 1;
        if (offsets[mid] <= y) lo = mid; else hi = mid - 1;
    }
    return lo;
}

function recordField(rec, key) {
    if (key === 'time') return fmtRowTime(rec.ts);
    if (key === 'level') return rec.level;
    if (key === 'msg') return rec.msg;
    if (key === 'version') return rec.version ? `v${rec.version}` : '';
    if (key === 'node') return rec.node ? String(rec.node) : '';
    if (key === 'run') return rec.run ? String(rec.run) : '';
    if (key === 'instance') return String(rec.instance);
    if (key === 'stream') return rec.stream;
    return rec.fields[key];
}

function recordJson(rec) {
    const obj = {time: new Date(rec.ts).toISOString()};
    if (rec.level) obj.level = rec.level;
    obj.msg = rec.msg;
    for (const [k, v] of Object.entries(rec.fields)) obj[k] = v;
    if (rec.version) obj.config_version = rec.version;
    if (rec.node) obj.node = rec.node;
    if (rec.run) obj.run = rec.run;
    obj.instance = rec.instance;
    obj.stream = rec.stream;
    if (rec.seq) obj.seq = rec.seq;
    return JSON.stringify(obj, null, 2);
}

// --- query language --------------------------------------------------------
// One string is the whole filter state: bare words and "quoted phrases" match
// the message, key:value matches a field (key:* = field exists), a leading -
// negates. Sidebar and row actions edit this string rather than owning
// side-channel filter state.

function parseQuery(text) {
    const tokens = [];
    const re = /(-)?(?:([\w.]+):)?(?:"([^"]*)"|([^\s"]+))/g;
    let m;
    while ((m = re.exec(text))) {
        const neg = Boolean(m[1]);
        const key = m[2] ? m[2].toLowerCase() : '';
        const value = (m[3] !== undefined ? m[3] : (m[4] || '')).toLowerCase();
        if (!value && !key) continue;
        tokens.push(key ? {type: 'pair', key, value, neg} : {type: 'text', value, neg});
    }
    return tokens;
}

const quoteValue = (v) => /\s/.test(v) ? `"${v}"` : v;
const tokenText = (t) => `${t.neg ? '-' : ''}${t.type === 'pair' ? `${t.key}:${quoteValue(t.value)}` : quoteValue(t.value)}`;

// tokensToRequest converts parsed tokens into wire filters. A non-negated
// integer version:N token addresses the deployment config version rather than
// a parsed field.
function tokensToRequest(tokens) {
    const filters = [];
    let configVersion = 0;
    for (const t of tokens) {
        if (t.type === 'text') {
            filters.push({op: t.neg ? 'not_contains' : 'contains', value: t.value});
            continue;
        }
        if (t.key === 'version' && !t.neg && /^\d+$/.test(t.value)) {
            configVersion = Number(t.value);
            continue;
        }
        if (t.value === '*') {
            filters.push({field: t.key, op: t.neg ? 'not_exists' : 'exists'});
            continue;
        }
        filters.push({field: t.key, op: t.neg ? 'neq' : 'eq', value: t.value});
    }
    return {filters, configVersion};
}

export function logsPage(selectedDeploymentId) {
    // --- state -------------------------------------------------------------
    const hiddenSpaces = van.state(loadHiddenSpaces());
    const deploymentId = van.state(selectedDeploymentId.val || 0);
    const queryText = van.state('');
    // workloadScope narrows the search to a config version / instance ordinal /
    // run; version and run 0 and instance null mean "all".
    const workloadScope = van.state({version: 0, instance: null, run: 0});
    const workloadVersionsS = van.state(null);
    const range = van.state({kind: 'preset', key: DEFAULT_PRESET});
    const levelOn = van.state(Object.fromEntries(LEVELS.map(l => [l, true])));
    const columns = van.state(['time', 'msg', 'level']);
    const wrap = van.state(false);
    const result = van.state(null);
    const searching = van.state(false);
    const errorMsg = van.state('');
    const expanded = van.state(null);       // {pos} — display position (oldest first)
    const detailTab = van.state('fields');
    const context = van.state(null);        // {rec}
    const sidebarOpen = van.state(true);
    const fieldOpen = van.state('');
    const timeOpen = van.state(false);
    const jsonCopied = van.state(false);
    let searchGen = 0;
    let autoSearchedDeploymentId = 0;
    let scroller;

    const liveDeployments = () => (deploymentsS.val || []).filter(item => item.config?.id && !item.config.deleted);

    // scopePayload resolves the deployment scope for a request: the system
    // deployment queries as deployment 0 on its node.
    const scopePayload = () => {
        const selected = selectedDeployment(liveDeployments(), Number(deploymentId.val || 0));
        return {
            deploymentId: isSystemDeployment(selected) ? 0 : Number(deploymentId.val || 0),
            targetNodeId: Number(selected?.config?.nodeId || 0),
        };
    };

    const resolveRange = () => {
        const r = range.val;
        if (r.kind === 'custom') return {startTs: r.startTs, endTs: r.endTs};
        const preset = PRESETS.find(pr => pr.key === r.key) || PRESETS[PRESETS.length - 1];
        const endTs = Date.now();
        return {startTs: endTs - preset.ms, endTs};
    };

    const rangeLabel = () => {
        const r = range.val;
        if (r.kind === 'custom') return `${fmtShort(r.startTs)} – ${fmtShort(r.endTs)}`;
        return (PRESETS.find(pr => pr.key === r.key) || PRESETS[PRESETS.length - 1]).label;
    };

    const currentRequest = () => {
        const {filters, configVersion} = tokensToRequest(parseQuery(queryText.val));
        const scope = workloadScope.val;
        if (scope.instance != null) filters.push({field: 'instance', op: 'eq', value: String(scope.instance)});
        if (scope.run) filters.push({field: 'run', op: 'eq', value: String(scope.run)});
        const enabled = LEVELS.filter(l => levelOn.val[l]);
        if (enabled.length < NL) {
            filters.push({field: 'level', op: 'in', values: enabled});
        }
        // An explicit version:N token in the query text outranks the picker.
        return {...scopePayload(), configVersion: configVersion || Number(scope.version || 0), filters};
    };

    const runSearch = async () => {
        if (!Number(deploymentId.val || 0)) {
            errorMsg.val = 'Select a deployment to search.';
            return;
        }
        const gen = ++searchGen;
        const {startTs, endTs} = resolveRange();
        searching.val = true;
        errorMsg.val = '';
        expanded.val = null;
        try {
            const t0 = performance.now();
            const resp = await capi.postV1DeploymentsLogQuery({
                ...currentRequest(),
                timeStart: new Date(startTs),
                timeEnd: new Date(endTs),
                limit: LOG_LINE_LIMIT,
                histogramBuckets: HISTOGRAM_BUCKETS,
            });
            if (gen !== searchGen) return;
            const built = buildResult(resp);
            built.feMs = Math.round(performance.now() - t0);
            result.val = built;
            // Test hook: the virtualised table only renders a window, so e2e
            // assertions read the full result set from here.
            window.__logsResult = result.val;
            searching.val = false;
            pendingScrollBottom = true;
        } catch (e) {
            if (gen !== searchGen) return;
            searching.val = false;
            errorMsg.val = `Search failed: ${e.message || e}`;
        }
    };

    const buildResult = (resp) => {
        const stats = resp.stats || {};
        const hist = resp.histogram;
        let startTs = stats.timeStart instanceof Date ? stats.timeStart.getTime() : 0;
        let endTs = stats.timeEnd instanceof Date ? stats.timeEnd.getTime() : startTs;
        const bucketMs = Number(hist?.bucketMs || 0);
        const series = hist?.series || [];
        const bucketN = series.length ? series[0].counts.length : 0;
        if (bucketN && bucketMs && hist.startTime instanceof Date) {
            startTs = hist.startTime.getTime();
            endTs = startTs + bucketN * bucketMs;
        }
        const counts = new Array(bucketN * NL).fill(0);
        for (const s of series) {
            const li = LEVELS.indexOf(s.level || '');
            if (li < 0) continue;
            for (let b = 0; b < bucketN; b++) counts[b * NL + li] += Number(s.counts[b] || 0);
        }
        return {
            records: (resp.records || []).map(wrapRecord).reverse(),  // oldest first; newest at the bottom
            fields: resp.fields || [],
            sampled: Number(stats.sampledRows || 0),
            warnings: resp.warnings || [],
            matched: Number(stats.matchedRows || 0),
            scanned: Number(stats.scannedRows || 0),
            truncated: Boolean(stats.truncated),
            tookMs: Number(stats.tookMs || 0),
            startTs, endTs, bucketMs, bucketN, counts,
        };
    };

    const addFilter = (key, value, neg = false) => {
        const token = `${neg ? '-' : ''}${key}:${quoteValue(String(value).toLowerCase())}`;
        if (queryText.val.split(/\s+/).includes(token)) return;
        queryText.val = `${queryText.val} ${token}`.trim();
        void runSearch();
    };

    const removeToken = (pos) => {
        const tokens = parseQuery(queryText.val);
        tokens.splice(pos, 1);
        queryText.val = tokens.map(tokenText).join(' ');
        void runSearch();
    };

    const toggleColumn = (key) => {
        columns.val = columns.val.includes(key)
            ? columns.val.filter(k => k !== key || k === 'msg')
            : [...columns.val, key];
    };

    const moveColumn = (key, dir) => {
        const cols = [...columns.val];
        const i = cols.indexOf(key);
        const j = i + dir;
        if (i < 0 || j < 0 || j >= cols.length) return;
        [cols[i], cols[j]] = [cols[j], cols[i]];
        columns.val = cols;
    };

    const levelTotals = (res) => {
        const totals = new Array(NL).fill(0);
        if (res) for (let b = 0; b < res.bucketN; b++) for (let l = 0; l < NL; l++) totals[l] += res.counts[b * NL + l];
        return totals;
    };

    // --- deployment scope ----------------------------------------------------

    const spaceFilter = spacesFilter({
        hiddenS: hiddenSpaces,
        onChange: saveHiddenSpaces,
        testid: "logs-space-filter",
        buttonClass: "input inline-flex h-[30px] items-center gap-1.5 text-xs text-gray-200 cursor-pointer hover:bg-gray-700",
    });

    const deploymentSelect = select({
        "data-testid": "logs-deployment-select",
        class: "input h-[30px] min-w-48 py-1 text-xs",
        onchange: (e) => {
            deploymentId.val = Number(e.target.value || 0);
            if (deploymentId.val) void runSearch();
        },
    });

    // rawVal: reacting to deploymentId here would snap a manual dropdown
    // switch back to the deployment the page was opened for. Navigating to a
    // deployment in a hidden space unhides that space, else the reset-on-
    // filter derive would immediately clear the selection.
    van.derive(() => {
        if (selectedDeploymentId.val && selectedDeploymentId.val !== deploymentId.rawVal) {
            deploymentId.val = selectedDeploymentId.val;
            const item = selectedDeployment((deploymentsS.rawVal || []), selectedDeploymentId.val);
            const sid = deploymentSpaceID(item);
            if (item && hiddenSpaces.rawVal.has(sid)) {
                const next = new Set(hiddenSpaces.rawVal);
                next.delete(sid);
                hiddenSpaces.val = next;
                saveHiddenSpaces(next);
            }
        }
    });

    // --- version / instance / run scope --------------------------------------

    const workloadOrdinals = van.derive(() => {
        const item = selectedDeployment(liveDeployments(), Number(deploymentId.val || 0));
        const ordinals = [...new Set((item?.scheduledInstances || []).map(s => Number(s.instance?.instanceOrdinal || 0)))].sort((a, b) => a - b);
        return ordinals.length ? ordinals : [0];
    });

    let versionsForDeployment = 0;
    const ensureWorkloadVersions = async () => {
        const id = Number(deploymentId.val || 0);
        if (!id) { workloadVersionsS.val = []; return; }
        if (versionsForDeployment === id && workloadVersionsS.val) return;
        versionsForDeployment = id;
        workloadVersionsS.val = null;
        const current = Number(selectedDeployment(liveDeployments(), id)?.config?.version || 0);
        try {
            const resp = await capi.postV1DeploymentsHistory({deploymentId: id});
            if (versionsForDeployment !== id) return;
            const versions = new Set((resp.entries || []).map(e => Number(e.config?.version || 0)));
            if (current) versions.add(current);
            workloadVersionsS.val = [...versions].filter(v => v > 0).sort((a, b) => b - a);
        } catch {
            if (versionsForDeployment !== id) return;
            versionsForDeployment = 0;   // allow a retry on the next open
            workloadVersionsS.val = current ? [current] : [];
        }
    };

    // Run numbers only exist in the log store, so they come from an
    // aggregates-only query's sampled field stats over the current range.
    const fetchWorkloadRuns = async (version, instance) => {
        const {startTs, endTs} = resolveRange();
        const filters = [];
        if (instance != null) filters.push({field: 'instance', op: 'eq', value: String(instance)});
        const resp = await capi.postV1DeploymentsLogQuery({
            ...scopePayload(),
            configVersion: Number(version || 0),
            filters,
            timeStart: new Date(startTs),
            timeEnd: new Date(endTs),
            limit: -1,
            histogramBuckets: 0,
        });
        const stats = (resp.fields || []).find(f => f.field === 'run');
        const runs = [...new Set((stats?.top || []).map(e => Number(e.value)))]
            .filter(n => Number.isFinite(n) && n > 0)
            .sort((a, b) => b - a);
        return {runs, distinct: Number(stats?.distinct || 0)};
    };

    van.derive(() => {
        void deploymentId.val;
        workloadScope.val = {version: 0, instance: null, run: 0};
        workloadVersionsS.val = null;
        versionsForDeployment = 0;
    });

    van.derive(() => {
        const items = liveDeployments();
        const filtered = items.filter(item => !hiddenSpaces.val.has(deploymentSpaceID(item)));
        if (deploymentId.val && filtered.length > 0 && !selectedDeployment(filtered, Number(deploymentId.val))) {
            deploymentId.val = 0;
        }
        deploymentSelect.replaceChildren(
            option({value: ""}, "Select deployment"),
            ...filtered.map(item => option({value: String(item.config.id)}, deploymentLabel(item, machinesS.val))),
        );
        deploymentSelect.value = String(deploymentId.val || '');
    });

    van.derive(() => {
        const id = Number(deploymentId.val || 0);
        if (!id || !loginS.val || autoSearchedDeploymentId === id) return;
        autoSearchedDeploymentId = id;
        setTimeout(() => {
            if (Number(deploymentId.val || 0) === id) void runSearch();
        }, 0);
    });

    // --- search bar ----------------------------------------------------------

    const queryInput = input({
        "data-testid": "logs-query-input",
        class: "input search-input-iconed h-[30px] w-full py-1 pr-2 font-mono text-xs bg-gray-900",
        placeholder: 'free text and field filters — e.g. level:error "pool exhausted" status:500 err:* -host:node-2',
        value: () => queryText.val,
        oninput: (e) => { queryText.val = e.target.value; },
        onkeydown: (e) => { if (e.key === 'Enter') void runSearch(); },
    });

    const presetRow = (preset) => button({
        type: "button",
        class: "flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs text-gray-200 hover:bg-gray-800 cursor-pointer",
        onclick: () => {
            range.val = {kind: 'preset', key: preset.key};
            timeOpen.val = false;
            void runSearch();
        },
    },
        span({class: "w-4"}, () => range.val.kind === 'preset' && range.val.key === preset.key ? checkIcon({class: "w-3.5 h-3.5 text-brand"}) : ''),
        preset.label);

    // The custom inputs need [color-scheme:dark]: without it the browser
    // renders the native picker chrome for a light page, which on this surface
    // leaves the calendar control invisible.
    // The inputs stay plain DOM state read back at apply time: binding their
    // values through van states makes them dependencies of the enclosing
    // dropdown binding, which then rebuilds — and resets both fields — on
    // every edit. That is what made the pickers unusable before.
    const customRange = () => {
        const rangeError = van.state('');
        const dtInput = (testid, ts) => input({
            "data-testid": testid,
            class: "input min-w-0 flex-1 py-1 text-xs [color-scheme:dark]",
            type: "datetime-local",
            value: toLocalInputValue(ts),
            oninput: () => { rangeError.val = ''; },
        });
        const {startTs, endTs} = resolveRange();
        const startInput = dtInput("logs-custom-start", startTs);
        const endInput = dtInput("logs-custom-end", endTs);
        const dtField = (label, el) => div(
            {class: "flex items-center gap-2"},
            span({class: "w-8 flex-none text-[10px] text-gray-500"}, label),
            el,
        );
        return div(
            {class: "border-t border-gray-800 p-3 flex flex-col gap-1.5"},
            span({class: "text-[10px] uppercase tracking-wide text-gray-500"}, "custom range"),
            dtField("from", startInput),
            dtField("to", endInput),
            () => rangeError.val ? span({class: "text-[10px] text-red-400"}, rangeError.val) : '',
            button({
                "data-testid": "logs-custom-apply",
                type: "button",
                class: "mt-1 cursor-pointer rounded-[0.3rem] border border-gray-600 bg-gray-700 py-1 text-xs text-gray-200 transition-colors hover:bg-gray-600",
                onclick: () => {
                    const start = new Date(startInput.value).getTime();
                    const end = new Date(endInput.value).getTime();
                    if (!Number.isFinite(start) || !Number.isFinite(end)) { rangeError.val = 'Enter a complete start and end date.'; return; }
                    if (end <= start) { rangeError.val = 'End must be after start.'; return; }
                    range.val = {kind: 'custom', startTs: start, endTs: end};
                    timeOpen.val = false;
                    void runSearch();
                },
            }, "Apply"),
        );
    };

    const timePicker = div(
        {class: "relative"},
        button({
            "data-testid": "logs-time-button",
            type: "button",
            class: "input flex h-[30px] items-center gap-1.5 whitespace-nowrap text-xs text-gray-200 cursor-pointer hover:bg-gray-700",
            onclick: () => { timeOpen.val = !timeOpen.val; },
        }, () => rangeLabel(), chevronDownIcon({class: "w-3 h-3 text-gray-500"})),
        () => !timeOpen.val ? '' : div(
            div({class: "fixed inset-0 z-20", onclick: () => { timeOpen.val = false; }}),
            div(
                {class: "absolute right-0 top-full z-30 mt-1 w-[22rem] rounded border border-gray-700 bg-gray-900 py-1 shadow-xl"},
                div({class: "grid grid-flow-col grid-cols-2 grid-rows-[repeat(7,auto)]"}, ...PRESETS.map(presetRow)),
                customRange(),
            ),
        ),
    );

    const chip = (token, pos) => span(
        {class: `inline-flex items-center gap-1 rounded border px-1.5 py-px font-mono text-[11px] ${token.neg
            ? "border-red-900/60 bg-red-950/40 text-red-300"
            : "border-gray-700 bg-gray-800 text-gray-200"}`},
        token.neg ? span({class: "font-sans text-[9px] uppercase text-red-400"}, "not") : '',
        token.type === 'pair' ? `${token.key}:${token.value}` : `"${token.value}"`,
        button({
            type: "button",
            class: "cursor-pointer text-gray-500 hover:text-gray-200",
            "aria-label": "Remove filter",
            onclick: () => removeToken(pos),
        }, xIcon({size: 10})),
    );

    const searchBar = div(
        {class: "flex-none border-b border-gray-700"},
        div(
            {class: "flex items-center gap-1.5 px-2 py-1.5"},
            spaceFilter,
            deploymentSelect,
            logScopePicker({
                scopeS: workloadScope,
                ordinalsS: workloadOrdinals,
                versionsS: workloadVersionsS,
                ensureVersions: ensureWorkloadVersions,
                fetchRuns: fetchWorkloadRuns,
                disabledS: van.derive(() => !Number(deploymentId.val || 0)),
                onChange: () => { if (Number(deploymentId.val || 0)) void runSearch(); },
            }),
            div(
                {class: "relative min-w-0 flex-1"},
                span({class: "pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-gray-500"}, searchIcon({class: "w-3.5 h-3.5"})),
                queryInput,
            ),
            timePicker,
            // Not btn-primary: its un-layered padding beats utility overrides,
            // and the border keeps the button the same box as the .input row.
            button({
                "data-testid": "logs-search-button",
                type: "button",
                class: "inline-flex h-[30px] cursor-pointer items-center rounded-[0.3rem] border border-brand bg-brand px-3 text-xs font-medium text-white transition-colors hover:bg-blue-600 disabled:cursor-default disabled:opacity-50",
                disabled: () => searching.val,
                onclick: () => void runSearch(),
            }, () => searching.val ? 'Searching…' : 'Search'),
        ),
        () => {
            const tokens = parseQuery(queryText.val);
            return tokens.length === 0 ? '' : div(
                {class: "flex flex-wrap items-center gap-1 px-2 pb-1.5"},
                ...tokens.map(chip),
            );
        },
    );

    // --- histogram -----------------------------------------------------------

    const histTooltip = div({class: "pointer-events-none absolute z-30 hidden rounded border border-gray-700 bg-gray-900 px-2 py-1 text-[10px] shadow-xl"});
    const histHover = div({class: "pointer-events-none absolute inset-y-0 hidden bg-white/5"});
    const histBrush = div({class: "pointer-events-none absolute inset-y-0 hidden border-x border-brand/70 bg-brand/15"});
    const histSvgHost = div({class: "absolute inset-0"});
    const histHost = div(
        {"data-testid": "logs-histogram", class: "relative h-[76px] min-w-0 flex-1 cursor-crosshair"},
        histSvgHost, histHover, histBrush, histTooltip,
    );
    const histMax = span({class: "w-10 flex-none self-start text-right text-[10px] tabular-nums text-gray-600"});
    const histAxis = div({class: "flex justify-between pl-12 pr-1 text-[10px] tabular-nums text-gray-600"});

    let drag = null;

    const bucketTotal = (res, b) => {
        let total = 0;
        for (let l = 0; l < NL; l++) total += res.counts[b * NL + l];
        return total;
    };

    const renderHistogram = () => {
        const res = result.val;
        const w = histHost.clientWidth;
        if (!res || !res.bucketN || !w) { histSvgHost.replaceChildren(); histMax.textContent = ''; histAxis.replaceChildren(); return; }
        const h = 76;
        const bucketW = w / res.bucketN;
        let maxTotal = 1;
        for (let b = 0; b < res.bucketN; b++) {
            const total = bucketTotal(res, b);
            if (total > maxTotal) maxTotal = total;
        }
        const bars = [];
        // Stack bottom→top none, DEBUG, INFO, WARN, ERROR so errors ride on
        // top, with a 2px gap between bars and a 1px surface gap between
        // segments tall enough to afford one. Segments keep their exact share
        // of the bucket height — a non-empty bucket gets a 1px presence mark
        // in its dominant level rather than every level being inflated to 1px.
        for (let b = 0; b < res.bucketN; b++) {
            const x = b * bucketW + 1;
            const barW = Math.max(1, bucketW - 2);
            const total = bucketTotal(res, b);
            if (!total) continue;
            const totalH = (total / maxTotal) * (h - 4);
            if (totalH < 1) {
                let top = 0;
                for (let l = 1; l < NL; l++) if (res.counts[b * NL + l] > res.counts[b * NL + top]) top = l;
                bars.push(rect({x: x.toFixed(1), y: String(h - 1), width: barW.toFixed(1), height: "1", fill: levelMeta(LEVELS[top]).fill}));
                continue;
            }
            let y = h;
            let topDrawn = false;
            for (let l = NL - 1; l >= 0; l--) {
                const count = res.counts[b * NL + l];
                if (!count) continue;
                const segH = (count / total) * totalH;
                y -= segH;
                if (segH < 0.5) continue;
                let isTop = true;
                for (let u = 0; u < l; u++) if (res.counts[b * NL + u]) { isTop = false; break; }
                bars.push(rect({
                    x: x.toFixed(1), y: Math.max(0, y).toFixed(1),
                    width: barW.toFixed(1), height: Math.max(0.5, segH - (!topDrawn || segH < 3 ? 0 : 1)).toFixed(1),
                    rx: isTop && segH >= 3 ? 1.5 : 0,
                    fill: levelMeta(LEVELS[l]).fill,
                }));
                topDrawn = true;
            }
        }
        histSvgHost.replaceChildren(svgEl(
            {width: String(w), height: String(h), viewBox: `0 0 ${w} ${h}`},
            svgLine({x1: "0", y1: String(h / 2), x2: String(w), y2: String(h / 2), stroke: "#1f2937", "stroke-width": "1"}),
            ...bars,
        ));
        histMax.textContent = fmtNum(maxTotal);
        const mid = res.startTs + (res.endTs - res.startTs) / 2;
        histAxis.replaceChildren(span(fmtShort(res.startTs)), span(fmtShort(mid)), span(fmtShort(res.endTs)));
    };

    histHost.onpointerdown = (e) => {
        const rectBox = histHost.getBoundingClientRect();
        drag = {x0: e.clientX - rectBox.left, x1: e.clientX - rectBox.left};
        histHost.setPointerCapture(e.pointerId);
    };
    histHost.onpointermove = (e) => {
        const res = result.val;
        const rectBox = histHost.getBoundingClientRect();
        const x = Math.max(0, Math.min(rectBox.width, e.clientX - rectBox.left));
        if (drag) {
            drag.x1 = x;
            const lo = Math.min(drag.x0, drag.x1), hi = Math.max(drag.x0, drag.x1);
            histBrush.style.left = `${lo}px`;
            histBrush.style.width = `${hi - lo}px`;
            histBrush.classList.remove('hidden');
            histTooltip.classList.add('hidden');
            histHover.classList.add('hidden');
            return;
        }
        if (!res || !res.bucketN) return;
        const bucketW = rectBox.width / res.bucketN;
        const b = Math.max(0, Math.min(res.bucketN - 1, Math.floor(x / bucketW)));
        histHover.style.left = `${b * bucketW}px`;
        histHover.style.width = `${bucketW}px`;
        histHover.classList.remove('hidden');
        const t0 = res.startTs + b * res.bucketMs;
        histTooltip.replaceChildren(
            div({class: "mb-0.5 text-gray-400"}, `${fmtShort(t0)} – ${fmtClock(t0 + res.bucketMs).slice(0, 5)}`),
            ...LEVELS.map((lvl, l) => div(
                {class: "flex items-center gap-1.5"},
                span({class: "h-2 w-2 rounded-[2px]", style: `background:${levelMeta(lvl).fill}`}),
                span({class: "w-10 text-gray-300"}, levelMeta(lvl).label),
                span({class: "tabular-nums text-gray-100"}, res.counts[b * NL + l].toLocaleString()),
            )),
        );
        histTooltip.classList.remove('hidden');
        const tipX = Math.min(rectBox.width - 130, x + 10);
        histTooltip.style.left = `${Math.max(0, tipX)}px`;
        histTooltip.style.top = `-6px`;
    };
    histHost.onpointerleave = () => { histTooltip.classList.add('hidden'); histHover.classList.add('hidden'); };
    histHost.onpointerup = () => {
        const res = result.val;
        const d = drag;
        drag = null;
        histBrush.classList.add('hidden');
        if (!d || !res || Math.abs(d.x1 - d.x0) < 5) return;
        const w = histHost.clientWidth;
        const t0 = res.startTs + (Math.min(d.x0, d.x1) / w) * (res.endTs - res.startTs);
        const t1 = res.startTs + (Math.max(d.x0, d.x1) / w) * (res.endTs - res.startTs);
        range.val = {kind: 'custom', startTs: Math.floor(t0), endTs: Math.ceil(t1)};
        void runSearch();
    };

    const legendButton = (lvl, l) => button({
        type: "button",
        class: () => `flex cursor-pointer items-center gap-1 rounded px-1 py-px transition-opacity ${levelOn.val[lvl] ? '' : 'opacity-35'}`,
        title: () => levelOn.val[lvl] ? `Hide ${levelMeta(lvl).label}` : `Show ${levelMeta(lvl).label}`,
        onclick: () => {
            levelOn.val = {...levelOn.val, [lvl]: !levelOn.val[lvl]};
            void runSearch();
        },
    },
        span({class: "h-2 w-2 rounded-[2px]", style: `background:${levelMeta(lvl).fill}`}),
        span({class: "text-[10px] text-gray-400"}, levelMeta(lvl).label),
        span({class: "text-[10px] tabular-nums text-gray-500"}, () => fmtNum(levelTotals(result.val)[l])),
    );

    const statusLine = () => {
        if (errorMsg.val) return errorMsg.val;
        if (searching.val) return 'Searching…';
        const res = result.val;
        if (!res) return 'No search yet.';
        let text = `${res.scanned.toLocaleString()} events scanned in ${res.tookMs} ms (fe ${res.feMs} ms) · ${res.matched.toLocaleString()} match${res.matched === 1 ? '' : 'es'}`;
        if (res.truncated) text += ` · list shows newest ${res.records.length.toLocaleString()}`;
        if (res.warnings.length) text += ` · ${res.warnings[0]}`;
        return text;
    };

    const histogramBand = div(
        {class: "flex-none border-b border-gray-800 bg-gray-950/40 px-2 pb-1 pt-1"},
        div(
            {class: "flex items-center gap-3 pb-1"},
            p({class: "min-w-0 flex-1 truncate text-[11px] text-gray-500", "aria-live": "polite", "data-testid": "logs-status"}, statusLine),
            () => range.val.kind !== 'custom' ? '' : button({
                type: "button",
                class: "cursor-pointer rounded border border-gray-700 px-1.5 py-px text-[10px] text-gray-400 hover:text-gray-200",
                onclick: () => { range.val = {kind: 'preset', key: DEFAULT_PRESET}; void runSearch(); },
            }, "clear zoom"),
            div({class: "flex items-center gap-2"}, ...LEVELS.map(legendButton)),
            button({
                type: "button",
                class: () => `cursor-pointer rounded border px-1.5 py-px text-[10px] ${wrap.val
                    ? 'border-brand/60 bg-brand/15 text-blue-300' : 'border-gray-700 text-gray-400 hover:text-gray-200'}`,
                "aria-pressed": () => String(wrap.val),
                onclick: () => { wrap.val = !wrap.val; },
            }, "wrap"),
        ),
        div({class: "flex items-end gap-2"}, histMax, histHost),
        histAxis,
    );

    // --- sidebar: columns + fields ------------------------------------------

    const microHeader = (text) => div({class: "px-2 pb-0.5 pt-2 text-[10px] font-medium uppercase tracking-wide text-gray-500"}, text);

    const columnRow = (key) => div(
        {class: "group flex items-center gap-1 px-2 py-0.5 text-[11px] text-gray-300 hover:bg-gray-800/40"},
        span({class: "min-w-0 flex-1 truncate font-mono"}, COLUMN_DEFS[key]?.label || key),
        div(
            {class: "hidden items-center gap-0.5 group-hover:flex"},
            button({type: "button", class: "cursor-pointer rounded px-1 text-gray-500 hover:text-gray-200", title: "Move left", onclick: () => moveColumn(key, -1)}, "↑"),
            button({type: "button", class: "cursor-pointer rounded px-1 text-gray-500 hover:text-gray-200", title: "Move right", onclick: () => moveColumn(key, 1)}, "↓"),
            key === 'msg' ? '' : button({
                type: "button", class: "cursor-pointer rounded px-0.5 text-gray-500 hover:text-red-400",
                title: "Remove column", onclick: () => toggleColumn(key),
            }, xIcon({size: 11})),
        ),
    );

    const fieldValueRow = (key, entry) => div(
        {class: "group/val px-2 py-0.5"},
        div(
            {class: "flex items-center gap-1 text-[11px]"},
            span({class: "min-w-0 flex-1 truncate font-mono text-gray-300", title: entry.value}, entry.value),
            span({class: "tabular-nums text-gray-500"}, `${Math.round(entry.frac * 100)}%`),
            div(
                {class: "hidden gap-0.5 group-hover/val:flex"},
                button({
                    type: "button", class: "cursor-pointer rounded bg-gray-800 px-1 text-gray-300 hover:text-green-400",
                    title: `Filter for ${key}:${entry.value}`, onclick: () => addFilter(key, entry.value),
                }, "+"),
                button({
                    type: "button", class: "cursor-pointer rounded bg-gray-800 px-1 text-gray-300 hover:text-red-400",
                    title: `Filter out ${key}:${entry.value}`, onclick: () => addFilter(key, entry.value, true),
                }, "−"),
            ),
        ),
        div({class: "mt-px h-[3px] rounded-sm bg-gray-800"},
            div({class: "h-full rounded-sm bg-brand/70", style: `width:${Math.max(2, entry.frac * 100)}%`})),
    );

    // Per-field stats ride in the query response, sampled server-side over
    // the newest matched records of the current search.
    const fieldStatsPanel = (key) => {
        const s = (result.val?.fields || []).find(f => f.field === key);
        const sampled = result.val?.sampled || 0;
        if (!s || !sampled) return div({class: "px-2 py-1 text-[11px] text-gray-600"}, "No matches to sample.");
        const withField = Number(s.coverage || 0) * sampled;
        if (!withField) return div({class: "px-2 py-1 text-[11px] text-gray-600"}, "Not in the sampled matches.");
        const other = Number(s.other || 0);
        return div(
            {class: "border-b border-gray-800/60 pb-1"},
            div({class: "px-2 py-0.5 text-[10px] text-gray-500"},
                `in ${Math.round(Number(s.coverage || 0) * 100)}% of ${fmtNum(sampled)} sampled · ${Number(s.distinct || 0)} distinct`),
            ...(s.top || []).map(entry => fieldValueRow(key, {
                value: entry.value,
                frac: sampled ? Number(entry.count) / sampled : 0,
            })),
            other > 0 ? div({class: "px-2 py-0.5 text-[10px] text-gray-600"},
                `+ ${fmtNum(other)} in ${fmtNum(Number(s.distinct || 0) - (s.top || []).length)} other values`) : '',
        );
    };

    const fieldRow = (key) => div(
        div(
            {class: "flex cursor-pointer items-center gap-1 px-1 py-0.5 hover:bg-gray-800/40",
                "data-testid": `logs-field-${key}`,
                onclick: () => { fieldOpen.val = fieldOpen.val === key ? '' : key; }},
            caretRightIcon({class: () => `w-3 h-3 flex-none text-gray-600 transition-transform ${fieldOpen.val === key ? 'rotate-90' : ''}`}),
            span({class: "min-w-0 flex-1 truncate font-mono text-[11px] text-gray-300"}, key),
            button({
                type: "button",
                class: () => `cursor-pointer rounded p-0.5 ${columns.val.includes(key) ? 'text-brand' : 'text-gray-600 hover:text-gray-300'}`,
                title: () => columns.val.includes(key) ? "Remove column" : "Add as column",
                onclick: (e) => { e.stopPropagation(); toggleColumn(key); },
            }, columnsIcon({size: 12})),
        ),
        () => fieldOpen.val === key ? div(fieldStatsPanel(key)) : '',
    );

    const sidebar = () => !sidebarOpen.val
        ? div(
            {class: "flex w-7 flex-none flex-col items-center border-r border-gray-800 pt-1.5"},
            button({
                type: "button", class: "cursor-pointer rounded p-1 text-gray-500 hover:bg-gray-800 hover:text-gray-200",
                title: "Show fields", onclick: () => { sidebarOpen.val = true; },
            }, columnsIcon({size: 14})),
        )
        : div(
            {class: "flex w-60 flex-none flex-col border-r border-gray-800"},
            div(
                {class: "flex flex-none items-center border-b border-gray-800 py-0.5 pl-2 pr-1"},
                span({class: "flex-1 text-[10px] font-medium uppercase tracking-wide text-gray-500"}, "fields"),
                button({
                    type: "button", class: "cursor-pointer rounded p-1 text-gray-500 hover:bg-gray-800 hover:text-gray-200",
                    title: "Hide fields", onclick: () => { sidebarOpen.val = false; },
                }, caretRightIcon({class: "w-3 h-3 rotate-180"})),
            ),
            div(
                {class: "app-scroll min-h-0 flex-1 overflow-y-auto pb-2"},
                microHeader("columns"),
                () => div(...columns.val.map(columnRow)),
                microHeader("available fields"),
                () => {
                    const names = (result.val?.fields || []).map(f => f.field);
                    return names.length === 0
                        ? div({class: "px-2 py-1 text-[11px] text-gray-600"}, "No parsed fields in results.")
                        : div(...names.map(fieldRow));
                },
            ),
        );

    // --- results table (virtualised) ----------------------------------------

    const colTemplate = (cols) => cols.map(k => {
        const d = COLUMN_DEFS[k] || {px: 110};
        return d.flex ? `minmax(${d.minPx}px,1fr)` : `${d.px}px`;
    }).join(' ');

    const colMinWidth = (cols) => cols.reduce((n, k) => {
        const d = COLUMN_DEFS[k] || {px: 110};
        return n + (d.flex ? d.minPx : d.px);
    }, 0);

    const headerRow = div({class: "sticky top-0 z-20 grid border-b border-gray-700 bg-gray-950"});
    const rowsHost = div({class: "relative"});
    const inner = div({}, headerRow, rowsHost);

    // Off-screen probe for the message cell's character width. It mirrors the
    // real nesting — .font-mono is a base-layer 0.92em rule, so it must resolve
    // against an inherited text-[11px] parent the way a cell does, not against
    // a text size set on the same element.
    const charProbe = span({class: "font-mono", style: "white-space:pre"}, 'x'.repeat(100));
    const probeHost = div(
        {class: "text-[11px] leading-[15px]", style: "position:absolute;left:-9999px;top:0;visibility:hidden;pointer-events:none"},
        charProbe,
    );
    let charW = 0;
    const charWidth = () => {
        const w = charProbe.getBoundingClientRect().width / 100;
        if (w > 0) charW = w;
        return charW || 6.1;   // pre-layout fallback; refined on the next render
    };

    scroller = div({
        "data-testid": "logs-results",
        class: "app-scroll min-h-0 flex-1 overflow-auto bg-gray-950",
    }, probeHost, inner);

    const headerCell = (key) => {
        const d = COLUMN_DEFS[key] || {label: key};
        return div(
            {class: "group flex items-center gap-1 border-r border-gray-800/40 px-2 py-1 text-[10px] font-medium uppercase tracking-wide text-gray-500 last:border-r-0"},
            span({class: "truncate"}, d.label || key),
            key === 'msg' ? '' : span(
                {class: "hidden items-center group-hover:inline-flex"},
                button({
                    type: "button", class: "cursor-pointer px-0.5 text-gray-600 hover:text-red-400",
                    title: "Remove column", onclick: () => toggleColumn(key),
                }, xIcon({size: 10})),
            ),
        );
    };

    const cellEl = (key, rec, lines) => {
        const d = COLUMN_DEFS[key] || {};
        const base = "overflow-hidden border-r border-gray-800/25 px-2 py-[3px] last:border-r-0";
        if (key === 'time') return div({class: `${base} whitespace-nowrap font-mono tabular-nums text-gray-400`}, fmtRowTime(rec.ts));
        if (key === 'level') return rec.level
            ? div({class: `${base} font-medium ${levelMeta(rec.level).text}`}, rec.level)
            : div({class: `${base} text-gray-700`}, "—");
        // Newlines always render as line breaks (the row is sized for them);
        // wrap only adds soft-wrapping of long lines. The inline clamp pins
        // the message to the lines the row was sized for, so a mis-estimate
        // ellipsizes cleanly instead of leaving a clipped sliver of an extra
        // line.
        if (key === 'msg') return div({
            class: `${base} font-mono text-gray-200 line-clamp-3 ${wrap.val ? 'whitespace-pre-wrap break-all' : 'whitespace-pre'}`,
            style: `-webkit-line-clamp:${lines}`,
            title: rec.msg,
        }, rec.msg);
        const value = recordField(rec, key);
        if (value === undefined || value === '') return div({class: `${base} text-gray-700`}, "—");
        return div({class: `${base} truncate whitespace-nowrap ${d.num ? 'text-right tabular-nums' : ''} ${d.mono ? 'font-mono' : ''} text-gray-300`}, String(value));
    };

    const toggleExpand = (pos) => {
        expanded.val = expanded.val && expanded.val.pos === pos ? null : {pos};
        detailTab.val = 'fields';
        jsonCopied.val = false;
    };

    const detailFieldRow = (rec, key, value) => div(
        {class: "group grid grid-cols-[9rem_minmax(0,1fr)_auto] items-baseline gap-2 rounded px-1 py-0.5 hover:bg-gray-800/40"},
        span({class: "truncate font-mono text-[11px] text-gray-500"}, key),
        span({class: "whitespace-pre-wrap break-all font-mono text-[11px] text-gray-200"}, String(value)),
        key === 'time' || key === 'msg' ? span() : div(
            {class: "hidden gap-0.5 group-hover:flex"},
            button({
                type: "button", class: "cursor-pointer rounded bg-gray-800 px-1 text-[11px] text-gray-300 hover:text-green-400",
                title: `Filter for ${key}:${value}`, onclick: () => addFilter(key, value),
            }, "+"),
            button({
                type: "button", class: "cursor-pointer rounded bg-gray-800 px-1 text-[11px] text-gray-300 hover:text-red-400",
                title: `Filter out ${key}:${value}`, onclick: () => addFilter(key, value, true),
            }, "−"),
            button({
                type: "button",
                class: () => `cursor-pointer rounded bg-gray-800 px-1 ${columns.val.includes(key) ? 'text-brand' : 'text-gray-300 hover:text-gray-100'}`,
                title: "Toggle column", onclick: () => toggleColumn(key),
            }, columnsIcon({size: 11})),
        ),
    );

    const detailEl = (y, rec) => {
        const tabButton = (key, text) => button({
            type: "button",
            class: () => `cursor-pointer border-b-2 px-2 py-1 text-[11px] ${detailTab.val === key
                ? 'border-brand text-gray-100' : 'border-transparent text-gray-500 hover:text-gray-300'}`,
            onclick: () => { detailTab.val = key; },
        }, text);
        const kvPairs = [
            ['time', new Date(rec.ts).toISOString()],
            ['level', rec.level],
            ['version', rec.version ? String(rec.version) : ''],
            ['node', rec.node ? String(rec.node) : ''],
            ['run', rec.run ? String(rec.run) : ''],
            ['instance', String(rec.instance)],
            ['stream', rec.stream],
            ...Object.entries(rec.fields).filter(([k]) => !META_FIELDS.has(k)),
            ['msg', rec.msg],
        ];
        return div(
            {style: `position:absolute;top:${y}px;left:0;right:0;`},
            div(
                {"data-testid": "logs-detail",
                    class: "sticky left-0 z-10 flex flex-col overflow-hidden border-y border-gray-600 bg-gray-950 shadow-xl",
                    style: `height:${DETAIL_H}px;width:${scroller.clientWidth}px`,
                    onclick: (e) => e.stopPropagation()},
                div(
                    {class: "flex flex-none items-center gap-1 border-b border-gray-800 bg-gray-900/60 px-2"},
                    tabButton('fields', 'Fields'),
                    tabButton('json', 'JSON'),
                    div({class: "flex-1"}),
                    button({
                        type: "button",
                        class: "cursor-pointer rounded border border-gray-700 px-2 py-0.5 text-[11px] text-gray-300 hover:bg-gray-800",
                        "data-testid": "logs-context-button",
                        onclick: () => { context.val = {rec}; },
                    }, "View in context"),
                    button({
                        type: "button", class: "cursor-pointer rounded p-1 text-gray-500 hover:bg-gray-800 hover:text-gray-200",
                        title: () => jsonCopied.val ? "Copied" : "Copy JSON",
                        onclick: async () => {
                            await navigator.clipboard.writeText(recordJson(rec));
                            jsonCopied.val = true;
                            setTimeout(() => { jsonCopied.val = false; }, 1500);
                        },
                    }, () => jsonCopied.val ? checkIcon({class: "w-3.5 h-3.5 text-green-400"}) : copyIcon({class: "w-3.5 h-3.5"})),
                    button({
                        type: "button", class: "cursor-pointer rounded p-1 text-gray-500 hover:bg-gray-800 hover:text-gray-200",
                        "aria-label": "Close details", onclick: () => { expanded.val = null; },
                    }, xIcon({size: 13})),
                ),
                div(
                    {class: "app-scroll min-h-0 flex-1 overflow-y-auto p-2"},
                    () => detailTab.val === 'json'
                        ? pre({class: "whitespace-pre-wrap break-all font-mono text-[11px] leading-4 text-gray-200"}, recordJson(rec))
                        : div(...kvPairs.filter(([, v]) => v !== undefined && v !== '').map(([k, v]) => detailFieldRow(rec, k, v))),
                ),
            ),
        );
    };

    const rowEl = (pos, y, rowH, lines, cols, template, rec) => {
        const exp = expanded.val;
        const isExp = exp && exp.pos === pos;
        const tint = rec.level === 'ERROR' ? '#c42121' : rec.level === 'WARN' ? '#c67b04' : '';
        return div(
            {"data-testid": "logs-row",
                class: `grid cursor-pointer items-start overflow-hidden border-b border-gray-800/40 text-[11px] leading-[15px] ${isExp ? 'bg-gray-800/70' : 'hover:bg-gray-800/40'}`,
                style: `position:absolute;top:${y}px;left:0;right:0;height:${rowH}px;grid-template-columns:${template}`,
                onclick: () => toggleExpand(pos)},
            tint ? span({style: `position:absolute;left:0;top:0;bottom:0;width:2px;background:${tint};opacity:.7`}) : '',
            ...cols.map(k => cellEl(k, rec, lines)),
        );
    };

    // msgColumns is the message column's width in characters: the grid gives it
    // whatever the fixed columns leave over (floored at its minmax minimum),
    // less px-2 and the cell's right border.
    const msgColumns = (cols) => {
        const i = cols.indexOf('msg');
        if (i < 0) return 0;
        let fixed = 0;
        for (const k of cols) {
            const d = COLUMN_DEFS[k] || {px: 110};
            if (!d.flex) fixed += d.px;
        }
        const innerW = Math.max(scroller.clientWidth, colMinWidth(cols));
        const cellW = Math.max(COLUMN_DEFS.msg.minPx, innerW - fixed);
        const textW = cellW - 16 - (i === cols.length - 1 ? 0 : 1);
        return Math.max(1, Math.floor(textW / charWidth()));
    };

    // rowLayout caches the prefix sum of row heights. It is rebuilt only when
    // something that can change a height changes — result set, columns, wrap,
    // or the resulting message width — never per scroll frame.
    let layout = null;
    const rowLayout = () => {
        const res = result.val;
        const records = res?.records || [];
        const cols = columns.val;
        const wrapped = wrap.val;
        const msgCols = wrapped ? msgColumns(cols) : 0;
        const sig = `${cols.join(',')}|${wrapped}|${msgCols}`;
        if (layout && layout.res === res && layout.sig === sig) return layout;
        const len = records.length;
        const offsets = new Float64Array(len + 1);
        const lines = new Uint8Array(len);
        for (let i = 0; i < len; i++) {
            const n = msgCols > 0 ? wrappedLines(records[i].msg, msgCols) : newlineLines(records[i].msg);
            lines[i] = n;
            offsets[i + 1] = offsets[i] + rowHeight(n);
        }
        layout = {res, sig, offsets, lines};
        return layout;
    };

    let renderQueued = false;
    let pendingScrollBottom = false;
    const renderRows = () => {
        renderQueued = false;
        const res = result.val;
        const cols = columns.val;
        const template = colTemplate(cols);
        const minW = colMinWidth(cols);
        inner.style.minWidth = `${minW}px`;
        headerRow.style.gridTemplateColumns = template;
        headerRow.replaceChildren(...cols.map(headerCell));

        const records = res?.records || [];
        const len = records.length;
        const exp = expanded.val;
        const {offsets, lines} = rowLayout();
        rowsHost.style.height = `${offsets[len] + (exp ? DETAIL_H : 0)}px`;
        if (pendingScrollBottom) {
            pendingScrollBottom = false;
            scroller.scrollTop = scroller.scrollHeight;
        }
        if (len === 0) {
            rowsHost.replaceChildren(div(
                {class: "flex h-24 items-center justify-center text-xs text-gray-600"},
                searching.val ? 'Searching…' : (res ? 'No events match.' : ''),
            ));
            return;
        }
        // Work in row-space: below the open detail pane, scroll offsets carry
        // its height, so take that back off before locating the window.
        const stAdj = Math.max(0, scroller.scrollTop - HEADER_H);
        const topY = exp && stAdj > offsets[exp.pos + 1] ? Math.max(0, stAdj - DETAIL_H) : stAdj;
        const botY = topY + scroller.clientHeight + (exp ? DETAIL_H : 0);
        const first = Math.max(0, posAt(offsets, len, topY) - OVERSCAN);
        const last = Math.min(len, posAt(offsets, len, botY) + 1 + OVERSCAN);

        const children = [];
        for (let pos = first; pos < last; pos++) {
            const y = offsets[pos] + (exp && pos > exp.pos ? DETAIL_H : 0);
            // records are oldest first: the last position is the newest match.
            children.push(rowEl(pos, y, offsets[pos + 1] - offsets[pos], lines[pos], cols, template, records[pos]));
        }
        // The pane is taller than a row, so it can be on screen while its own
        // row has already scrolled past the top.
        if (exp && exp.pos < len) {
            const detailY = offsets[exp.pos + 1];
            if (detailY <= botY && detailY + DETAIL_H >= topY) children.push(detailEl(detailY, records[exp.pos]));
        }
        rowsHost.replaceChildren(...children);
    };

    const scheduleRender = () => {
        if (renderQueued) return;
        renderQueued = true;
        requestAnimationFrame(() => {
            renderRows();
            renderHistogram();
        });
    };

    scroller.onscroll = () => {
        if (renderQueued) return;
        renderQueued = true;
        requestAnimationFrame(renderRows);
    };
    window.addEventListener('resize', scheduleRender);

    van.derive(() => {
        void result.val; void columns.val; void wrap.val; void expanded.val;
        void searching.val; void detailTab.val; void jsonCopied.val;
        scheduleRender();
    });

    // --- context view --------------------------------------------------------
    // No row addressing exists server-side: context is a pair of unfiltered
    // time-range queries centred on the anchor's timestamp — newest-limit
    // before it, oldest-limit after it — extended the same way on demand.

    const contextQuery = async (startMs, endMs, order) => {
        const resp = await capi.postV1DeploymentsLogQuery({
            ...scopePayload(),
            timeStart: new Date(startMs),
            timeEnd: new Date(endMs),
            limit: CONTEXT_LIMIT,
            order,
            histogramBuckets: 0,
        });
        const recs = (resp.records || []).map(wrapRecord);
        if (order === 'desc') recs.reverse();  // oldest first for display
        return recs;
    };

    const contextPanel = (ctx) => {
        const anchor = ctx.rec;
        let loTs = anchor.ts + 1;   // exclusive upper bound of the loaded "before" block
        let hiTs = anchor.ts + 1;   // inclusive lower bound of the next "after" block
        let anchorEl;

        const lineEl = (rec) => {
            const isAnchor = rec.ts === anchor.ts && rec.msg === anchor.msg && !anchorEl;
            const el = div(
                {class: `whitespace-pre-wrap break-all px-3 ${isAnchor ? 'border-l-2 border-amber-400 bg-amber-500/10' : 'border-l-2 border-transparent'}`},
                span({class: "text-gray-500"}, fmtClock(rec.ts)), '  ',
                rec.level ? span({class: levelMeta(rec.level).text}, rec.level.padEnd(5)) : span({class: "text-gray-600"}, '·    '),
                ' ',
                span({class: "text-gray-200"}, rec.msg),
            );
            if (isAnchor) anchorEl = el;
            return el;
        };

        const linesHost = div({class: "py-1"});
        const body = div({class: "app-scroll min-h-0 flex-1 overflow-y-auto font-mono text-[11px] leading-5"});
        const loading = div({class: "px-3 py-2 text-[11px] text-gray-500"}, "Loading context…");

        const moreButton = (text, onclick) => button({
            type: "button",
            class: "block w-full cursor-pointer py-1 text-center text-[11px] text-gray-500 hover:bg-gray-900 hover:text-gray-300 disabled:opacity-30",
            onclick,
        }, text);

        const earlierButton = moreButton("· · · load earlier", async () => {
            earlierButton.disabled = true;
            const first = linesHost.firstChild?._rec;
            const end = first ? first.ts : loTs;
            const add = await contextQuery(end - CONTEXT_WINDOW, end, 'desc');
            const before = body.scrollHeight;
            linesHost.prepend(...add.map(r => { const el = lineEl(r); el._rec = r; return el; }));
            body.scrollTop += body.scrollHeight - before;
            earlierButton.disabled = add.length === 0;
        });
        const laterButton = moreButton("load later · · ·", async () => {
            laterButton.disabled = true;
            const start = hiTs;
            const add = await contextQuery(start, start + CONTEXT_WINDOW, 'asc');
            for (const r of add) {
                const el = lineEl(r);
                el._rec = r;
                linesHost.append(el);
                hiTs = Math.max(hiTs, r.ts + 1);
            }
            laterButton.disabled = add.length === 0;
        });
        earlierButton.disabled = true;
        laterButton.disabled = true;
        body.append(earlierButton, loading, linesHost, laterButton);

        (async () => {
            try {
                const [before, after] = await Promise.all([
                    contextQuery(anchor.ts + 1 - CONTEXT_WINDOW, anchor.ts + 1, 'desc'),
                    contextQuery(anchor.ts + 1, anchor.ts + 1 + CONTEXT_WINDOW, 'asc'),
                ]);
                loading.remove();
                for (const r of before) {
                    const el = lineEl(r);
                    el._rec = r;
                    linesHost.append(el);
                }
                if (!anchorEl && linesHost.lastChild) anchorEl = linesHost.lastChild;
                for (const r of after) {
                    const el = lineEl(r);
                    el._rec = r;
                    linesHost.append(el);
                    hiTs = Math.max(hiTs, r.ts + 1);
                }
                earlierButton.disabled = before.length === 0;
                laterButton.disabled = false;
                anchorEl?.scrollIntoView({block: 'center'});
            } catch (e) {
                loading.textContent = `Failed loading context: ${e.message || e}`;
            }
        })();

        const overlay = div(
            {"data-testid": "logs-context",
                class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-6",
                tabindex: "-1",
                onkeydown: (e) => { if (e.key === 'Escape') context.val = null; },
                onclick: (e) => { if (e.target === overlay) context.val = null; }},
            div(
                {class: "flex h-[82vh] w-full max-w-5xl flex-col overflow-hidden rounded border border-gray-700 bg-gray-950 shadow-2xl"},
                div(
                    {class: "flex flex-none items-center gap-2 border-b border-gray-800 bg-gray-900/60 px-3 py-1.5"},
                    span({class: "text-xs text-gray-200"}, `Context around ${fmtRowTime(anchor.ts)}`),
                    span({class: "text-[10px] text-gray-500"}, "search filters do not apply here"),
                    div({class: "flex-1"}),
                    button({
                        type: "button", class: "cursor-pointer rounded p-1 text-gray-500 hover:bg-gray-800 hover:text-gray-200",
                        "aria-label": "Close context view", onclick: () => { context.val = null; },
                    }, xIcon({size: 14})),
                ),
                body,
            ),
        );
        setTimeout(() => overlay.focus(), 0);
        return overlay;
    };

    // --- assemble ------------------------------------------------------------

    return div(
        {class: "flex h-full min-h-0 flex-col overflow-hidden bg-surface"},
        searchBar,
        histogramBand,
        div(
            {class: "flex min-h-0 flex-1"},
            () => sidebar(),
            div({class: "flex min-w-0 flex-1 flex-col"}, scroller),
        ),
        () => context.val ? contextPanel(context.val) : '',
    );
}
