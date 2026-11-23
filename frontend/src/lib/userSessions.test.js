import test from "node:test";
import assert from "node:assert/strict";
import {USER_SESSION_KIND_FULL, USER_SESSION_KIND_LINK, USER_SESSION_KIND_ROOT, isLinkSession, isRootSession, userSessionLive, userSessionStatus} from "./userSessions.js";

const now = Date.parse("2026-10-23T12:00:00Z");
const at = (iso) => new Date(iso);
const none = new Date(0);

test("kind predicates read the numeric kind", () => {
    assert.equal(isRootSession({kind: USER_SESSION_KIND_ROOT}), true);
    assert.equal(isRootSession({kind: USER_SESSION_KIND_FULL}), false);
    assert.equal(isLinkSession({kind: USER_SESSION_KIND_LINK}), true);
    assert.equal(isLinkSession(null), false);
});

test("a full session is active until revoked or expired", () => {
    const live = {kind: USER_SESSION_KIND_FULL, expiresAt: at("2026-10-25T12:00:00Z"), revokedAt: none};
    assert.equal(userSessionStatus(live, now).label, "Active");
    assert.equal(userSessionLive(live, now), true);
    assert.equal(userSessionStatus({...live, revokedAt: at("2026-10-23T11:00:00Z")}, now).label, "Revoked");
    assert.equal(userSessionStatus({...live, expiresAt: at("2026-10-23T11:59:00Z")}, now).label, "Expired");
});

test("a link is unused until redeemed, cancelled, or expired", () => {
    const link = {kind: USER_SESSION_KIND_LINK, expiresAt: at("2026-10-30T12:00:00Z"), revokedAt: none, redeemedAt: none};
    assert.equal(userSessionStatus(link, now).label, "Unused");
    assert.equal(userSessionLive(link, now), true);
    assert.equal(userSessionStatus({...link, redeemedAt: at("2026-10-23T11:30:00Z")}, now).label, "Redeemed");
    assert.equal(userSessionLive({...link, redeemedAt: at("2026-10-23T11:30:00Z")}, now), false);
    assert.equal(userSessionStatus({...link, revokedAt: at("2026-10-23T11:30:00Z")}, now).label, "Cancelled");
    assert.equal(userSessionStatus({...link, expiresAt: at("2026-10-22T12:00:00Z")}, now).label, "Expired");
});
