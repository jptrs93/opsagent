import assert from 'node:assert/strict';
import test from 'node:test';
import {createTree, applySnapshot} from './tree.js';
import {deriveDeploymentRows} from './deploymentMerge.js';
import {deriveEnrollments, configViewModel, authzGrantViewModel} from './derive.js';

const deployment = version => ({deploymentId: 7, version, specVersion: version, eventType: 1, value: {spec: {container1Spec: {running: false}}}});
const instance = (id, version = 1, state = 0, ordinal = 0) => ({scheduledInstanceId: id, version: 1, value: {deploymentId: 7, deploymentVersion: version, state, instanceOrdinal: ordinal}});
const rows = (events, instances = []) => {
    const tree = createTree();
    applySnapshot(tree, {seq: 1, deploymentEvents: events, scheduledInstanceEvents: instances});
    return deriveDeploymentRows(tree);
};

test('stopped desired deployment remains without assignments', () => {
    const [row] = rows([deployment(2)]);
    assert.equal(row.config.version, 2);
    assert.deepEqual(row.scheduledInstances, []);
    assert.equal(row.instance, undefined);
});

test('latest desired config and every live pinned config retain distinct versions', () => {
    const [row] = rows([deployment(1), deployment(2), deployment(3)], [instance(11, 2), instance(10, 1), instance(12, 3, 2)]);
    assert.equal(row.config.version, 3);
    assert.deepEqual(row.scheduledInstances.map(s => s.instance.id), [10, 11]);
    assert.deepEqual(row.scheduledInstances.map(s => s.config.version), [1, 2]);
    assert.equal(row.instance.id, 11);
    assert.equal(row.pinnedConfig.version, 2);
});

test('each ordinal selects live instances or its newest finalized incarnation', () => {
    const [row] = rows([deployment(1)], [instance(10, 1, 2), instance(11, 1, 2), instance(12, 1, 2, 1), instance(13, 1, 0, 1)]);
    assert.deepEqual(row.scheduledInstances.map(s => s.instance.id), [11, 13]);
});

test('enrollments derive pending members without changing their membership status', () => {
    const tree = createTree();
    applySnapshot(tree, {seq: 1, nodeEvents: [
        {nodeId: 1, version: 4, value: {status: 4, enrollmentRequestedAt: 123, reported: {identifier: 'member'}}},
        {nodeId: 2, version: 2, value: {status: 4, enrollmentRequestedAt: 0}},
        {nodeId: 3, version: 1, value: {status: 1, enrollmentRequestedAt: 124}},
    ], nodeStatuses: [{nodeId: 1, updatedAt: 10, isConnected: true}]});
    const result = deriveEnrollments(tree);
    assert.deepEqual(result.map(r => r.id), [3, 1]);
    assert.equal(result[1].version, 4, 'accept uses the node version reviewed by the operator');
    assert.equal(result[1].isConnected, true);
    assert.equal(tree.nodes.get(1).value.status, 4);
});

test('rename and move history preserve original pinnable value event ids', () => {
    const history = [
        {configId: 1, version: 1, eventId: 10, valueVersion: 1, spaceVersion: 1, value: {fs: {name: 'old'}, spaceId: 1, value: 'a'}},
        {configId: 1, version: 2, eventId: 11, valueVersion: 2, spaceVersion: 1, value: {fs: {name: 'old'}, spaceId: 1, value: 'b'}},
        {configId: 1, version: 3, eventId: 12, valueVersion: 2, spaceVersion: 2, value: {fs: {name: 'new'}, spaceId: 2, value: 'b'}},
    ];
    const model = configViewModel(history);
    assert.equal(model.name, 'new');
    assert.equal(model.spaceId, 2);
    assert.deepEqual(model.valueVersions.map(v => v.id), [11, 10]);
    assert.deepEqual(model.valueVersions.map(v => v.value), ['b', 'a']);
    assert.deepEqual(model.spaceVersions.map(v => v.spaceId), [2, 1]);
});


test('grant view preserves subject, bindings and attribution from the event', () => {
    const value = {userId: 7, templateId: 2, grant: {args: [{argumentId: 1, values: [3]}]}};
    assert.deepEqual(authzGrantViewModel({authzGrantId: 10, author: 4, createdTime: 123, value}), {
        ...value, id: 10, author: 4, createdAt: 123,
    });
});
