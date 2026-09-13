import assert from "node:assert/strict";
import {test} from "node:test";
import {DEPLOYMENT_EVENT_DELETE, deploymentRestartEvent} from "./deployment.js";

const event = (overrides = {}) => ({version: 2, specVersion: 1, spaceVersion: 1, nameVersion: 1, eventType: 2, ...overrides});

test("an update whose facets did not move is a restart", () => {
    assert.equal(deploymentRestartEvent(event({version: 3}), event()), true);
});

test("a spec, space, or name change is not a restart", () => {
    assert.equal(deploymentRestartEvent(event({version: 3, specVersion: 2}), event()), false);
    assert.equal(deploymentRestartEvent(event({version: 3, spaceVersion: 2}), event()), false);
    assert.equal(deploymentRestartEvent(event({version: 3, nameVersion: 2}), event()), false);
});

test("creates and deletes are not restarts", () => {
    assert.equal(deploymentRestartEvent(event(), null), false);
    assert.equal(deploymentRestartEvent(event({version: 3, eventType: DEPLOYMENT_EVENT_DELETE}), event()), false);
    assert.equal(deploymentRestartEvent(event({version: 4}), event({version: 3, eventType: DEPLOYMENT_EVENT_DELETE})), false);
});
