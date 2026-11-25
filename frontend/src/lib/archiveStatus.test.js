import assert from "node:assert/strict";
import {test} from "node:test";
import {archiveLabel, archiveRows, archiveSentence, archiveTone, archiveTotals, kindHint, kindSummary, kindText, pendingText} from "./archiveStatus.js";

const kind = (local, archived, pending, lastArchivedAt = 0) => ({
    local: {chunks: local[0], bytes: local[1]},
    archived: {chunks: archived[0], bytes: archived[1]},
    pending: {chunks: pending[0], bytes: pending[1]},
    lastArchivedAt,
});

const status = {
    enabled: true,
    nodes: [
        {nodeId: 3, logs: kind([4, 4000], [10, 9000], [1, 1000], 5_000), deploymentMetrics: kind([2, 200], [2, 200], [0, 0], 9_000), nodeMetrics: kind([1, 50], [0, 0], [1, 50])},
        {nodeId: 1, logs: kind([0, 0], [0, 0], [0, 0]), deploymentMetrics: kind([3, 300], [3, 300], [0, 0], 7_000), nodeMetrics: kind([1, 10], [1, 10], [0, 0], 8_000)},
    ],
};
const machines = [{id: 1, name: "primary"}, {id: 3, identifier: "worker-3", evicted: true}];

test("a kind's recorded files are the archived ones plus the ones only its node holds", () => {
    const s = kindSummary(status.nodes[0].logs);
    assert.deepEqual(s.total, {chunks: 11, bytes: 10_000});
    assert.equal(kindText(s), "10 / 11");
    assert.equal(kindHint(s), "9.0 KB archived, 4.0 KB on the node, 10.0 KB recorded");
    assert.equal(kindText(kindSummary(undefined)), "-");
    assert.equal(kindHint(kindSummary({})), "No sealed files recorded");
});

test("rows are in node order, named from the node list, with totals across kinds", () => {
    const rows = archiveRows(status, machines);
    assert.deepEqual(rows.map((r) => [r.id, r.name, r.evicted]), [[1, "primary", false], [3, "worker-3", true]]);
    assert.deepEqual(rows[1].pending, {chunks: 2, bytes: 1050});
    assert.deepEqual(rows[1].archived, {chunks: 12, bytes: 9200});
    assert.equal(rows[1].lastArchivedAt, 9_000);
    assert.equal(rows[0].lastArchivedAt, 8_000);
    assert.deepEqual(rows[0].kinds.map((k) => k.label), ["Logs", "Deployment metrics", "Node metrics"]);
    assert.equal(archiveRows({nodes: [{nodeId: 9}]}, machines)[0].name, "node 9");
    assert.deepEqual(archiveRows(null), []);
});

test("the badge and sentence follow whether the archive is on and what is waiting", () => {
    const rows = archiveRows(status, machines);
    assert.deepEqual(archiveTotals(rows).pending, {chunks: 2, bytes: 1050});
    assert.equal(archiveTone(status, rows), "warn");
    assert.equal(archiveLabel(status, rows), "2 waiting");
    assert.match(archiveSentence(status, rows), /^2 files · 1\.1 KB still to send\./);

    const done = {enabled: true, nodes: [status.nodes[1]]};
    const doneRows = archiveRows(done, machines);
    assert.equal(archiveTone(done, doneRows), "ok");
    assert.equal(archiveLabel(done, doneRows), "archived");
    assert.equal(archiveSentence(done, doneRows), "Every sealed log and metrics file is in the backup bucket.");

    assert.equal(archiveSentence({enabled: true, nodes: []}, []), "Nothing to send yet. A log or metrics file is sealed after its day ends.");
    assert.equal(archiveLabel({enabled: true, nodes: []}, []), "on");
    assert.equal(archiveTone({enabled: true, nodes: []}, []), "ok");

    const off = {...status, enabled: false};
    assert.equal(archiveTone(off, rows), "off");
    assert.equal(archiveLabel(off, rows), "off");
    assert.match(archiveSentence(off, rows), /stay on the node that wrote them/);
});

test("one file waiting reads in the singular", () => {
    assert.equal(pendingText({chunks: 1, bytes: 2_500_000}), "1 file · 2.50 MB");
    assert.equal(pendingText({chunks: 0, bytes: 0}), "-");
});
