import test from 'node:test';
import assert from 'node:assert/strict';
import {CHARTS, NODE_CHARTS, NODE_QUERY_FIELDS, QUERY_FIELDS, buildChartData, buildNodeChartData} from './metricsData.js';

const series = (run, field, values) => ({scheduledInstanceId: run, ordinal: 0, deploymentVersion: 1, run: 1, nodeId: 2, field, kind: 0, values});

const resp = {
    timeStart: new Date(1_700_000_000_000),
    stepMs: 60_000,
    buckets: 3,
    series: [
        series(10, 'cpu_usage_usec', [500_000, NaN, 250_000]),
        series(11, 'cpu_usage_usec', [NaN, 1_000_000, 250_000]),
        series(10, 'mem_current', [100, 200, NaN]),
        series(10, 'mem_oom_kill', [0, 1 / 60, 0]),
    ],
};

test('combined mode sums runs per bucket and keeps NaN only where no run has data', () => {
    const out = buildChartData(resp, false);
    assert.equal(out.runs.length, 2);
    assert.equal(out.buckets, 3);
    assert.equal(out.stepMs, 60_000);
    assert.equal(out.startTs, 1_700_000_000_000);
    const cpu = out.charts.cpu.series.find(s => s.label === 'total');
    assert.deepEqual(cpu.values.map(v => Number.isNaN(v) ? 'nan' : v), [0.5, 1, 0.5]);
    const mem = out.charts.mem.series.find(s => s.label === 'current');
    assert.deepEqual(mem.values.map(v => Number.isNaN(v) ? 'nan' : v), [100, 200, 'nan']);
    const oom = out.charts.events.series.find(s => s.label === 'oom kills');
    assert.ok(Math.abs(oom.values[1] - 1) < 1e-9);
    assert.equal(out.charts.net, null);
});

test('split mode plots one line per run with run labels', () => {
    const out = buildChartData(resp, true);
    const labels = out.charts.cpu.series.map(s => s.label);
    assert.deepEqual(labels, ['total v1 i0 r1', 'total v1 i0 r1']);
    assert.notEqual(out.charts.cpu.series[0].color, out.charts.cpu.series[1].color);
    assert.equal(out.charts.mem.series.length, 1);
});

test('a line keeps its colour slot when the lines before it have no data', () => {
    const out = buildChartData({...resp, series: [series(10, 'cpu_system_usec', [1, 2, 3])]}, false);
    assert.equal(out.charts.cpu.series.length, 1);
    assert.equal(out.charts.cpu.series[0].color, 'var(--chart-series-3)');
});

test('query fields cover every chart line exactly once', () => {
    const fields = CHARTS.flatMap(c => c.lines.map(l => l.field));
    assert.deepEqual([...new Set(fields)], QUERY_FIELDS);
});

test('node charts plot one line per field, scaled, and leave a chart out when it has no field', () => {
    const out = buildNodeChartData({
        timeStart: new Date(1_700_000_000_000),
        stepMs: 60_000,
        buckets: 2,
        series: [
            {nodeId: 2, field: 'cpu_usage_usec', kind: 0, values: [1_500_000, NaN]},
            {nodeId: 2, field: 'cpu_iowait_usec', kind: 0, values: [250_000, 500_000]},
            {nodeId: 2, field: 'fs_data_used_bytes', kind: 1, values: [10, 20]},
            {nodeId: 2, field: 'fs_data_total_bytes', kind: 1, values: [100, 100]},
        ],
    });
    assert.equal(out.buckets, 2);
    assert.equal(out.stepMs, 60_000);
    assert.equal(out.startTs, 1_700_000_000_000);
    assert.equal(out.fields, 4);
    assert.deepEqual(out.charts.cpu.series.map(s => s.label), ['total', 'iowait']);
    assert.deepEqual(out.charts.cpu.series[0].values.map(v => Number.isNaN(v) ? 'nan' : v), [1.5, 'nan']);
    assert.equal(out.charts.cpu.series[1].color, 'var(--chart-series-4)');
    assert.deepEqual(out.charts.disk.series.map(s => s.values), [[10, 20], [100, 100]]);
    assert.equal(out.charts.mem, null);
    assert.equal(out.charts.net, null);
});

test('node query fields cover every node chart line exactly once', () => {
    const fields = NODE_CHARTS.flatMap(c => c.lines.map(l => l.field));
    assert.deepEqual([...new Set(fields)], NODE_QUERY_FIELDS);
    assert.equal(fields.length, NODE_QUERY_FIELDS.length);
});
