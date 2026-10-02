import assert from 'node:assert/strict';
import test from 'node:test';
import {createTree, applyMessage} from './tree.js';
import {deriveDeploymentRows} from './deploymentMerge.js';
import {deriveEnrollments, configViewModel, authzGrantViewModel, nodeViewModel} from './derive.js';

const DEPLOYMENT = 1, SCHEDULED_INSTANCE = 2, NODE = 3, SCHEDULED_INSTANCE_STATUS = 16, NODE_STATUS = 17;
const fields = {[DEPLOYMENT]: 'deployment', [SCHEDULED_INSTANCE]: 'scheduledInstance', [NODE]: 'node', [SCHEDULED_INSTANCE_STATUS]: 'scheduledInstanceStatus', [NODE_STATUS]: 'nodeStatus'};
const create = (type, id, entity, meta) => ({create: {entityType: type, entityId: id, entity: {[fields[type]]: entity}, ...(meta ? {meta} : {})}});
const deployment = version => ({spec: {container1Spec: {running: false, version: `sha-${version}`}}});
const deploymentMeta = version => ({version, specVersion: version});
const instance = (id, version = 1, state = 0, ordinal = 0) => ({id, deploymentId: 7, deploymentVersion: version, state, instanceOrdinal: ordinal});
const fold = (mutations, seq = 1) => {
    const tree = createTree();
    applyMessage(tree, {seq, events: [{seq, time: seq * 1000, actor: 0, mutations}]});
    return tree;
};
const rows = (versions, instances = [], statuses = []) => deriveDeploymentRows(fold([
    ...versions.map(v => create(DEPLOYMENT, 7, deployment(v), deploymentMeta(v))),
    ...instances.map(i => create(SCHEDULED_INSTANCE, i.id, i)),
    ...statuses.map(s => create(SCHEDULED_INSTANCE_STATUS, s.scheduledInstanceId, s)),
]));

test('stopped desired deployment remains without assignments', () => {
    const [row] = rows([2]);
    assert.equal(row.config.version, 2);
    assert.deepEqual(row.scheduledInstances, []);
    assert.equal(row.instance, undefined);
});

test('latest desired config and every live pinned config retain distinct versions', () => {
    const [row] = rows([1, 2, 3], [instance(11, 2), instance(10, 1), instance(12, 3, 2)]);
    assert.equal(row.config.version, 3);
    assert.deepEqual(row.scheduledInstances.map(s => s.instance.id), [10, 11]);
    assert.deepEqual(row.scheduledInstances.map(s => s.config.version), [1, 2]);
    assert.equal(row.instance.id, 11);
    assert.equal(row.pinnedConfig.version, 2);
});

test('each ordinal selects live instances or its newest finalized incarnation', () => {
    const [row] = rows([1], [instance(10, 1, 2), instance(11, 1, 2), instance(12, 1, 2, 1), instance(13, 1, 0, 1)]);
    assert.deepEqual(row.scheduledInstances.map(s => s.instance.id), [11, 13]);
});

test('the running version is read off the pinned config the runner reports against', () => {
    const status = (id, specVersion) => ({scheduledInstanceId: id, updatedAt: new Date(10), runner: {status: 2, deploymentSpecVersion: specVersion}});
    const [row] = rows([1, 2], [instance(10, 1), instance(11, 2)], [status(10, 1), status(11, 1)]);
    assert.equal(row.scheduledInstances[0].status.runner.runningVersion, 'sha-1');
    assert.equal(row.scheduledInstances[1].status.runner.runningVersion, undefined, 'a runner still on an older spec version has no version for the new pin');
    assert.equal(row.status.runner.runningVersion, undefined);
});

test('enrollments derive pending members without changing their membership status', () => {
    const tree = createTree();
    applyMessage(tree, {seq: 4, events: [
        {seq: 2, time: 2000, mutations: [create(NODE, 2, {status: 4, enrollmentRequestedAt: 0}), create(NODE, 3, {status: 1, enrollmentRequestedAt: 124})]},
        {seq: 4, time: 4000, mutations: [create(NODE, 1, {status: 4, enrollmentRequestedAt: 123, reported: {identifier: 'member'}}), create(NODE_STATUS, 1, {nodeId: 1, updatedAt: 10, isConnected: true})]},
    ]});
    const result = deriveEnrollments(tree);
    assert.deepEqual(result.map(r => r.id), [3, 1]);
    assert.equal(result[1].seq, 4, 'accept carries the seq of the node row the operator reviewed');
    assert.equal(result[1].isConnected, true);
    assert.equal(tree.nodes.get(1).value.status, 4);
});

test('evicted nodes are neither members nor pending enrollments', () => {
    const tree = createTree();
    applyMessage(tree, {seq: 2, events: [
        {seq: 1, time: 100, mutations: [create(NODE, 1, {status: 4, enrollmentRequestedAt: 0, reported: {identifier: 'member'}}), create(NODE, 3, {status: 6, enrollmentRequestedAt: 0, reported: {identifier: 'draining'}})]},
        {seq: 2, time: 500, mutations: [create(NODE, 2, {status: 8, enrollmentRequestedAt: 0, reported: {identifier: 'gone'}})]},
    ]});
    assert.deepEqual(deriveEnrollments(tree).map(r => r.id), []);
    const gone = nodeViewModel(tree.nodes.get(2));
    assert.equal(gone.evicted, true);
    assert.equal(gone.draining, false);
    assert.equal(gone.eventTime, 500);
    assert.equal(gone.seq, 2);
    assert.equal(nodeViewModel(tree.nodes.get(3)).draining, true);
    assert.equal(nodeViewModel(tree.nodes.get(1)).evicted, false);
});

test('rename and move history preserve the value versions that can be pinned', () => {
    const history = [
        {configId: 1, seq: 10, eventTime: 100, author: 3, valueVersion: 1, value: {fs: {name: 'old'}, spaceId: 1, value: 'a'}},
        {configId: 1, seq: 11, eventTime: 200, author: 3, valueVersion: 2, value: {fs: {name: 'old'}, spaceId: 1, value: 'b'}},
        {configId: 1, seq: 12, eventTime: 300, author: 4, valueVersion: 2, value: {fs: {name: 'new'}, spaceId: 2, value: 'b'}},
    ];
    const model = configViewModel(history);
    assert.equal(model.name, 'new');
    assert.equal(model.spaceId, 2);
    assert.equal(model.version, 2);
    assert.equal(model.seq, 12);
    assert.deepEqual(model.valueVersions.map(v => v.version), [2, 1]);
    assert.deepEqual(model.valueVersions.map(v => v.value), ['b', 'a']);
    assert.deepEqual(model.valueVersions.map(v => v.createdAt.getTime()), [200, 100]);
});

test('grant view preserves subject, bindings and attribution from the event', () => {
    const value = {userId: 7, templateId: 2, spec: {args: [{argumentId: 1, values: [3]}]}, createdTime: 123};
    assert.deepEqual(authzGrantViewModel({authzGrantId: 10, author: 4, createdTime: 123, value}), {
        ...value, id: 10, author: 4, createdAt: 123,
    });
});
