export const SERIES_COLORS = ['#3b82f6', '#f59e0b', '#10b981', '#ef4444', '#a855f7', '#06b6d4', '#f472b6', '#84cc16', '#fb923c', '#2dd4bf'];

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

export const runLabel = (s) => `v${s.specVersion} i${s.ordinal} r${s.run}`;
export const runKey = (s) => `${s.scheduledInstanceId}|${s.specVersion}|${s.run}`;

export function buildChartData(resp, split) {
    const buckets = Number(resp.buckets || 0);
    const stepMs = Number(resp.stepMs || 0);
    const startTs = resp.timeStart instanceof Date ? resp.timeStart.getTime() : 0;
    const series = resp.series || [];
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
    let colorIdx = 0;
    for (const chart of CHARTS) {
        const lines = [];
        for (const ln of chart.lines) {
            const scale = (ln.scale || 1) * (chart.perBucket ? stepSec : 1);
            if (split && runs.length > 1) {
                for (const run of runs) {
                    const s = byRunField.get(`${run.key}|${ln.field}`);
                    if (!s) continue;
                    lines.push({label: `${ln.label} ${run.label}`, color: SERIES_COLORS[colorIdx++ % SERIES_COLORS.length], values: s.values.map(v => v * scale), field: ln.field});
                }
                continue;
            }
            const values = new Array(buckets).fill(NaN);
            let any = false;
            for (const run of runs) {
                const s = byRunField.get(`${run.key}|${ln.field}`);
                if (!s) continue;
                any = true;
                for (let b = 0; b < buckets; b++) {
                    const v = s.values[b];
                    if (!Number.isFinite(v)) continue;
                    values[b] = (Number.isFinite(values[b]) ? values[b] : 0) + v * scale;
                }
            }
            if (any) lines.push({label: ln.label, color: SERIES_COLORS[colorIdx++ % SERIES_COLORS.length], values, field: ln.field});
        }
        colorIdx = 0;
        out[chart.key] = lines.length ? {startTs, stepMs, buckets, series: lines} : null;
    }
    return {charts: out, runs, buckets, stepMs, startTs};
}

