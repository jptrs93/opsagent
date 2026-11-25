import test from 'node:test';
import assert from 'node:assert/strict';
import {dataDiskUsage, diskUsage, extraDiskUsages, latestByNode, nodeOverviewRow, usageTone} from './nodeMetrics.js';

const GIB = 1024 ** 3;

test('disk usage counts used against what an unprivileged writer can still fill', () => {
    const u = diskUsage({totalBytes: 100 * GIB, freeBytes: 40 * GIB, availBytes: 35 * GIB});
    assert.equal(u.total, 100 * GIB);
    assert.equal(u.used, 60 * GIB);
    assert.equal(u.avail, 35 * GIB);
    assert.ok(Math.abs(u.usedPct - 60 / 95 * 100) < 1e-9);
});

test('disk usage is absent without a total or a free count', () => {
    assert.equal(diskUsage(null), null);
    assert.equal(diskUsage({}), null);
    assert.equal(diskUsage({totalBytes: 0, freeBytes: 0, availBytes: 0}), null);
    assert.equal(diskUsage({totalBytes: 10}), null);
    assert.equal(dataDiskUsage(undefined), null);
    assert.equal(dataDiskUsage({memTotal: 1}), null);
});

test('a filesystem with nothing left to write reads as full', () => {
    assert.equal(diskUsage({totalBytes: 10, freeBytes: 0, availBytes: 0}).usedPct, 100);
    assert.equal(diskUsage({totalBytes: 10, freeBytes: 4}).usedPct, 60);
});

test('the data filesystem comes from the flat sample fields', () => {
    const u = dataDiskUsage({fsDataTotalBytes: 200, fsDataFreeBytes: 50, fsDataAvailBytes: 50});
    assert.deepEqual(u, {total: 200, used: 150, avail: 50, usedPct: 75});
});

test('extra filesystems are listed by mount point and skip unusable entries', () => {
    const list = extraDiskUsages({extraFilesystems: {
        '/var/lib/opendeploy-nix': {totalBytes: 500, freeBytes: 250, availBytes: 250},
        '/mnt/logs': {totalBytes: 100, freeBytes: 90, availBytes: 90},
        '': {totalBytes: 1, freeBytes: 1, availBytes: 1},
        '/empty': {totalBytes: 0, freeBytes: 0, availBytes: 0},
    }});
    assert.deepEqual(list.map(f => f.mount), ['/mnt/logs', '/var/lib/opendeploy-nix']);
    assert.equal(list[1].usedPct, 50);
    assert.deepEqual(extraDiskUsages({}), []);
    assert.deepEqual(extraDiskUsages(null), []);
});

test('usage tone warns at 80 percent and alarms at 90', () => {
    assert.equal(usageTone(NaN), '');
    assert.equal(usageTone(79.9), '');
    assert.equal(usageTone(80), 'warn');
    assert.equal(usageTone(89.9), 'warn');
    assert.equal(usageTone(90), 'error');
});

test('latest entries are keyed by node and entries without a node are dropped', () => {
    const map = latestByNode({entries: [{sample: {nodeId: 2}}, {sample: {nodeId: 0}}, {sample: {nodeId: 5}}]});
    assert.deepEqual([...map.keys()], [2, 5]);
    assert.equal(latestByNode(null).size, 0);
});

test('an overview row turns the latest sample and its rates into the table values', () => {
    const row = nodeOverviewRow({
        sample: {
            time: 1_000_000, nodeId: 2, cpuCount: 4, load1: 1.5,
            memTotal: 16 * GIB, memAvailable: 10 * GIB,
            fsDataTotalBytes: 100 * GIB, fsDataFreeBytes: 40 * GIB, fsDataAvailBytes: 40 * GIB,
        },
        rates: [
            {field: 'cpu_usage_usec', perSecond: 1_000_000},
            {field: 'net_rx_bytes', perSecond: 2048},
            {field: 'io_write_bytes', perSecond: 4096},
        ],
    }, 1_007_400);
    assert.equal(row.cpu, 1);
    assert.equal(row.cpuPct, 25);
    assert.equal(row.load, 1.5);
    assert.equal(row.memUsed, 6 * GIB);
    assert.equal(row.memPct, 37.5);
    assert.equal(row.diskUsed, 60 * GIB);
    assert.equal(row.diskPct, 60);
    assert.equal(row.rx, 2048);
    assert.ok(Number.isNaN(row.tx));
    assert.ok(Number.isNaN(row.read));
    assert.equal(row.write, 4096);
    assert.equal(row.age, 7);
});

test('an overview row for a node with no sample is all blanks', () => {
    const row = nodeOverviewRow(undefined, 5);
    assert.equal(row.disk, null);
    for (const key of ['cpu', 'cpuPct', 'load', 'memUsed', 'memPct', 'diskUsed', 'diskPct', 'rx', 'tx', 'read', 'write', 'age']) {
        assert.ok(Number.isNaN(row[key]), key);
    }
});
