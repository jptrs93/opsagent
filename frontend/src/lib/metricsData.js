export const SERIES_COLORS = Array.from({length: 8}, (_, i) => `var(--chart-series-${i + 1})`);
const seriesColor = (slot) => SERIES_COLORS[slot % SERIES_COLORS.length];

export const CHARTS = [
    {key: 'cpu', title: 'CPU', unit: 'cores', lines: [
        {field: 'cpu_usage_usec', label: 'total', scale: 1e-6},
        {field: 'cpu_user_usec', label: 'user', scale: 1e-6},
        {field: 'cpu_system_usec', label: 'system', scale: 1e-6},
        {field: 'cpu_throttled_usec', label: 'throttled', scale: 1e-6},
    ]},
    {key: 'mem', title: 'Memory', unit: 'bytes', lines: [
        {field: 'mem_current', label: 'current'},
        {field: 'mem_anon', label: 'anon'},
        {field: 'mem_file', label: 'file'},
    ]},
    {key: 'net', title: 'Network', unit: 'bytes/s', lines: [
        {field: 'net_rx_bytes', label: 'rx'},
        {field: 'net_tx_bytes', label: 'tx'},
    ]},
    {key: 'io', title: 'Disk I/O', unit: 'bytes/s', lines: [
        {field: 'io_read_bytes', label: 'read'},
        {field: 'io_write_bytes', label: 'write'},
    ]},
    {key: 'volume', title: 'Volume', unit: 'bytes', lines: [
        {field: 'volume_bytes', label: 'used'},
    ]},
    {key: 'pids', title: 'Processes & file descriptors', unit: 'count', lines: [
        {field: 'pids', label: 'pids'},
        {field: 'open_fds', label: 'open fds'},
    ]},
    {key: 'tcp', title: 'TCP connections', unit: 'count', lines: [
        {field: 'tcp_established', label: 'established'},
        {field: 'tcp_time_wait', label: 'time_wait'},
        {field: 'tcp_close_wait', label: 'close_wait'},
    ]},
    {key: 'psi', title: 'Pressure stall (some, 10s avg)', unit: 'pct', lines: [
        {field: 'psi_cpu_some_avg10', label: 'cpu'},
        {field: 'psi_mem_some_avg10', label: 'memory'},
        {field: 'psi_io_some_avg10', label: 'io'},
    ]},
    {key: 'events', title: 'Events per bucket', unit: 'count', perBucket: true, lines: [
        {field: 'mem_oom_kill', label: 'oom kills'},
        {field: 'cpu_nr_throttled', label: 'throttle periods'},
        {field: 'net_rx_dropped', label: 'rx dropped'},
        {field: 'net_tx_dropped', label: 'tx dropped'},
    ]},
];
export const QUERY_FIELDS = [...new Set(CHARTS.flatMap(c => c.lines.map(l => l.field)))];

export const runLabel = (s) => `v${s.deploymentVersion} i${s.ordinal} r${s.run}`;
export const runKey = (s) => `${s.scheduledInstanceId}|${s.deploymentVersion}|${s.run}`;

export const HELD_KIND = 3;
const isHeld = (s) => Number(s.kind) === HELD_KIND;

function sumSeries(list, buckets, scale) {
    const values = new Array(buckets).fill(NaN);
    for (const s of list) {
        for (let b = 0; b < buckets; b++) {
            const v = s.values[b];
            if (!Number.isFinite(v)) continue;
            values[b] = (Number.isFinite(values[b]) ? values[b] : 0) + v * scale;
        }
    }
    return values;
}

export const placementKey = (deploymentId, nodeId) => `${Number(deploymentId || 0)}|${Number(nodeId || 0)}`;

export function volumesByPlacement(volumes) {
    const out = new Map();
    for (const v of volumes || []) out.set(placementKey(v.deploymentId, v.nodeId), v);
    return out;
}

export function idleVolumes(volumes, entries) {
    const running = new Set((entries || []).map(e => placementKey(e.sample?.deploymentId, e.sample?.nodeId)));
    return (volumes || []).filter(v => !running.has(placementKey(v.deploymentId, v.nodeId)));
}

export function buildChartData(resp, split) {
    const buckets = Number(resp.buckets || 0);
    const stepMs = Number(resp.stepMs || 0);
    const startTs = resp.timeStart instanceof Date ? resp.timeStart.getTime() : 0;
    const all = resp.series || [];
    const series = all.filter(s => !isHeld(s));
    const heldByField = new Map();
    for (const s of all) {
        if (!isHeld(s)) continue;
        if (!heldByField.has(s.field)) heldByField.set(s.field, []);
        heldByField.get(s.field).push(s);
    }
    const runs = [];
    const runIndex = new Map();
    for (const s of series) {
        const k = runKey(s);
        if (!runIndex.has(k)) { runIndex.set(k, runs.length); runs.push({key: k, label: runLabel(s)}); }
    }
    const byRunField = new Map();
    for (const s of series) byRunField.set(`${runKey(s)}|${s.field}`, s);
    const stepSec = stepMs / 1000;
    const out = {};
    for (const chart of CHARTS) {
        const lines = [];
        for (const [lineIdx, ln] of chart.lines.entries()) {
            const scale = (ln.scale || 1) * (chart.perBucket ? stepSec : 1);
            const held = heldByField.get(ln.field);
            if (held) {
                if (split && held.length > 1) {
                    for (const [i, s] of held.entries()) {
                        lines.push({label: `${ln.label} node ${Number(s.nodeId)}`, color: seriesColor(lineIdx * held.length + i), values: s.values.map(v => v * scale), field: ln.field});
                    }
                } else {
                    lines.push({label: ln.label, color: seriesColor(lineIdx), values: sumSeries(held, buckets, scale), field: ln.field});
                }
                continue;
            }
            if (split && runs.length > 1) {
                for (const [runIdx, run] of runs.entries()) {
                    const s = byRunField.get(`${run.key}|${ln.field}`);
                    if (!s) continue;
                    lines.push({label: `${ln.label} ${run.label}`, color: seriesColor(lineIdx * runs.length + runIdx), values: s.values.map(v => v * scale), field: ln.field});
                }
                continue;
            }
            const present = runs.map(run => byRunField.get(`${run.key}|${ln.field}`)).filter(Boolean);
            if (present.length) lines.push({label: ln.label, color: seriesColor(lineIdx), values: sumSeries(present, buckets, scale), field: ln.field});
        }
        out[chart.key] = lines.length ? {startTs, stepMs, buckets, series: lines} : null;
    }
    return {charts: out, runs, buckets, stepMs, startTs};
}

export const NODE_CHARTS = [
    {key: 'cpu', title: 'CPU', unit: 'cores', lines: [
        {field: 'cpu_usage_usec', label: 'total', scale: 1e-6},
        {field: 'cpu_user_usec', label: 'user', scale: 1e-6},
        {field: 'cpu_system_usec', label: 'system', scale: 1e-6},
        {field: 'cpu_iowait_usec', label: 'iowait', scale: 1e-6},
        {field: 'cpu_steal_usec', label: 'steal', scale: 1e-6},
    ]},
    {key: 'load', title: 'Load average', unit: 'count', lines: [
        {field: 'load1', label: '1 min'},
        {field: 'load5', label: '5 min'},
        {field: 'load15', label: '15 min'},
        {field: 'cpu_count', label: 'cpus'},
    ]},
    {key: 'mem', title: 'Memory', unit: 'bytes', lines: [
        {field: 'mem_used', label: 'used'},
        {field: 'mem_cached', label: 'cached'},
        {field: 'swap_used', label: 'swap used'},
        {field: 'mem_total', label: 'total'},
    ]},
    {key: 'disk', title: 'Data filesystem', unit: 'bytes', lines: [
        {field: 'fs_data_used_bytes', label: 'used'},
        {field: 'fs_data_total_bytes', label: 'total'},
    ]},
    {key: 'net', title: 'Network', unit: 'bytes/s', lines: [
        {field: 'net_rx_bytes', label: 'rx'},
        {field: 'net_tx_bytes', label: 'tx'},
    ]},
    {key: 'io', title: 'Disk I/O', unit: 'bytes/s', lines: [
        {field: 'io_read_bytes', label: 'read'},
        {field: 'io_write_bytes', label: 'write'},
    ]},
    {key: 'iops', title: 'Disk operations', unit: 'per_s', lines: [
        {field: 'io_read_ops', label: 'reads'},
        {field: 'io_write_ops', label: 'writes'},
    ]},
    {key: 'psi', title: 'Pressure stall (some, 10s avg)', unit: 'pct', lines: [
        {field: 'psi_cpu_some_avg10', label: 'cpu'},
        {field: 'psi_mem_some_avg10', label: 'memory'},
        {field: 'psi_io_some_avg10', label: 'io'},
    ]},
];
export const NODE_QUERY_FIELDS = [...new Set(NODE_CHARTS.flatMap(c => c.lines.map(l => l.field)))];

export function buildNodeChartData(resp) {
    const buckets = Number(resp.buckets || 0);
    const stepMs = Number(resp.stepMs || 0);
    const startTs = resp.timeStart instanceof Date ? resp.timeStart.getTime() : 0;
    const byField = new Map((resp.series || []).map(s => [s.field, s]));
    const charts = {};
    for (const chart of NODE_CHARTS) {
        const lines = [];
        for (const [lineIdx, ln] of chart.lines.entries()) {
            const s = byField.get(ln.field);
            if (!s) continue;
            const scale = ln.scale || 1;
            lines.push({label: ln.label, color: seriesColor(lineIdx), values: s.values.map(v => v * scale), field: ln.field});
        }
        charts[chart.key] = lines.length ? {startTs, stepMs, buckets, series: lines} : null;
    }
    return {charts, buckets, stepMs, startTs, fields: byField.size};
}
