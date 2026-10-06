import assert from "node:assert/strict";
import {test} from "node:test";
import {
    containerWorkload,
    dedicatedScheduling,
    deploymentDeleted,
    deploymentId,
    deploymentRestartEvent,
    deploymentSeq,
    isSelfDeployment,
    placementNodeId,
} from "./deployment.js";

const record = ({generation = 1, specVersion = 1, name = 'api', spaceId = 1, running = true, nodes = [2], deleted = false} = {}) => ({
    deployment: {id: 7, name, spaceId, scheduling: {running, restartGeneration: generation, placement: {value: {dedicatedNodes: {nodes}}}}},
    meta: {version: 2, specVersion, updatedSeq: 40, deleted},
});

test("an update that only advances the restart generation is a restart", () => {
    assert.equal(deploymentRestartEvent(record({generation: 2}), record()), true);
});

test("a spec, space, name, running, or placement change is not a restart", () => {
    assert.equal(deploymentRestartEvent(record({generation: 2, specVersion: 2}), record()), false);
    assert.equal(deploymentRestartEvent(record({generation: 2, spaceId: 2}), record()), false);
    assert.equal(deploymentRestartEvent(record({generation: 2, name: 'web'}), record()), false);
    assert.equal(deploymentRestartEvent(record({generation: 2, running: false}), record()), false);
    assert.equal(deploymentRestartEvent(record({generation: 2, nodes: [3]}), record()), false);
    assert.equal(deploymentRestartEvent(record(), record()), false);
});

test("creates and deletes are not restarts", () => {
    assert.equal(deploymentRestartEvent(record(), null), false);
    assert.equal(deploymentRestartEvent(record({generation: 2, deleted: true}), record()), false);
    assert.equal(deploymentRestartEvent(record({generation: 3}), record({generation: 2, deleted: true})), false);
});

test("record readers follow the DeploymentRecord shape", () => {
    const item = record();
    assert.equal(deploymentId(item), 7);
    assert.equal(deploymentSeq(item), 40);
    assert.equal(placementNodeId(item), 2);
    assert.equal(deploymentDeleted(item), false);
    assert.equal(deploymentDeleted(record({deleted: true})), true);
    assert.equal(containerWorkload(item), null);
    item.deployment.spec = {workload: {value: {container: {version: "1"}}}};
    assert.deepEqual(containerWorkload(item), {version: "1"});
    assert.equal(isSelfDeployment(record({spaceId: 0, name: 'opendeploy'})), true);
    assert.equal(isSelfDeployment(record({spaceId: 1, name: 'opendeploy'})), false);
    assert.deepEqual(dedicatedScheduling(true, 4), {running: true, placement: {value: {dedicatedNodes: {nodes: [4]}}}});
    assert.deepEqual(dedicatedScheduling(false, 0), {running: false, placement: {value: {dedicatedNodes: {nodes: []}}}});
});
