import assert from "node:assert/strict";
import test from "node:test";
import {orderSessions, sessionExpired, sessionLive, sessionStatus} from "./agentSessions.js";

const NOW = new Date("2026-08-06T12:00:00Z").getTime();
const at = (offsetHours) => new Date(NOW + offsetHours * 3600 * 1000);

const session = (id, {hoursLeft = 6, revoked = false} = {}) => ({
    id,
    createdAt: at(-1),
    expiresAt: at(hoursLeft),
    revoked,
    tokenPrefix: `tok${id}`,
});

test("a session with time left and no revocation is live", () => {
    assert.equal(sessionLive(session("a"), NOW), true);
    assert.equal(sessionExpired(session("a"), NOW), false);
});

test("expiry is treated as exclusive", () => {
    const expiring = {...session("a"), expiresAt: new Date(NOW)};
    assert.equal(sessionExpired(expiring, NOW), true);
    assert.equal(sessionLive(expiring, NOW), false);
});

test("a missing or invalid expiry counts as expired rather than live", () => {
    assert.equal(sessionExpired({}, NOW), true);
    assert.equal(sessionExpired({expiresAt: new Date("nonsense")}, NOW), true);
});

test("revoked wins over expired, since it says why the session stopped", () => {
    const both = session("a", {hoursLeft: -1, revoked: true});
    assert.equal(sessionStatus(both, NOW).label, "Revoked");
    assert.equal(sessionStatus(session("b", {hoursLeft: -1}), NOW).label, "Expired");
    assert.equal(sessionStatus(session("c"), NOW).label, "Active");
});

test("live sessions sort above revoked and expired ones", () => {
    const ordered = orderSessions([
        session("expired", {hoursLeft: -2}),
        session("live-newer"),
        session("revoked", {revoked: true}),
        session("live-older", {hoursLeft: 3}),
    ], NOW);
    assert.deepEqual(ordered.map(s => s.id), ["live-newer", "live-older", "expired", "revoked"]);
});

// The server returns newest-first; the partition must not disturb that, or the
// top row would stop being the session the operator just started.
test("ordering is stable within each group", () => {
    const input = [session("s1"), session("s2"), session("s3")];
    assert.deepEqual(orderSessions(input, NOW).map(s => s.id), ["s1", "s2", "s3"]);
});

test("an empty or missing list is handled", () => {
    assert.deepEqual(orderSessions([], NOW), []);
    assert.deepEqual(orderSessions(undefined, NOW), []);
});
