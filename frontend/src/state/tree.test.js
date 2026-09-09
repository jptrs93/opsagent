import assert from 'node:assert/strict';
import test from 'node:test';
import {applySnapshot, applyCore, applyObserved, applyBackupStatus, applyAgentSessions, createTree, observedClock} from './tree.js';

export const deployment = (version = 1, eventType = 1, deploymentId = 7) => ({deploymentId, version, eventType, specVersion: version, value: {name: 'api', spaceId: 1, spec: {container1Spec: {running: false}}}});
export const instance = (id, deploymentVersion = 1, state = 0, instanceOrdinal = 0) => ({scheduledInstanceId: id, version: 1, eventType: 1, value: {deploymentId: 7, deploymentVersion, state, instanceOrdinal}});

// These tests exercise complete wire messages so cross-collection invariants
// are checked at the same boundary used by the browser transport.
test('transaction reducer applies all collections and ignores duplicate or older authored updates', () => {
    const tree = createTree();
    const update = {seq: 5, deploymentEvents: [deployment()], configEvents: [{configId: 2, version: 1, valueVersion: 1, eventId: 42, value: {value: 'one'}}]};
    const changed = applyCore(tree, update);
    assert.equal(tree.seq, 5);
    assert.deepEqual([...changed], ['deployments', 'configs']);
    assert.equal(tree.configs.get(2)[0].eventId, 42);
    assert.equal(applyCore(tree, update).size, 0);
    assert.equal(applyCore(tree, {seq: 4, configEvents: [{configId: 2, eventType: 3}]}).size, 0);
    assert.equal(tree.configs.get(2).length, 1);
});

test('observed clocks merge by clock, drop replays at or below the sequence, and advance it', () => {
    const tree = createTree();
    applySnapshot(tree, {seq: 8, deploymentEvents: [deployment()], scheduledInstanceEvents: [instance(11)], nodeEvents: [{nodeId: 1, version: 1, value: {}}]});
    const at = nanos => Object.defineProperty(new Date(1000), 'epochNanoseconds', {value: nanos});
    const status = nanos => ({scheduledInstanceId: 11, updatedAt: at(nanos), preparer: {deploymentSpecVersion: 1}});
    const newer = status(1000000002n), older = status(1000000001n);
    assert.equal(applyObserved(tree, {seq: 7, instanceStatuses: [older], nodeStatuses: [{nodeId: 1, updatedAt: 9, isConnected: false}]}).size, 0, 'a replay at or below the snapshot sequence is dropped');
    assert.equal(tree.seq, 8);
    applyObserved(tree, {seq: 9, instanceStatuses: [newer], nodeStatuses: [{nodeId: 1, updatedAt: 10, isConnected: true}]});
    applyObserved(tree, {seq: 10, instanceStatuses: [older], nodeStatuses: [{nodeId: 1, updatedAt: 9, isConnected: false}]});
    assert.equal(tree.instanceStatuses.get(11), newer);
    assert.equal(tree.nodeStatuses.get(1).isConnected, true);
    assert.equal(observedClock(newer.updatedAt) - observedClock(older.updatedAt), 1n);
    assert.equal(tree.seq, 10, 'observed messages advance the sequence');
});

test('backup status updates without advancing the authored sequence', () => {
    const tree = createTree();
    applySnapshot(tree, {seq: 8, backupStatus: {assetPending: 2}});
    assert.equal(tree.backupStatus.assetPending, 2);
    const status = {assetPending: 0};
    assert.deepEqual([...applyBackupStatus(tree, status)], ['backupStatus']);
    assert.equal(tree.backupStatus, status);
    assert.equal(tree.seq, 8);
});

test('snapshot replaces every collection even when its sequence goes backwards', () => {
    const tree = createTree();
    applyCore(tree, {seq: 9, deploymentEvents: [deployment()], spaces: [{id: 0, name: 'system'}], authzGrantEvents: [{authzGrantId: 1, eventType: 1}], nodeStatuses: [{nodeId: 1, updatedAt: 5}]});
    applySnapshot(tree, {seq: 2, spaces: [{id: 1, name: 'global'}], authzGrantEvents: []});
    assert.equal(tree.deployments.size, 0);
    assert.equal(tree.nodeStatuses.size, 0);
    assert.deepEqual([...tree.spaces.keys()], [1]);
    assert.equal(tree.authzGrants.size, 0);
    assert.equal(tree.seq, 2);
});

test('pins survive desired updates and prune once a newer instance supersedes the final run', () => {
    const tree = createTree();
    applySnapshot(tree, {seq: 1, deploymentEvents: [deployment(1), deployment(2), deployment(3)], scheduledInstanceEvents: [instance(10, 1, 2)]});
    assert.deepEqual([...tree.deployments.get(7).keys()], [1, 3]);
    applyCore(tree, {seq: 2, scheduledInstanceEvents: [instance(11, 3)]});
    assert.deepEqual([...tree.deployments.get(7).keys()], [3]);
    assert.deepEqual([...tree.scheduledInstances.keys()], [11]);
});

test('deployment tombstone retains live pins until finalization, then drops the entity', () => {
    const tree = createTree();
    applySnapshot(tree, {seq: 1, deploymentEvents: [deployment()], scheduledInstanceEvents: [instance(10)]});
    applyCore(tree, {seq: 2, deploymentEvents: [deployment(2, 3)]});
    assert.deepEqual([...tree.deployments.get(7).keys()], [1, 2]);
    applyCore(tree, {seq: 3, scheduledInstanceEvents: [instance(10, 1, 2)]});
    assert.equal(tree.deployments.size, 0);
    assert.equal(tree.scheduledInstances.size, 0);
});

test('event and legacy collection tombstones remove their entities', () => {
    const tree = createTree();
    applySnapshot(tree, {seq: 1, secretEvents: [{secretId: 2, version: 1}], networkPolicyEvents: [{networkPolicyId: 3, version: 1}], spaces: [{id: 0}]});
    applyCore(tree, {seq: 2, secretEvents: [{secretId: 2, version: 2, eventType: 3}], networkPolicyEvents: [{networkPolicyId: 3, version: 2, eventType: 3}], spaces: [{id: 0, deleted: true}]});
    assert.equal(tree.secrets.size + tree.networkPolicies.size + tree.spaces.size, 0);
});

test('late observed statuses for pruned instances do not create orphan state', () => {
 const tree=createTree();
 applySnapshot(tree,{seq:1,deploymentEvents:[deployment()],scheduledInstanceEvents:[instance(10,1,2)]});
 applyCore(tree,{seq:2,scheduledInstanceEvents:[instance(11)]});
 applyObserved(tree,{seq:2,instanceStatuses:[{scheduledInstanceId:10,updatedAt:new Date(100)}]});
 assert.equal(tree.instanceStatuses.has(10),false);
});

test('sidecars survive a core reset and an empty session list replaces the whole collection', () => {
    const tree = createTree();
    applySnapshot(tree, {seq: 10, backupStatus: {assetPending: 2}, agentSessions: [{id: 'mine'}]});
    applySnapshot(tree, {seq: 11, spaces: [{id: 1}]});
    assert.equal(tree.backupStatus.assetPending, 2);
    assert.equal(tree.agentSessions.size, 1);
    applyAgentSessions(tree, {items: []});
    assert.equal(tree.agentSessions.size, 0);
    assert.equal(tree.seq, 11);
    applySnapshot(tree, {seq: 0}, {preserveSidecars: false});
    assert.equal(tree.backupStatus, undefined);
});

test('observed tombstones clear live and reconnected views and retain their clock', () => {
    const snapshot = {seq: 40, deploymentEvents: [deployment()], scheduledInstanceEvents: [instance(11)], nodeEvents: [{nodeId: 1, version: 1, value: {}}]};
    const initial = {instanceStatuses: [{scheduledInstanceId: 11, updatedAt: new Date(10), runner: {status: 1}}], nodeStatuses: [{nodeId: 1, updatedAt: new Date(10), isConnected: true}]};
    const cleared = {instanceStatuses: [{scheduledInstanceId: 11, updatedAt: new Date(20), preparer: {}, runner: {lastRestartAt: new Date(0), networkDiagnostics: []}}], nodeStatuses: [{nodeId: 1, updatedAt: new Date(20), lastConnectedAt: new Date(0), isConnected: false}]};
    const live = createTree(), reconnect = createTree();
    applySnapshot(live, {...snapshot, ...initial});
    applyObserved(live, cleared);
    applySnapshot(reconnect, {...snapshot, ...cleared});
    for (const tree of [live, reconnect]) {
        assert.equal(tree.instanceStatuses.size, 0);
        assert.equal(tree.nodeStatuses.size, 0);
        applyObserved(tree, initial);
        assert.equal(tree.instanceStatuses.size, 0, 'delayed instance status must stay cleared');
        assert.equal(tree.nodeStatuses.size, 0, 'delayed node status must stay cleared');
        applyObserved(tree, {nodeStatuses: [{nodeId: 1, updatedAt: new Date(21), isConnected: true}]});
        assert.equal(tree.nodeStatuses.get(1).isConnected, true);
        assert.equal(tree.seq, 40);
    }
    assert.deepEqual(live, reconnect);
});


test('grant events merge by identity and deletes survive replay and reconnect', () => {
    const tree = createTree();
    const grant = (id, version = 1, eventType = 1) => ({authzGrantId: id, version, eventType, value: {userId: 7, templateId: 1, grant: {}}});
    applySnapshot(tree, {seq: 1, authzGrantEvents: [grant(10)]});
    applyCore(tree, {seq: 2, authzGrantEvents: [grant(11)]});
    assert.deepEqual([...tree.authzGrants.keys()], [10, 11]);
    applyCore(tree, {seq: 3, authzGrantEvents: [grant(10, 2, 3)]});
    assert.deepEqual([...tree.authzGrants.keys()], [11]);
    assert.equal(applyCore(tree, {seq: 2, authzGrantEvents: [grant(10)]}).size, 0);
    assert.deepEqual([...tree.authzGrants.keys()], [11]);
    const snapshotTree = createTree();
    applySnapshot(snapshotTree, {seq: 3, authzGrantEvents: [grant(11)]});
    assert.deepEqual(tree.authzGrants, snapshotTree.authzGrants);
    applySnapshot(tree, {seq: 4, authzGrantEvents: []});
    assert.equal(tree.authzGrants.size, 0);
});
