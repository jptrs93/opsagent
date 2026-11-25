import test from 'node:test';
import assert from 'node:assert/strict';
import {CHARTS, NODE_CHARTS, NODE_QUERY_FIELDS, QUERY_FIELDS, buildChartData, buildNodeChartData, idleVolumes, volumesByPlacement, placementKey} from './metricsData.js';

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

const held = (nodeId, values) => ({scheduledInstanceId: 0, ordinal: 0, deploymentVersion: 0, run: 0, nodeId, field: 'volume_bytes', kind: 3, values});

test('a volume series belongs to the deployment: it adds no run and is one line', () => {
    const out = buildChartData({...resp, series: [...resp.series, held(2, [1000, 1000, 2000])]}, false);
    assert.equal(out.runs.length, 2);
    assert.deepEqual(out.charts.volume.series.map(s => s.label), ['used']);
    assert.deepEqual(out.charts.volume.series[0].values, [1000, 1000, 2000]);
    assert.deepEqual(buildChartData({...resp, series: [...resp.series, held(2, [1000, 1000, 2000])]}, true).charts.volume.series.map(s => s.label), ['used']);
    assert.equal(buildChartData(resp, false).charts.volume, null);
});

test('volumes on two nodes sum when combined and split into one line per node', () => {
    const both = {...resp, series: [held(2, [1000, 1000, NaN]), held(5, [NaN, 500, 500])]};
    const combined = buildChartData(both, false);
    assert.equal(combined.runs.length, 0);
    assert.deepEqual(combined.charts.volume.series[0].values.map(v => Number.isNaN(v) ? 'nan' : v), [1000, 1500, 500]);
    const split = buildChartData(both, true);
    assert.deepEqual(split.charts.volume.series.map(s => s.label), ['used node 2', 'used node 5']);
    assert.notEqual(split.charts.volume.series[0].color, split.charts.volume.series[1].color);
});

test('a volume with no running container on its node is listed as idle', () => {
    const volumes = [
        {deploymentId: 7, nodeId: 2, bytes: 100},
        {deploymentId: 7, nodeId: 5, bytes: 200},
        {deploymentId: 9, nodeId: 2, bytes: 300},
    ];
    const entries = [{sample: {deploymentId: 7, nodeId: 2}}, {sample: {deploymentId: 7, nodeId: 2}}];
    assert.equal(volumesByPlacement(volumes).get(placementKey(7, 2)).bytes, 100);
    assert.deepEqual(idleVolumes(volumes, entries).map(v => v.bytes), [200, 300]);
    assert.deepEqual(idleVolumes(undefined, entries), []);
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
