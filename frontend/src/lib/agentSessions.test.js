import assert from "node:assert/strict";
import test from "node:test";
import {
    STATUS,
    orderSessions,
    sessionExpired,
    sessionLive,
    sessionPending,
    sessionStatus,
    sessionStoppable,
} from "./agentSessions.js";

const NOW = new Date("2026-08-06T12:00:00Z").getTime();
const at = (offsetHours) => new Date(NOW + offsetHours * 3600 * 1000);

const session = (id, {hoursLeft = 6, status = STATUS.APPROVED} = {}) => ({
    id,
    createdAt: at(-1),
    expiresAt: at(hoursLeft),
    status,
    tokenPrefix: `tok${id}`,
});

const pending = (id) => ({
    id,
    createdAt: at(-0.05),
    expiresAt: new Date(0),
    status: STATUS.PENDING,
    approvalCode: "K7M-4QP2",
    requestingAddress: "10.0.0.4",
});

test("an approved session with time left is live", () => {
    assert.equal(sessionLive(session("a"), NOW), true);
    assert.equal(sessionExpired(session("a"), NOW), false);
});

test("expiry is treated as exclusive", () => {
    const expiring = {...session("a"), expiresAt: new Date(NOW)};
    assert.equal(sessionExpired(expiring, NOW), true);
    assert.equal(sessionLive(expiring, NOW), false);
});

// A pending or uncollected session has no expiry yet. Reading that as "expired"
// would show every fresh request as dead on arrival.
test("a missing expiry is not treated as expired", () => {
    assert.equal(sessionExpired({}, NOW), false);
    assert.equal(sessionExpired({expiresAt: new Date(0)}, NOW), false);
    assert.equal(sessionPending(pending("p")), true);
});

test("status reports why a session stopped, not just that it did", () => {
    assert.equal(sessionStatus(pending("p"), NOW).label, "Awaiting approval");
    assert.equal(sessionStatus(session("a", {status: STATUS.REVOKED}), NOW).label, "Revoked");
    assert.equal(sessionStatus(session("b", {status: STATUS.REJECTED}), NOW).label, "Rejected");
    assert.equal(sessionStatus(session("c", {hoursLeft: -1}), NOW).label, "Expired");
    assert.equal(sessionStatus(session("d"), NOW).label, "Active");
});

test("an approved session with no expiry is waiting to be collected", () => {
    const approved = {...session("a"), expiresAt: new Date(0)};
    assert.equal(sessionStatus(approved, NOW).label, "Approved, not collected");
    assert.equal(sessionLive(approved, NOW), true);
});

test("pending requests sort above live sessions, and dead ones sink", () => {
    const ordered = orderSessions([
        session("expired", {hoursLeft: -2}),
        session("live-newer"),
        session("revoked", {status: STATUS.REVOKED}),
        pending("request"),
        session("live-older", {hoursLeft: 3}),
    ], NOW);
    assert.deepEqual(
        ordered.map(s => s.id),
        ["request", "live-newer", "live-older", "expired", "revoked"],
    );
});

// The server returns newest-first; the partition must not disturb that, or the
// top row would stop being the session the operator just started.
test("ordering is stable within each group", () => {
    const input = [session("s1"), session("s2"), session("s3")];
    assert.deepEqual(orderSessions(input, NOW).map(s => s.id), ["s1", "s2", "s3"]);
});

test("only pending and live sessions can be stopped", () => {
    assert.equal(sessionStoppable(pending("p"), NOW), true);
    assert.equal(sessionStoppable(session("a"), NOW), true);
    assert.equal(sessionStoppable(session("b", {hoursLeft: -1}), NOW), false);
    assert.equal(sessionStoppable(session("c", {status: STATUS.REVOKED}), NOW), false);
});

test("an empty or missing list is handled", () => {
    assert.deepEqual(orderSessions([], NOW), []);
    assert.deepEqual(orderSessions(undefined, NOW), []);
});
