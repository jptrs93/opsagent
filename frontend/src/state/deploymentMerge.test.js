import assert from 'node:assert/strict';
import test from 'node:test';

import {applyScheduledInstanceUpdate, mergeDeploymentState} from './deploymentMerge.js';

test('keeps stopped desired deployment without an assignment', () => {
    const configs = new Map([[7, {id: 7, version: 2, spec: {container1Spec: {running: false}}}]]);
    const rows = mergeDeploymentState(configs, new Map());
    assert.equal(rows.length, 1);
    assert.equal(rows[0].config.version, 2);
    assert.deepEqual(rows[0].scheduledInstances, []);
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
    assert.deepEqual(row.scheduledInstances, [instances.get(11)]);
    assert.equal(row.config, desired);
    assert.equal(row.pinnedConfig, pinned);
    assert.equal(row.instance.id, 11);
});

test('keeps non-final runtimes oldest first and uses the newest runtime aliases', () => {
    const configs = new Map([[7, {id: 7, version: 3}]]);
    const instances = new Map([
        [11, {instance: {id: 11, deploymentId: 7, state: 0}, config: {id: 7, version: 2}}],
        [10, {instance: {id: 10, deploymentId: 7, state: 0}, config: {id: 7, version: 1}}],
        [12, {instance: {id: 12, deploymentId: 7, state: 2}, config: {id: 7, version: 3}}],
    ]);
    const [row] = mergeDeploymentState(configs, instances);
    assert.deepEqual(row.scheduledInstances.map(state => state.instance.id), [10, 11]);
    assert.equal(row.instance.id, 11);
    assert.equal(row.pinnedConfig.version, 2);
});

test('stopped deployment shows the last run of the ordinal', () => {
    const configs = new Map([[7, {id: 7, version: 3, spec: {container1Spec: {running: false}}}]]);
    const instances = new Map([
        [10, {instance: {id: 10, deploymentId: 7, instanceOrdinal: 0, state: 2}, status: {runner: {status: 2}}}],
        [11, {instance: {id: 11, deploymentId: 7, instanceOrdinal: 0, state: 2}, status: {runner: {status: 3}}}],
    ]);
    const [row] = mergeDeploymentState(configs, instances);
    assert.deepEqual(row.scheduledInstances.map(state => state.instance.id), [11]);
    assert.equal(row.status.runner.status, 3);
});

test('a live instance hides the finalized run it replaced', () => {
    const configs = new Map([[7, {id: 7, version: 3}]]);
    const instances = new Map([
        [10, {instance: {id: 10, deploymentId: 7, instanceOrdinal: 0, state: 2}}],
        [11, {instance: {id: 11, deploymentId: 7, instanceOrdinal: 0, state: 0}}],
    ]);
    const [row] = mergeDeploymentState(configs, instances);
    assert.deepEqual(row.scheduledInstances.map(state => state.instance.id), [11]);
});

test('each ordinal keeps its own last run', () => {
    const configs = new Map([[7, {id: 7, version: 3}]]);
    const instances = new Map([
        [10, {instance: {id: 10, deploymentId: 7, instanceOrdinal: 0, state: 2}}],
        [11, {instance: {id: 11, deploymentId: 7, instanceOrdinal: 1, state: 2}}],
    ]);
    const [row] = mergeDeploymentState(configs, instances);
    assert.deepEqual(row.scheduledInstances.map(state => state.instance.id), [10, 11]);
});

test('finalized update is retained as the last run of its ordinal', () => {
    const instances = new Map([
        [10, {instance: {id: 10, deploymentId: 7, instanceOrdinal: 0, state: 0}}],
        [11, {instance: {id: 11, deploymentId: 7, instanceOrdinal: 0, state: 0}}],
    ]);
    const next = applyScheduledInstanceUpdate(instances, {instance: {id: 10, deploymentId: 7, instanceOrdinal: 0, state: 2}});
    assert.equal(next.get(10).instance.state, 2);
    assert.equal(next.has(11), true);
});

test('an update prunes the finalized runs it supersedes on the same ordinal', () => {
    const instances = new Map([
        [10, {instance: {id: 10, deploymentId: 7, instanceOrdinal: 0, state: 2}}],
        [20, {instance: {id: 20, deploymentId: 8, instanceOrdinal: 0, state: 2}}],
        [21, {instance: {id: 21, deploymentId: 7, instanceOrdinal: 1, state: 2}}],
    ]);
    const next = applyScheduledInstanceUpdate(instances, {instance: {id: 30, deploymentId: 7, instanceOrdinal: 0, state: 0}});
    assert.equal(next.has(10), false, 'superseded run on the same ordinal is dropped');
    assert.equal(next.has(20), true, 'other deployments are untouched');
    assert.equal(next.has(21), true, 'other ordinals are untouched');
    assert.equal(next.has(30), true);
});
