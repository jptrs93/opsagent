import test from 'node:test';
import assert from 'node:assert/strict';
import {CHARTS, QUERY_FIELDS, buildChartData} from './metricsData.js';

const series = (run, field, values) => ({scheduledInstanceId: run, ordinal: 0, specVersion: 1, run: 1, nodeId: 2, field, kind: 0, values});

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

test('query fields cover every chart line exactly once', () => {
    const fields = CHARTS.flatMap(c => c.lines.map(l => l.field));
    assert.deepEqual([...new Set(fields)], QUERY_FIELDS);
});
