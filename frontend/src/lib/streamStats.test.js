import test from "node:test";
import assert from "node:assert/strict";
import {createStreamStats, formatAge, formatByteCount} from "./streamStats.js";

test("a heartbeat moves the heartbeat time and the sequence but is not an update", () => {
    const {stats, opening, connected, frame} = createStreamStats();
    opening();
    connected(new Date(1000));
    frame({snapshot: {}, seq: 7}, new Date(1000));
    frame({heartbeat: true, seq: 7}, new Date(4000));
    frame({heartbeat: true, seq: 9}, new Date(7000));

    assert.equal(stats.updates, 1);
    assert.equal(stats.seq, 9);
    assert.equal(stats.lastUpdate.getTime(), 1000);
    assert.equal(stats.lastHeartbeat.getTime(), 7000);
    assert.equal(stats.connectedSince.getTime(), 1000);
});

test("a frame without a sequence keeps the last one", () => {
    const {stats, frame} = createStreamStats();
    frame({events: [{}], seq: 12});
    frame({backupStatus: {}});

    assert.equal(stats.seq, 12);
    assert.equal(stats.updates, 2);
});

test("opening starts the counts and the byte share again", () => {
    let total = 500;
    const {stats, opening, connected, frame, closed} = createStreamStats(() => total);
    opening();
    connected();
    frame({events: [{}], seq: 3});
    total = 1700;
    assert.equal(stats.bytes, 1200);

    closed();
    assert.equal(stats.connectedSince, null);
    assert.equal(stats.updates, 1);

    opening();
    assert.equal(stats.bytes, 0);
    assert.equal(stats.updates, 0);
    assert.equal(stats.seq, 0);
    assert.equal(stats.lastUpdate, null);
});

test("formatAge", () => {
    const now = new Date(10 * 24 * 3600 * 1000);
    const ago = (ms) => new Date(now.getTime() - ms);
    assert.equal(formatAge(null, now), "—");
    assert.equal(formatAge(ago(400), now), "just now");
    assert.equal(formatAge(ago(-5000), now), "just now");
    assert.equal(formatAge(ago(42 * 1000), now), "42s ago");
    assert.equal(formatAge(ago(3 * 60 * 1000 + 5000), now), "3m ago");
    assert.equal(formatAge(ago(5 * 3600 * 1000), now), "5h ago");
    assert.equal(formatAge(ago(3 * 24 * 3600 * 1000), now), "3d ago");
});

test("formatByteCount", () => {
    assert.equal(formatByteCount(0), "0 B");
    assert.equal(formatByteCount(1023), "1023 B");
    assert.equal(formatByteCount(1024), "1 KiB");
    assert.equal(formatByteCount(1536), "1.5 KiB");
    assert.equal(formatByteCount(5 * 1024 * 1024), "5 MiB");
    assert.equal(formatByteCount(3.25 * 1024 * 1024 * 1024), "3.3 GiB");
});
