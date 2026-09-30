import assert from "node:assert/strict";
import {test} from "node:test";
import {DEPLOYMENT_EVENT_DELETE, deploymentRestartEvent} from "./deployment.js";

const event = ({generation = 1, specVersion = 1, name = 'api', spaceId = 1, running = true, nodes = [2], eventType = 2} = {}) => ({
    version: 2, specVersion, eventType, value: {name, spaceId, scheduling: {running, generation, dedicatedNodes: {nodes}}},
});

test("an update that only advances the scheduling generation is a restart", () => {
    assert.equal(deploymentRestartEvent(event({generation: 2}), event()), true);
});

test("a spec, space, name, running, or placement change is not a restart", () => {
    assert.equal(deploymentRestartEvent(event({generation: 2, specVersion: 2}), event()), false);
    assert.equal(deploymentRestartEvent(event({generation: 2, spaceId: 2}), event()), false);
    assert.equal(deploymentRestartEvent(event({generation: 2, name: 'web'}), event()), false);
    assert.equal(deploymentRestartEvent(event({generation: 2, running: false}), event()), false);
    assert.equal(deploymentRestartEvent(event({generation: 2, nodes: [3]}), event()), false);
    assert.equal(deploymentRestartEvent(event(), event()), false);
});

test("creates and deletes are not restarts", () => {
    assert.equal(deploymentRestartEvent(event(), null), false);
    assert.equal(deploymentRestartEvent(event({generation: 2, eventType: DEPLOYMENT_EVENT_DELETE}), event()), false);
    assert.equal(deploymentRestartEvent(event({generation: 3}), event({generation: 2, eventType: DEPLOYMENT_EVENT_DELETE})), false);
});
