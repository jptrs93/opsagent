import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {loginS} from "../state/login.js";
import {deploymentsS, machinesS} from "../state/deployments.js";
import {nodeDisplayName} from "../lib/machines.js";
import {deploymentDeleted} from "../lib/deployment.js";
import {spacesFilter, spaceDot} from "../components/spacesFilter.js";
import {resolveRange, timeRangePicker} from "../components/timeRangePicker.js";
import {formatValue, lineChart} from "../components/lineChart.js";
import {CHARTS, QUERY_FIELDS, buildChartData, runLabel} from "../lib/metricsData.js";
import {sortArrowIcon} from "../lib/icons.js";
import {tabButton, tabPanel, tabStrip} from "../components/tabs.js";

const {button, div, p, span, table, tbody, td, th, thead, tr} = van.tags;

const OVERVIEW = 'overview';
const DEFAULT_PRESET = '1h';
const STALE_MS = 10_000;
const HIDDEN_SPACES_KEY = 'opsagent_metrics_hidden_spaces';
const OVERVIEW_SORT_KEY = 'opsagent_metrics_overview_sort';

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
    {key: 'pids', label: 'PIDs', num: true, value: r => r.pids},
    {key: 'tcp', label: 'TCP est', num: true, value: r => r.tcp},
    {key: 'age', label: 'Age', num: true, value: r => r.age},
];

function loadOverviewSort() {
    try {
        const raw = localStorage.getItem(OVERVIEW_SORT_KEY);
        if (raw) {
            const parsed = JSON.parse(raw);
            if (OVERVIEW_COLUMNS.some(c => c.key === parsed?.key) && (parsed.dir === 'asc' || parsed.dir === 'desc')) return parsed;
        }
    } catch {}
    return {key: 'name', dir: 'asc'};
}

function saveOverviewSort(sort) {
    try { localStorage.setItem(OVERVIEW_SORT_KEY, JSON.stringify(sort)); } catch {}
}

const byName = (a, b) => a.name.localeCompare(b.name) || a.runNumber - b.runNumber;

function compareOverviewRows(column, dir) {
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
            latest.val = {entries: resp.entries || [], warnings: resp.warnings || [], fetchedAt: Date.now()};
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
        if (l.warnings.length) text += ` · ${l.warnings[0]}`;
        return text;
    };

    const overviewSort = van.state(loadOverviewSort());
    const setOverviewSort = (column) => {
        const cur = overviewSort.val;
        const next = cur.key === column.key
            ? {key: column.key, dir: cur.dir === 'asc' ? 'desc' : 'asc'}
            : {key: column.key, dir: column.num ? 'desc' : 'asc'};
        overviewSort.val = next;
        saveOverviewSort(next);
    };

    const headCell = (column) => {
        const sort = overviewSort.val;
        const active = sort.key === column.key;
        return th({
            "data-testid": `metrics-overview-sort-${column.key}`,
            class: `metrics-cell col-head group/th cursor-pointer select-none classic:text-[10px] classic:font-medium classic:tracking-wide ${column.num ? 'text-right' : 'text-left'} ${active ? 'text-gray-100' : 'hover:text-gray-300'}`,
            ...(active ? {"aria-sort": sort.dir === 'desc' ? 'descending' : 'ascending'} : {}),
            onclick: () => setOverviewSort(column),
        }, span({class: `inline-flex items-center gap-1 ${column.num ? 'flex-row-reverse' : ''}`},
            column.label,
            active
                ? sortArrowIcon({class: `h-2.5 w-2.5 text-brand ${sort.dir === 'desc' ? 'rotate-180' : ''}`})
                : sortArrowIcon({class: "h-2.5 w-2.5 text-gray-600 opacity-0 transition-opacity group-hover/th:opacity-100"})));
    };

    const numCell = (v, unit, extra = '') => td({class: `metrics-cell text-right tabular-nums ${extra}`}, formatValue(v, unit));

    const overviewTable = () => {
        const l = latest.val;
        if (!l) return '';
        const items = liveDeployments();
        const hidden = hiddenSpaces.val;
        const machines = machinesS.val;
        const rows = l.entries.map(e => {
            const s = e.sample;
            const deploymentId = Number(s?.deploymentId || 0);
            const item = selectedDeployment(items, deploymentId);
            return {
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
        }).filter(r => !r.item || !hidden.has(r.spaceId));
        if (rows.length === 0) {
            return div({"data-testid": "metrics-overview-empty", class: "p-6 text-center text-xs text-gray-500"}, "No running containers are reporting metrics.");
        }
        const sort = overviewSort.val;
        const column = OVERVIEW_COLUMNS.find(c => c.key === sort.key) || OVERVIEW_COLUMNS[0];
        rows.sort(compareOverviewRows(column, sort.dir));
        return table(
            {"data-testid": "metrics-overview-table", class: "w-full border-collapse text-xs"},
            thead(tr({class: "border-b border-gray-800 slate:border-line graphite:border-0"}, ...OVERVIEW_COLUMNS.map(headCell))),
            tbody(...rows.map(r => tr({
                "data-testid": `metrics-overview-row-${r.name}`,
                "data-deployment-id": String(r.deploymentId),
                class: "data-row cursor-pointer",
                onclick: () => {
                    if (r.deploymentId) openDeployment({deploymentId: r.deploymentId, name: r.name, spaceId: r.item ? r.spaceId : null});
                },
            },
                td({class: "metrics-cell"}, span({class: "flex items-center gap-1.5"}, spaceDot(r.spaceId), span({class: "text-gray-200"}, r.name))),
                td({class: "metrics-cell text-gray-400"}, r.node),
                td({class: "metrics-cell font-mono text-gray-500"}, r.run),
                numCell(r.cpu, 'cores', 'text-gray-200'),
                numCell(r.mem, 'bytes', 'text-gray-200'),
                numCell(r.rx, 'bytes/s'),
                numCell(r.tx, 'bytes/s'),
                numCell(r.read, 'bytes/s'),
                numCell(r.write, 'bytes/s'),
                numCell(r.pids, 'count'),
                numCell(r.tcp, 'count'),
                td({class: "metrics-cell text-right tabular-nums text-gray-500"}, `${r.age}s`),
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
            activeTab.val = neighbour ? neighbour.id : OVERVIEW;
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

    mountPanel(OVERVIEW, overviewView({hiddenSpaces, openDeployment}));

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
        tabButton({id: OVERVIEW, activeTab, title: "Overview", testid: `metrics-tab-${OVERVIEW}`}),
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
