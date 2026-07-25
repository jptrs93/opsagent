import assert from 'node:assert/strict';
import test from 'node:test';

import {applyScheduledInstanceUpdate, mergeDeploymentState} from './deploymentMerge.js';

test('keeps stopped desired deployment without an assignment', () => {
    const configs = new Map([[7, {id: 7, version: 2, spec: {container1Spec: {running: false}}}]]);
    const rows = mergeDeploymentState(configs, new Map());
    assert.equal(rows.length, 1);
    assert.equal(rows[0].config.version, 2);
    assert.equal(rows[0].instance, undefined);
});

test('uses latest desired config and keeps the pinned runtime config', () => {
    const desired = {id: 7, version: 2};
    const pinned = {id: 7, version: 1};
    const configs = new Map([[7, desired]]);
    const instances = new Map([[11, {
        instance: {id: 11, deploymentId: 7, state: 0},
        config: pinned,
        status: {runner: {status: 2}},
    }]]);
    const [row] = mergeDeploymentState(configs, instances);
    assert.equal(row.config, desired);
    assert.equal(row.pinnedConfig, pinned);
    assert.equal(row.instance.id, 11);
});

test('selects newest non-final runtime and ignores finalized state', () => {
    const configs = new Map([[7, {id: 7, version: 3}]]);
    const instances = new Map([
        [10, {instance: {id: 10, deploymentId: 7, state: 0}, config: {id: 7, version: 1}}],
        [11, {instance: {id: 11, deploymentId: 7, state: 0}, config: {id: 7, version: 2}}],
        [12, {instance: {id: 12, deploymentId: 7, state: 2}, config: {id: 7, version: 3}}],
    ]);
    const [row] = mergeDeploymentState(configs, instances);
    assert.equal(row.instance.id, 11);
    assert.equal(row.pinnedConfig.version, 2);
});

test('finalized update removes only its scheduled instance', () => {
    const instances = new Map([
        [10, {instance: {id: 10, deploymentId: 7, state: 0}}],
        [11, {instance: {id: 11, deploymentId: 7, state: 0}}],
    ]);
    const next = applyScheduledInstanceUpdate(instances, {instance: {id: 10, deploymentId: 7, state: 2}});
    assert.equal(next.has(10), false);
    assert.equal(next.has(11), true);
});
