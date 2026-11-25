import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {loginS} from "../state/login.js";
import {deploymentsS, machinesS} from "../state/deployments.js";
import {nodeDisplayName} from "../lib/machines.js";
import {deploymentDeleted} from "../lib/deployment.js";
import {spacesFilter, spaceDot} from "../components/spacesFilter.js";
import {resolveRange, timeRangePicker} from "../components/timeRangePicker.js";
import {formatValue, lineChart} from "../components/lineChart.js";
import {CHARTS, NODE_CHARTS, NODE_QUERY_FIELDS, QUERY_FIELDS, buildChartData, buildNodeChartData, idleVolumes, placementKey, runLabel, volumesByPlacement} from "../lib/metricsData.js";
import {latestByNode, nodeOverviewRow, usageTone} from "../lib/nodeMetrics.js";
import {sortArrowIcon} from "../lib/icons.js";
import {tabButton, tabPanel, tabStrip} from "../components/tabs.js";

const {button, div, p, span, table, tbody, td, th, thead, tr} = van.tags;

const OVERVIEW = 'overview';
const NODES = 'nodes';
const DEFAULT_PRESET = '1h';
const STALE_MS = 10_000;
const HIDDEN_SPACES_KEY = 'opsagent_metrics_hidden_spaces';
const OVERVIEW_SORT_KEY = 'opsagent_metrics_overview_sort';
const NODES_SORT_KEY = 'opsagent_metrics_nodes_sort';

const nodeTabId = (nodeId) => `node-${nodeId}`;

const OVERVIEW_COLUMNS = [
    {key: 'name', label: 'Deployment', value: r => r.name},
    {key: 'node', label: 'Node', value: r => r.node},
    {key: 'run', label: 'Run', value: r => r.run},
    {key: 'cpu', label: 'CPU', num: true, value: r => r.cpu},
    {key: 'mem', label: 'Memory', num: true, value: r => r.mem},
    {key: 'rx', label: 'Net rx', num: true, value: r => r.rx},
    {key: 'tx', label: 'Net tx', num: true, value: r => r.tx},
    {key: 'read', label: 'Disk read', num: true, value: r => r.read},
    {key: 'write', label: 'Disk write', num: true, value: r => r.write},
    {key: 'volume', label: 'Volume', num: true, value: r => r.volume},
    {key: 'pids', label: 'PIDs', num: true, value: r => r.pids},
    {key: 'tcp', label: 'TCP est', num: true, value: r => r.tcp},
    {key: 'age', label: 'Age', num: true, value: r => r.age},
];

const NODE_COLUMNS = [
    {key: 'name', label: 'Node', value: r => r.name},
    {key: 'cpu', label: 'CPU', num: true, value: r => r.cpu},
    {key: 'cpuPct', label: 'CPU %', num: true, value: r => r.cpuPct},
    {key: 'load', label: 'Load', num: true, value: r => r.load},
    {key: 'memUsed', label: 'Memory', num: true, value: r => r.memUsed},
    {key: 'memPct', label: 'Mem %', num: true, value: r => r.memPct},
    {key: 'diskUsed', label: 'Disk', num: true, value: r => r.diskUsed},
    {key: 'diskPct', label: 'Disk %', num: true, value: r => r.diskPct},
    {key: 'rx', label: 'Net rx', num: true, value: r => r.rx},
    {key: 'tx', label: 'Net tx', num: true, value: r => r.tx},
    {key: 'read', label: 'Disk read', num: true, value: r => r.read},
    {key: 'write', label: 'Disk write', num: true, value: r => r.write},
    {key: 'age', label: 'Age', num: true, value: r => r.age},
];

function loadSort(storageKey, columns) {
    try {
        const raw = localStorage.getItem(storageKey);
        if (raw) {
            const parsed = JSON.parse(raw);
            if (columns.some(c => c.key === parsed?.key) && (parsed.dir === 'asc' || parsed.dir === 'desc')) return parsed;
        }
    } catch {}
    return {key: 'name', dir: 'asc'};
}

function saveSort(storageKey, sort) {
    try { localStorage.setItem(storageKey, JSON.stringify(sort)); } catch {}
}

const byName = (a, b) => a.name.localeCompare(b.name) || (a.runNumber || 0) - (b.runNumber || 0);

function compareRows(column, dir) {
    const sign = dir === 'desc' ? -1 : 1;
    return (a, b) => {
        const va = column.value(a);
        const vb = column.value(b);
        if (column.num) {
            const na = Number.isFinite(va);
            const nb = Number.isFinite(vb);
            if (na !== nb) return na ? -1 : 1;
            if (na && va !== vb) return sign * (va - vb);
        } else if (va !== vb) {
            return sign * va.localeCompare(vb);
        }
        return byName(a, b);
    };
}

function sortableColumns({columns, storageKey, testid}) {
    const sortS = van.state(loadSort(storageKey, columns));
    const setSort = (column) => {
        const cur = sortS.val;
        const next = cur.key === column.key
            ? {key: column.key, dir: cur.dir === 'asc' ? 'desc' : 'asc'}
            : {key: column.key, dir: column.num ? 'desc' : 'asc'};
        sortS.val = next;
        saveSort(storageKey, next);
    };
    const headCell = (column) => {
        const sort = sortS.val;
        const active = sort.key === column.key;
        return th({
            "data-testid": `${testid}-${column.key}`,
            class: `metrics-cell col-head group/th cursor-pointer select-none classic:text-[10px] classic:font-medium classic:tracking-wide ${column.num ? 'text-right' : 'text-left'} ${active ? 'text-gray-100' : 'hover:text-gray-300'}`,
            ...(active ? {"aria-sort": sort.dir === 'desc' ? 'descending' : 'ascending'} : {}),
            onclick: () => setSort(column),
        }, span({class: `inline-flex items-center gap-1 ${column.num ? 'flex-row-reverse' : ''}`},
            column.label,
            active
                ? sortArrowIcon({class: `h-2.5 w-2.5 text-brand ${sort.dir === 'desc' ? 'rotate-180' : ''}`})
                : sortArrowIcon({class: "h-2.5 w-2.5 text-gray-600 opacity-0 transition-opacity group-hover/th:opacity-100"})));
    };
    return {
        head: () => thead(tr({class: "border-b border-gray-800 slate:border-line graphite:border-0"}, ...columns.map(headCell))),
        sort: (rows) => {
            const sort = sortS.val;
            const column = columns.find(c => c.key === sort.key) || columns[0];
            return rows.sort(compareRows(column, sort.dir));
        },
    };
}

const numCell = (v, unit, extra = '') => td({class: `metrics-cell text-right tabular-nums ${extra}`}, formatValue(v, unit));

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

const deploymentSpaceID = (item) => item?.config?.deployment?.spaceId || 0;
const selectedDeployment = (items, id) => items.find(item => item.config?.deployment?.id === id) || null;

const rateOf = (entry, field) => {
    const r = (entry.rates || []).find(x => x.field === field);
    return r ? Number(r.perSecond) : NaN;
};
const gaugeOf = (entry, field) => {
    const v = entry.sample?.[field];
    return v == null ? NaN : Number(v);
};

const fmtStep = (ms) => ms >= 60_000 ? `${ms / 60_000} min` : `${ms / 1000} s`;

const fmtAgo = (ms) => {
    const s = Math.max(0, Math.round(ms / 1000));
    if (s < 90) return `${s} s ago`;
    if (s < 90 * 60) return `${Math.round(s / 60)} min ago`;
    return `${Math.round(s / 3600)} h ago`;
};

const volumeTitle = (v, fetchedAt) => v
    ? `${Number(v.inodes || 0).toLocaleString()} files and directories · measured ${fmtAgo(fetchedAt - Number(v.time))} in ${Number(v.walkMs || 0)} ms`
    : "No volume measured for this deployment on this node";

const volumeCell = (r, fetchedAt) => td(
    {class: "metrics-cell text-right tabular-nums", title: volumeTitle(r.volumeInfo, fetchedAt), "data-testid": `metrics-overview-volume-${r.name}`},
    formatValue(r.volume, 'bytes'));

function loader(query) {
    const loading = van.state(false);
    const errorMsg = van.state('');
    let gen = 0;
    let loadedAt = 0;
    let loadedLogin;
    let requestLogin;

    const load = async () => {
        const g = ++gen;
        requestLogin = loginS.rawVal;
        loading.val = true;
        errorMsg.val = '';
        try {
            const apply = await query();
            if (g !== gen) return;
            apply();
        } catch (e) {
            if (g !== gen) return;
            errorMsg.val = `Query failed: ${e.message || e}`;
        }
        loadedAt = Date.now();
        loadedLogin = requestLogin;
        loading.val = false;
    };

    const activate = () => {
        if (loading.rawVal && requestLogin === loginS.rawVal) return;
        if (!loadedAt || loadedLogin !== loginS.rawVal || Date.now() - loadedAt > STALE_MS) void load();
    };

    return {loading, errorMsg, load, activate};
}

const refreshButton = (loading, onclick) => button({
    "data-testid": "metrics-refresh-button",
    type: "button",
    class: "metrics-primary",
    disabled: () => loading.val,
    onclick,
}, () => loading.val ? 'Loading…' : 'Refresh');

const statusLine = (text, errorMsg) => p({
    class: () => `status-line ${errorMsg.val ? 'text-red-400' : ''}`,
    "aria-live": "polite",
    "data-testid": "metrics-status",
}, () => errorMsg.val || text());

function overviewView({hiddenSpaces, openDeployment}) {
    const latest = van.state(null);
    const {loading, errorMsg, load, activate} = loader(async () => {
        const resp = await capi.postV1MetricsLatest({});
        return () => {
            latest.val = {entries: resp.entries || [], volumes: resp.volumes || [], warnings: resp.warnings || [], fetchedAt: Date.now()};
            window.__metricsLatest = latest.val;
        };
    });
    const refresh = () => { void load(); };

    const liveDeployments = () => (deploymentsS.val || []).filter(item => item.config?.deployment?.id && !deploymentDeleted(item.config));

    const spaceFilter = spacesFilter({
        hiddenS: hiddenSpaces,
        onChange: saveHiddenSpaces,
        testid: "metrics-space-filter",
    });

    const status = () => {
        const l = latest.val;
        if (!l) return loading.val ? 'Loading…' : 'No data yet.';
        let text = `${l.entries.length} running container${l.entries.length === 1 ? '' : 's'} · sampled every 10 s`;
        if (l.volumes.length) text += ` · ${l.volumes.length} volume${l.volumes.length === 1 ? '' : 's'} measured every 30 min`;
        if (l.warnings.length) text += ` · ${l.warnings[0]}`;
        return text;
    };

    const columns = sortableColumns({columns: OVERVIEW_COLUMNS, storageKey: OVERVIEW_SORT_KEY, testid: "metrics-overview-sort"});

    const overviewTable = () => {
        const l = latest.val;
        if (!l) return '';
        const items = liveDeployments();
        const hidden = hiddenSpaces.val;
        const machines = machinesS.val;
        const volumes = volumesByPlacement(l.volumes);
        const rows = l.entries.map(e => {
            const s = e.sample;
            const deploymentId = Number(s?.deploymentId || 0);
            const item = selectedDeployment(items, deploymentId);
            const volumeInfo = volumes.get(placementKey(deploymentId, s?.nodeId));
            return {
                volumeInfo,
                volume: volumeInfo ? Number(volumeInfo.bytes) : NaN,
                entry: e, item, deploymentId,
                name: item?.config?.deployment?.name || `#${s?.deploymentId}`,
                spaceId: deploymentSpaceID(item),
                node: nodeDisplayName(s.nodeId, machines) || '',
                run: runLabel(s),
                runNumber: Number(s.run),
                cpu: rateOf(e, 'cpu_usage_usec') / 1e6,
                mem: gaugeOf(e, 'memCurrent'),
                rx: rateOf(e, 'net_rx_bytes'),
                tx: rateOf(e, 'net_tx_bytes'),
                read: rateOf(e, 'io_read_bytes'),
                write: rateOf(e, 'io_write_bytes'),
                pids: gaugeOf(e, 'pids'),
                tcp: gaugeOf(e, 'tcpEstablished'),
                age: Math.max(0, Math.round((l.fetchedAt - Number(s.time)) / 1000)),
            };
        });
        for (const v of idleVolumes(l.volumes, l.entries)) {
            const deploymentId = Number(v.deploymentId);
            const item = selectedDeployment(items, deploymentId);
            rows.push({
                idle: true, item, deploymentId,
                name: item?.config?.deployment?.name || `#${deploymentId}`,
                spaceId: deploymentSpaceID(item),
                node: nodeDisplayName(v.nodeId, machines) || '',
                run: 'not running', runNumber: 0,
                cpu: NaN, mem: NaN, rx: NaN, tx: NaN, read: NaN, write: NaN, pids: NaN, tcp: NaN, age: NaN,
                volumeInfo: v, volume: Number(v.bytes),
            });
        }
        const shown = rows.filter(r => !r.item || !hidden.has(r.spaceId));
        if (shown.length === 0) {
            return div({"data-testid": "metrics-overview-empty", class: "p-6 text-center text-xs text-gray-500"}, "No running containers are reporting metrics.");
        }
        return table(
            {"data-testid": "metrics-overview-table", class: "w-full border-collapse text-xs"},
            columns.head(),
            tbody(...columns.sort(shown).map(r => tr({
                "data-testid": r.idle ? `metrics-overview-idle-row-${r.name}` : `metrics-overview-row-${r.name}`,
                "data-deployment-id": String(r.deploymentId),
                class: "data-row cursor-pointer",
                onclick: () => {
                    if (r.deploymentId) openDeployment({deploymentId: r.deploymentId, name: r.name, spaceId: r.item ? r.spaceId : null});
                },
            },
                td({class: "metrics-cell"}, span({class: "flex items-center gap-1.5"}, spaceDot(r.spaceId), span({class: "text-gray-200"}, r.name))),
                td({class: "metrics-cell text-gray-400"}, r.node),
                td({class: `metrics-cell text-gray-500 ${r.idle ? '' : 'font-mono'}`}, r.run),
                numCell(r.cpu, 'cores', 'text-gray-200'),
                numCell(r.mem, 'bytes', 'text-gray-200'),
                numCell(r.rx, 'bytes/s'),
                numCell(r.tx, 'bytes/s'),
                numCell(r.read, 'bytes/s'),
                numCell(r.write, 'bytes/s'),
                volumeCell(r, l.fetchedAt),
                numCell(r.pids, 'count'),
                numCell(r.tcp, 'count'),
                td({class: "metrics-cell text-right tabular-nums text-gray-500"}, r.idle ? '–' : `${r.age}s`),
            ))),
        );
    };

    const content = div(
        {"data-testid": "metrics-panel-overview", class: "flex h-full min-h-0 flex-col"},
        div({class: "page-toolbar"},
            spaceFilter,
            div({class: "flex-1"}),
            refreshButton(loading, refresh)),
        statusLine(status, errorMsg),
        div({class: "app-scroll min-h-0 flex-1 overflow-y-auto classic:p-2"}, overviewTable),
    );
    return {content, activate};
}

function nodesOverviewView({openNode}) {
    const latest = van.state(null);
    const {loading, errorMsg, load, activate} = loader(async () => {
        const resp = await capi.postV1MetricsNodesLatest({});
        return () => {
            latest.val = {entries: resp.entries || [], byNode: latestByNode(resp), warnings: resp.warnings || [], fetchedAt: Date.now()};
            window.__metricsNodesLatest = latest.val;
        };
    });
    const refresh = () => { void load(); };

    const status = () => {
        const l = latest.val;
        if (!l) return loading.val ? 'Loading…' : 'No data yet.';
        let text = `${l.byNode.size} node${l.byNode.size === 1 ? '' : 's'} reporting · sampled every 10 s`;
        if (l.warnings.length) text += ` · ${l.warnings[0]}`;
        return text;
    };

    const columns = sortableColumns({columns: NODE_COLUMNS, storageKey: NODES_SORT_KEY, testid: "metrics-nodes-sort"});

    const pctCell = (v, toned) => td({class: "metrics-cell text-right tabular-nums"},
        span({class: "cluster-tone", "data-tone": toned ? usageTone(v) : ''}, formatValue(v, 'pct')));
    const ofTotal = (total) => Number.isFinite(total) ? `of ${formatValue(total, 'bytes')}` : '';

    const nodesTable = () => {
        const l = latest.val;
        if (!l) return '';
        const machines = (machinesS.val || []).filter(m => !m.evicted);
        const known = new Set(machines.map(m => Number(m.id)));
        const rows = machines.map(m => ({
            nodeId: Number(m.id),
            name: m.name || `node ${m.id}`,
            disconnected: !m.connected && !m.isPrimary,
            ...nodeOverviewRow(l.byNode.get(Number(m.id)), l.fetchedAt),
        }));
        for (const [nodeId, entry] of l.byNode) {
            if (!known.has(nodeId)) rows.push({nodeId, name: nodeDisplayName(nodeId, []), disconnected: false, ...nodeOverviewRow(entry, l.fetchedAt)});
        }
        if (rows.length === 0) {
            return div({"data-testid": "metrics-nodes-empty", class: "p-6 text-center text-xs text-gray-500"}, "No nodes are reporting metrics.");
        }
        return table(
            {"data-testid": "metrics-nodes-table", class: "w-full border-collapse text-xs"},
            columns.head(),
            tbody(...columns.sort(rows).map(r => tr({
                "data-testid": `metrics-nodes-row-${r.name}`,
                "data-node-id": String(r.nodeId),
                class: "data-row cursor-pointer",
                onclick: () => openNode({nodeId: r.nodeId, name: r.name}),
            },
                td({class: "metrics-cell"}, span({class: "flex items-center gap-1.5"},
                    span({class: "font-mono text-gray-200"}, r.name),
                    r.disconnected ? span({class: "cluster-tone", "data-tone": "error"}, "disconnected") : "")),
                td({class: "metrics-cell text-right tabular-nums text-gray-200", title: Number.isFinite(r.cpuCount) ? `of ${r.cpuCount} CPUs` : ''}, formatValue(r.cpu, 'cores')),
                pctCell(r.cpuPct, false),
                td({class: "metrics-cell text-right tabular-nums"}, Number.isFinite(r.load) ? r.load.toFixed(2) : '–'),
                td({class: "metrics-cell text-right tabular-nums text-gray-200", title: ofTotal(r.memTotal)}, formatValue(r.memUsed, 'bytes')),
                pctCell(r.memPct, true),
                td({class: "metrics-cell text-right tabular-nums text-gray-200", title: ofTotal(r.disk?.total)}, formatValue(r.diskUsed, 'bytes')),
                pctCell(r.diskPct, true),
                numCell(r.rx, 'bytes/s'),
                numCell(r.tx, 'bytes/s'),
                numCell(r.read, 'bytes/s'),
                numCell(r.write, 'bytes/s'),
                td({class: "metrics-cell text-right tabular-nums text-gray-500"}, Number.isFinite(r.age) ? `${r.age}s` : '–'),
            ))),
        );
    };

    const content = div(
        {"data-testid": "metrics-panel-nodes", class: "flex h-full min-h-0 flex-col"},
        div({class: "page-toolbar"},
            div({class: "flex-1"}),
            refreshButton(loading, refresh)),
        statusLine(status, errorMsg),
        div({class: "app-scroll min-h-0 flex-1 overflow-y-auto classic:p-2"}, nodesTable),
    );
    return {content, activate};
}

function nodeView({nodeId, isVisible}) {
    const range = van.state({kind: 'preset', key: DEFAULT_PRESET});
    const result = van.state(null);
    const publish = () => { if (isVisible() && result.rawVal) window.__metricsNodeResult = result.rawVal; };

    const {loading, errorMsg, load, activate} = loader(async () => {
        const {startTs, endTs} = resolveRange(range.rawVal);
        const t0 = performance.now();
        const resp = await capi.postV1MetricsNodesQuery({
            nodeId,
            timeStart: new Date(startTs),
            timeEnd: new Date(endTs),
            fields: NODE_QUERY_FIELDS,
        });
        const feMs = Math.round(performance.now() - t0);
        return () => {
            result.val = {
                nodeId,
                scanned: Number(resp.scannedRows || 0),
                tookMs: Number(resp.tookMs || 0),
                feMs,
                warnings: resp.warnings || [],
                ...buildNodeChartData(resp),
            };
            publish();
        };
    });
    const refresh = () => { void load(); };

    const status = () => {
        const res = result.val;
        if (!res) return loading.val ? 'Loading…' : 'No data yet.';
        let text = `${res.scanned.toLocaleString()} samples in ${res.tookMs} ms (fe ${res.feMs} ms) · step ${fmtStep(res.stepMs)}`;
        if (res.warnings.length) text += ` · ${res.warnings[0]}`;
        return text;
    };

    const chartStates = Object.fromEntries(NODE_CHARTS.map(c => [c.key, van.state(null)]));
    van.derive(() => {
        const res = result.val;
        for (const c of NODE_CHARTS) chartStates[c.key].val = res ? res.charts[c.key] : null;
    });

    const content = div(
        {"data-testid": `metrics-panel-${nodeTabId(nodeId)}`, class: "flex h-full min-h-0 flex-col"},
        div({class: "page-toolbar"},
            div({class: "flex-1"}),
            timeRangePicker({rangeS: range, testid: "metrics", onChange: refresh}),
            refreshButton(loading, refresh)),
        statusLine(status, errorMsg),
        div({class: "app-scroll min-h-0 flex-1 overflow-y-auto"},
            div({"data-testid": "metrics-node-charts", class: "metrics-grid"},
                ...NODE_CHARTS.map(c => lineChart({title: c.title, unit: c.unit, dataS: chartStates[c.key], testid: `metrics-node-chart-${c.key}`})))),
    );
    return {
        content,
        activate: () => { publish(); activate(); },
    };
}

function deploymentView({deploymentId, isVisible}) {
    const range = van.state({kind: 'preset', key: DEFAULT_PRESET});
    const split = van.state(false);
    const result = van.state(null);
    const publish = () => { if (isVisible() && result.rawVal) window.__metricsResult = result.rawVal; };

    const {loading, errorMsg, load, activate} = loader(async () => {
        const {startTs, endTs} = resolveRange(range.rawVal);
        const t0 = performance.now();
        const resp = await capi.postV1MetricsQuery({
            deploymentId,
            timeStart: new Date(startTs),
            timeEnd: new Date(endTs),
            fields: QUERY_FIELDS,
        });
        const feMs = Math.round(performance.now() - t0);
        return () => {
            result.val = {
                deploymentId,
                resp,
                scanned: Number(resp.scannedRows || 0),
                tookMs: Number(resp.tookMs || 0),
                feMs,
                warnings: resp.warnings || [],
                ...buildChartData(resp, split.rawVal),
            };
            publish();
        };
    });
    const refresh = () => { void load(); };

    van.derive(() => {
        const s = split.val;
        const r = result.rawVal;
        if (r) {
            result.val = {...r, ...buildChartData(r.resp, s)};
            publish();
        }
    });

    const status = () => {
        const res = result.val;
        if (!res) return loading.val ? 'Loading…' : 'No data yet.';
        let text = `${res.scanned.toLocaleString()} samples in ${res.tookMs} ms (fe ${res.feMs} ms) · step ${fmtStep(res.stepMs)} · ${res.runs.length} run${res.runs.length === 1 ? '' : 's'}`;
        if (res.warnings.length) text += ` · ${res.warnings[0]}`;
        return text;
    };

    const splitToggle = button({
        "data-testid": "metrics-split-toggle",
        type: "button",
        title: "One line per instance run instead of the sum",
        "aria-pressed": () => String(split.val),
        class: "metrics-toggle",
        onclick: () => { split.val = !split.val; },
    }, "Split by run");

    const chartStates = Object.fromEntries(CHARTS.map(c => [c.key, van.state(null)]));
    van.derive(() => {
        const res = result.val;
        for (const c of CHARTS) chartStates[c.key].val = res ? res.charts[c.key] : null;
    });

    const content = div(
        {"data-testid": `metrics-panel-${deploymentId}`, class: "flex h-full min-h-0 flex-col"},
        div({class: "page-toolbar"},
            splitToggle,
            div({class: "flex-1"}),
            timeRangePicker({rangeS: range, testid: "metrics", onChange: refresh}),
            refreshButton(loading, refresh)),
        statusLine(status, errorMsg),
        div({class: "app-scroll min-h-0 flex-1 overflow-y-auto"},
            div({"data-testid": "metrics-charts", class: "metrics-grid"},
                ...CHARTS.map(c => lineChart({title: c.title, unit: c.unit, dataS: chartStates[c.key], testid: `metrics-chart-${c.key}`})))),
    );
    return {
        content,
        activate: () => { publish(); activate(); },
    };
}

export function metricsPage({active = () => true} = {}) {
    const hiddenSpaces = van.state(loadHiddenSpaces());
    const tabs = van.state([]);
    const activeTab = van.state(OVERVIEW);
    const panelsHost = div({class: "page-body"});
    const views = new Map();
    let shown = null;

    const mountPanel = (id, view) => {
        const panel = tabPanel(activeTab, id, view.content);
        panelsHost.append(panel);
        views.set(id, {...view, panel});
    };

    const closeTab = (id) => {
        const index = tabs.val.findIndex(item => item.id === id);
        if (index < 0) return;
        views.get(id)?.panel.remove();
        views.delete(id);
        tabs.val = tabs.val.filter(item => item.id !== id);
        if (activeTab.val === id) {
            const neighbour = tabs.val[index] || tabs.val[index - 1];
            activeTab.val = neighbour ? neighbour.id : id.startsWith('node-') ? NODES : OVERVIEW;
        }
    };

    const openDeployment = ({deploymentId, name, spaceId}) => {
        const id = String(deploymentId);
        if (!views.has(id)) {
            mountPanel(id, deploymentView({deploymentId, isVisible: () => shown === id}));
            tabs.val = [...tabs.val, {id, title: name, spaceId}];
        }
        activeTab.val = id;
    };

    const openNode = ({nodeId, name}) => {
        const id = nodeTabId(nodeId);
        if (!views.has(id)) {
            mountPanel(id, nodeView({nodeId, isVisible: () => shown === id}));
            tabs.val = [...tabs.val, {id, title: name, spaceId: null}];
        }
        activeTab.val = id;
    };

    mountPanel(OVERVIEW, overviewView({hiddenSpaces, openDeployment}));
    mountPanel(NODES, nodesOverviewView({openNode}));

    van.derive(() => {
        void loginS.val;
        const id = active() ? activeTab.val : null;
        shown = id;
        if (id === null) return;
        setTimeout(() => {
            if (shown === id) views.get(id)?.activate();
        }, 0);
    });

    const strip = tabStrip({label: "Metrics"},
        tabButton({id: OVERVIEW, activeTab, title: "Deployments overview", testid: `metrics-tab-${OVERVIEW}`}),
        tabButton({id: NODES, activeTab, title: "Nodes overview", testid: `metrics-tab-${NODES}`}),
        () => div({class: "contents"}, ...tabs.val.map(item => tabButton({
            id: item.id,
            activeTab,
            title: item.title,
            testid: `metrics-tab-${item.id}`,
            lead: item.spaceId === null ? null : spaceDot(item.spaceId),
            mono: true,
            onClose: () => closeTab(item.id),
        }))),
    );

    return div(
        {class: "page", "data-testid": "metrics-page"},
        strip,
        panelsHost,
    );
}
