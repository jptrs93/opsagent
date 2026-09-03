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

const {button, div, option, p, select, span, table, tbody, td, th, thead, tr} = van.tags;

const DEFAULT_PRESET = '1h';
const HIDDEN_SPACES_KEY = 'opsagent_metrics_hidden_spaces';

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

function deploymentLabel(item, machines) {
    const cfg = item?.config || {};
    const node = nodeDisplayName(cfg.def?.nodeId, machines);
    return [node, cfg.def?.name].filter(Boolean).join(' / ') || `#${cfg.id}`;
}

const deploymentSpaceID = (item) => item?.config?.def?.spaceId || 0;
const selectedDeployment = (items, id) => items.find(item => item.config?.id === id) || null;

const rateOf = (entry, field) => {
    const r = (entry.rates || []).find(x => x.field === field);
    return r ? Number(r.perSecond) : NaN;
};
const gaugeOf = (entry, field) => {
    const v = entry.sample?.[field];
    return v == null ? NaN : Number(v);
};

export function metricsPage(selectedMetricsDeploymentId) {
    const hiddenSpaces = van.state(loadHiddenSpaces());
    const deploymentId = van.state(selectedMetricsDeploymentId?.val || 0);
    const range = van.state({kind: 'preset', key: DEFAULT_PRESET});
    const split = van.state(false);
    const result = van.state(null);
    const latest = van.state(null);
    const loading = van.state(false);
    const errorMsg = van.state('');
    let queryGen = 0;

    const liveDeployments = () => (deploymentsS.val || []).filter(item => item.config?.id && !deploymentDeleted(item.config));
    const visibleDeployments = () => liveDeployments().filter(item => !hiddenSpaces.val.has(deploymentSpaceID(item)));

    const runQuery = async () => {
        const id = Number(deploymentId.val || 0);
        const gen = ++queryGen;
        loading.val = true;
        errorMsg.val = '';
        try {
            if (!id) {
                const resp = await capi.postV1MetricsLatest({});
                if (gen !== queryGen) return;
                latest.val = {entries: resp.entries || [], warnings: resp.warnings || [], fetchedAt: Date.now()};
                window.__metricsLatest = latest.val;
            } else {
                const {startTs, endTs} = resolveRange(range.val);
                const t0 = performance.now();
                const resp = await capi.postV1MetricsQuery({
                    deploymentId: id,
                    timeStart: new Date(startTs),
                    timeEnd: new Date(endTs),
                    fields: QUERY_FIELDS,
                });
                if (gen !== queryGen) return;
                result.val = {
                    deploymentId: id,
                    resp,
                    scanned: Number(resp.scannedRows || 0),
                    tookMs: Number(resp.tookMs || 0),
                    feMs: Math.round(performance.now() - t0),
                    warnings: resp.warnings || [],
                    ...buildChartData(resp, split.val),
                };
                window.__metricsResult = result.val;
            }
            loading.val = false;
        } catch (e) {
            if (gen !== queryGen) return;
            loading.val = false;
            errorMsg.val = `Query failed: ${e.message || e}`;
        }
    };

    const refresh = () => { void runQuery(); };

    van.derive(() => {
        void deploymentId.val;
        void loginS.val;
        result.val = null;
        setTimeout(refresh, 0);
    });
    van.derive(() => {
        const s = split.val;
        const r = result.rawVal;
        if (r) result.val = {...r, ...buildChartData(r.resp, s)};
    });

    if (selectedMetricsDeploymentId) {
        van.derive(() => {
            if (selectedMetricsDeploymentId.val && selectedMetricsDeploymentId.val !== deploymentId.rawVal) {
                deploymentId.val = selectedMetricsDeploymentId.val;
                const item = selectedDeployment(deploymentsS.rawVal || [], selectedMetricsDeploymentId.val);
                const sid = deploymentSpaceID(item);
                if (item && hiddenSpaces.rawVal.has(sid)) {
                    const next = new Set(hiddenSpaces.rawVal);
                    next.delete(sid);
                    hiddenSpaces.val = next;
                    saveHiddenSpaces(next);
                }
            }
        });
    }

    const spaceFilter = spacesFilter({
        hiddenS: hiddenSpaces,
        onChange: saveHiddenSpaces,
        testid: "metrics-space-filter",
        buttonClass: "input inline-flex h-[30px] items-center gap-1.5 text-xs text-gray-200 cursor-pointer hover:bg-gray-700",
    });

    const deploymentSelect = select({
        "data-testid": "metrics-deployment-select",
        class: "input h-[30px] min-w-48 py-1 text-xs",
        onchange: (e) => { deploymentId.val = Number(e.target.value || 0); },
    });

    van.derive(() => {
        const filtered = visibleDeployments();
        if (deploymentId.val && filtered.length > 0 && !selectedDeployment(filtered, Number(deploymentId.val))) {
            deploymentId.val = 0;
        }
        deploymentSelect.replaceChildren(
            option({value: ""}, "All deployments"),
            ...filtered.map(item => option({value: String(item.config.id)}, deploymentLabel(item, machinesS.val))),
        );
        deploymentSelect.value = String(deploymentId.val || '');
    });

    const toggle = (testid, labelText, stateS, title) => button({
        "data-testid": testid,
        type: "button",
        title,
        "aria-pressed": () => String(stateS.val),
        class: () => `inline-flex h-[30px] cursor-pointer items-center rounded-[0.3rem] border px-2.5 text-xs transition-colors ${stateS.val
            ? 'border-brand/60 bg-brand/15 text-blue-300' : 'border-gray-600 bg-gray-800 text-gray-400 hover:text-gray-200'}`,
        onclick: () => { stateS.val = !stateS.val; },
    }, labelText);

    const toolbar = div(
        {class: "flex-none border-b border-gray-700"},
        div(
            {class: "flex items-center gap-1.5 px-2 py-1.5"},
            spaceFilter,
            deploymentSelect,
            () => Number(deploymentId.val || 0) ? toggle("metrics-split-toggle", "Split by run", split, "One line per instance run instead of the sum") : '',
            div({class: "flex-1"}),
            () => Number(deploymentId.val || 0) ? timeRangePicker({rangeS: range, testid: "metrics", onChange: refresh}) : '',
            button({
                "data-testid": "metrics-refresh-button",
                type: "button",
                class: "inline-flex h-[30px] cursor-pointer items-center rounded-[0.3rem] border border-brand bg-brand px-3 text-xs font-medium text-white transition-colors hover:bg-blue-600 disabled:cursor-default disabled:opacity-50",
                disabled: () => loading.val,
                onclick: refresh,
            }, () => loading.val ? 'Loading…' : 'Refresh'),
        ),
    );

    const statusLine = () => {
        if (errorMsg.val) return errorMsg.val;
        const id = Number(deploymentId.val || 0);
        if (!id) {
            const l = latest.val;
            if (!l) return loading.val ? 'Loading…' : 'No data yet.';
            let text = `${l.entries.length} running container${l.entries.length === 1 ? '' : 's'} · sampled every 10 s`;
            if (l.warnings.length) text += ` · ${l.warnings[0]}`;
            return text;
        }
        const res = result.val;
        if (!res) return loading.val ? 'Loading…' : 'No data yet.';
        let text = `${res.scanned.toLocaleString()} samples in ${res.tookMs} ms (fe ${res.feMs} ms) · step ${res.stepMs >= 60_000 ? `${res.stepMs / 60_000} min` : `${res.stepMs / 1000} s`} · ${res.runs.length} run${res.runs.length === 1 ? '' : 's'}`;
        if (res.warnings.length) text += ` · ${res.warnings[0]}`;
        return text;
    };

    const numCell = (v, unit, extra = '') => td({class: `px-2 py-1 text-right tabular-nums ${extra}`}, formatValue(v, unit));

    const overviewTable = () => {
        const l = latest.val;
        if (!l) return '';
        const items = liveDeployments();
        const hidden = hiddenSpaces.val;
        const rows = l.entries.map(e => {
            const item = selectedDeployment(items, Number(e.sample?.deploymentId || 0));
            return {entry: e, item, name: item?.config?.def?.name || `#${e.sample?.deploymentId}`, spaceId: deploymentSpaceID(item)};
        }).filter(r => !r.item || !hidden.has(r.spaceId));
        rows.sort((a, b) => a.name.localeCompare(b.name) || Number(a.entry.sample.run) - Number(b.entry.sample.run));
        if (rows.length === 0) {
            return div({"data-testid": "metrics-overview-empty", class: "p-6 text-center text-xs text-gray-500"}, "No running containers are reporting metrics.");
        }
        const hdr = (t, right = false) => th({class: `px-2 py-1 text-[10px] font-medium uppercase tracking-wide text-gray-500 ${right ? 'text-right' : 'text-left'}`}, t);
        return table(
            {"data-testid": "metrics-overview-table", class: "w-full border-collapse text-xs"},
            thead(tr({class: "border-b border-gray-800"},
                hdr("Deployment"), hdr("Node"), hdr("Run"),
                hdr("CPU", true), hdr("Memory", true), hdr("Net rx", true), hdr("Net tx", true),
                hdr("Disk read", true), hdr("Disk write", true), hdr("PIDs", true), hdr("TCP est", true), hdr("Age", true))),
            tbody(...rows.map(r => {
                const s = r.entry.sample;
                const age = Math.max(0, Math.round((l.fetchedAt - Number(s.time)) / 1000));
                return tr({
                    "data-testid": `metrics-overview-row-${r.name}`,
                    class: "cursor-pointer border-b border-gray-800/60 hover:bg-gray-800/40",
                    onclick: () => { if (r.item) deploymentId.val = r.item.config.id; },
                },
                    td({class: "px-2 py-1"}, span({class: "flex items-center gap-1.5"}, spaceDot(r.spaceId), span({class: "text-gray-200"}, r.name))),
                    td({class: "px-2 py-1 text-gray-400"}, nodeDisplayName(s.nodeId, machinesS.val)),
                    td({class: "px-2 py-1 font-mono text-gray-500"}, runLabel(s)),
                    numCell(rateOf(r.entry, 'cpu_usage_usec') / 1e6, 'cores', 'text-gray-200'),
                    numCell(gaugeOf(r.entry, 'memCurrent'), 'bytes', 'text-gray-200'),
                    numCell(rateOf(r.entry, 'net_rx_bytes'), 'bytes/s'),
                    numCell(rateOf(r.entry, 'net_tx_bytes'), 'bytes/s'),
                    numCell(rateOf(r.entry, 'io_read_bytes'), 'bytes/s'),
                    numCell(rateOf(r.entry, 'io_write_bytes'), 'bytes/s'),
                    numCell(gaugeOf(r.entry, 'pids'), 'count'),
                    numCell(gaugeOf(r.entry, 'tcpEstablished'), 'count'),
                    td({class: "px-2 py-1 text-right tabular-nums text-gray-500"}, `${age}s`),
                );
            })),
        );
    };

    const chartStates = Object.fromEntries(CHARTS.map(c => [c.key, van.state(null)]));
    van.derive(() => {
        const res = result.val;
        for (const c of CHARTS) chartStates[c.key].val = res ? res.charts[c.key] : null;
    });
    const chartGrid = div(
        {"data-testid": "metrics-charts", class: "grid grid-cols-1 gap-2 p-2 xl:grid-cols-2"},
        ...CHARTS.map(c => lineChart({title: c.title, unit: c.unit, dataS: chartStates[c.key], testid: `metrics-chart-${c.key}`})),
    );

    const body = div(
        {class: "app-scroll min-h-0 flex-1 overflow-y-auto"},
        () => Number(deploymentId.val || 0) ? chartGrid : div({class: "p-2"}, overviewTable),
    );

    const root = div(
        {"data-testid": "metrics-page", class: "flex h-full min-h-0 flex-col"},
        toolbar,
        div(
            {class: "flex-none border-b border-gray-800 bg-gray-950/40 px-2 py-1"},
            p({class: "min-w-0 truncate text-[11px] text-gray-500", "aria-live": "polite", "data-testid": "metrics-status"}, statusLine),
        ),
        body,
    );

    return root;
}
