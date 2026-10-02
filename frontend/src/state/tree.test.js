import assert from 'node:assert/strict';
import test from 'node:test';
import {CREATE, DELETE, UPDATE, applyMessage, createTree, deploymentFromRecord, observedClock, resetTree, written} from './tree.js';

const DEPLOYMENT = 1, SCHEDULED_INSTANCE = 2, NODE = 3, SECRET = 4, CONFIG = 5, NETWORK_POLICY = 7, SPACE = 8, AUTHZ_GRANT = 13,
    SYSTEM_CONFIG = 15, SCHEDULED_INSTANCE_STATUS = 16, NODE_STATUS = 17, AGENT_SESSION = 18, USER_SESSION = 19;
const fields = {[DEPLOYMENT]: 'deployment', [SCHEDULED_INSTANCE]: 'scheduledInstance', [NODE]: 'node', [SECRET]: 'secret', [CONFIG]: 'config',
    [NETWORK_POLICY]: 'networkPolicy', [SPACE]: 'space', [AUTHZ_GRANT]: 'authzGrant', [SYSTEM_CONFIG]: 'systemConfig',
    [SCHEDULED_INSTANCE_STATUS]: 'scheduledInstanceStatus', [NODE_STATUS]: 'nodeStatus', [AGENT_SESSION]: 'agentSession', [USER_SESSION]: 'userSession'};

export const create = (type, id, entity, meta) => ({create: {entityType: type, entityId: id, entity: {[fields[type]]: entity}, ...(meta ? {meta} : {})}});
export const update = (type, id, entity, meta) => ({update: {entityType: type, entityId: id, entity: {[fields[type]]: entity}, ...(meta ? {meta} : {})}});
export const remove = (type, id) => ({delete: {entityType: type, entityId: id}});
export const event = (seq, mutations, actor = 0) => ({seq, time: seq * 1000, actor, mutations});
export const message = (events, extra = {}) => ({events, seq: events.at(-1)?.seq || 0, ...extra});
// A snapshot entry's meta carries the envelope of the entity's last write.
export const entry = (type, id, entity, meta = {}) => ({entityType: type, entityId: id, entity: {[fields[type]]: entity}, meta: {updatedTime: (meta.updatedSeq || 0) * 1000, ...meta}});
export const snapshot = (seq, entities) => ({snapshot: {seq, entities}, seq, synced: true});

export const deployment = () => ({name: 'api', spaceId: 1, spec: {container1Spec: {running: false}}, scheduling: {running: true}});
export const deploymentMeta = (version = 1, extra = {}) => ({version, specVersion: version, createdTime: 0, ...extra});
export const instance = (id, deploymentVersion = 1, state = 0, instanceOrdinal = 0) => ({id, deploymentId: 7, deploymentVersion, state, instanceOrdinal});
const deploymentEvents = (...versions) => versions.map((version, i) => event(i + 1, [(version === 1 ? create : update)(DEPLOYMENT, 7, deployment(), deploymentMeta(version))]));
const config = value => ({fs: {name: 'c'}, spaceId: 1, value});

test('events fold every collection, advance the seq, and ignore replays', () => {
    const tree = createTree();
    const msg = message([event(5, [create(DEPLOYMENT, 7, deployment(), deploymentMeta()), create(CONFIG, 2, config('one'), {valueVersion: 1})], 9)]);
    const changed = applyMessage(tree, msg);
    assert.equal(tree.seq, 5);
    assert.deepEqual([...changed], ['deployments', 'configs']);
    assert.deepEqual(tree.configs.get(2).map(e => [e.seq, e.author, e.valueVersion, e.value.value]), [[5, 9, 1, 'one']]);
    assert.equal(tree.deployments.get(7).get(1).eventTime.getTime(), 5000);
    assert.equal(applyMessage(tree, msg).size, 0);
    assert.equal(applyMessage(tree, message([event(4, [remove(CONFIG, 2)])])).size, 0);
    assert.equal(tree.configs.get(2).length, 1);
    applyMessage(tree, message([event(6, [update(CONFIG, 2, config('two'), {valueVersion: 2})])]));
    assert.deepEqual(tree.configs.get(2).map(e => e.valueVersion), [1, 2]);
});

test('a snapshot replaces the tree with every retained version, value history, tombstone, and document meta', () => {
    const tree = createTree();
    applyMessage(tree, message([event(3, [create(SPACE, 9, {id: 9, name: 'stale'})])]));
    const changed = applyMessage(tree, snapshot(50, [
        entry(DEPLOYMENT, 7, {...deployment(), name: 'v1'}, deploymentMeta(1, {createdTime: 1000, updatedSeq: 10, updatedActor: 2})),
        entry(DEPLOYMENT, 7, {...deployment(), name: 'v3'}, deploymentMeta(3, {specVersion: 2, createdTime: 1000, updatedSeq: 12, updatedActor: 2})),
        entry(DEPLOYMENT, 8, deployment(), deploymentMeta(2, {createdTime: 2000, deleted: true, updatedSeq: 20, updatedActor: 3})),
        entry(SCHEDULED_INSTANCE, 10, instance(10, 1), {updatedSeq: 11}),
        entry(SCHEDULED_INSTANCE, 11, {id: 11, deploymentId: 8, deploymentVersion: 2, state: 0, instanceOrdinal: 0}, {updatedSeq: 19}),
        entry(CONFIG, 2, config('one'), {valueVersion: 1, createdTime: 5000, updatedSeq: 5, updatedActor: 9}),
        entry(CONFIG, 2, config('two'), {valueVersion: 2, createdTime: 5000, updatedSeq: 6, updatedActor: 9}),
        entry(USER_SESSION, 3, {id: 'u1'}, {createdTime: 7000, updatedSeq: 7, updatedActor: 1}),
    ]));
    assert.equal(changed.has('spaces'), true, 'a snapshot reports every collection as changed');
    assert.equal(tree.spaces.size, 0);
    assert.equal(tree.seq, 50);
    const versions = tree.deployments.get(7);
    assert.deepEqual([...versions.keys()], [1, 3]);
    assert.deepEqual([versions.get(3).specVersion, versions.get(3).createdTime.getTime(), versions.get(3).seq, versions.get(3).eventTime.getTime(), versions.get(3).author, versions.get(3).value.name], [2, 1000, 12, 12000, 2, 'v3']);
    const deleted = tree.deployments.get(8);
    assert.deepEqual([...deleted.keys()], [2, 3]);
    assert.deepEqual([deleted.get(3).eventType, deleted.get(3).seq, deleted.get(3).author], [3, 20, 3]);
    assert.deepEqual(tree.configs.get(2).map(e => [e.valueVersion, e.seq, e.eventTime, e.author, e.value.value]), [[1, 5, 5000, 9, 'one'], [2, 6, 6000, 9, 'two']]);
    assert.deepEqual(tree.userSessions.get(3), {id: 'u1', createdAt: new Date(7000), author: 1});
});

test('observed statuses merge by clock, tolerate arriving before their parent, and drop stale replays', () => {
    const tree = createTree();
    const at = nanos => Object.defineProperty(new Date(1000), 'epochNanoseconds', {value: nanos});
    const status = nanos => ({scheduledInstanceId: 11, updatedAt: at(nanos), preparer: {deploymentSpecVersion: 1}});
    const newer = status(1000000002n), older = status(1000000001n);
    applyMessage(tree, message([
        event(7, [create(SCHEDULED_INSTANCE_STATUS, 11, newer), create(NODE_STATUS, 1, {nodeId: 1, updatedAt: 10, isConnected: true})]),
        event(8, [create(DEPLOYMENT, 7, deployment(), deploymentMeta()), create(SCHEDULED_INSTANCE, 11, instance(11)), create(NODE, 1, {})]),
    ]));
    assert.equal(tree.instanceStatuses.get(11), newer, 'a status ahead of its parent in the same message is kept');
    assert.equal(tree.nodeStatuses.get(1).isConnected, true);
    const changed = applyMessage(tree, message([event(9, [update(SCHEDULED_INSTANCE_STATUS, 11, older), update(NODE_STATUS, 1, {nodeId: 1, updatedAt: 9, isConnected: false})])]));
    assert.equal(changed.size, 0, 'older clocks do not replace newer observations');
    assert.equal(tree.instanceStatuses.get(11), newer);
    assert.equal(observedClock(newer.updatedAt) - observedClock(older.updatedAt), 1n);
    assert.equal(tree.seq, 9, 'the message seq advances even when nothing changed');
});

test('sidecars update without advancing the seq and survive a snapshot', () => {
    const tree = createTree();
    applyMessage(tree, message([event(8, [create(SPACE, 1, {id: 1, name: 'global'})])]));
    const status = {assetPending: 2};
    assert.deepEqual([...applyMessage(tree, {backupStatus: status})], ['backupStatus']);
    assert.equal(tree.backupStatus, status);
    assert.equal(tree.seq, 8);
    applyMessage(tree, snapshot(12, [entry(SPACE, 2, {id: 2, name: 'other'}, {updatedSeq: 12})]));
    assert.equal(tree.backupStatus, status);
    assert.deepEqual([...tree.spaces.keys()], [2]);
    assert.equal(tree.seq, 12);
    resetTree(tree, {preserveSidecars: false});
    assert.equal(tree.backupStatus, undefined);
});

test('a snapshot replaces every collection even when its sequence goes backwards', () => {
    const tree = createTree();
    applyMessage(tree, message([event(9, [create(DEPLOYMENT, 7, deployment(), deploymentMeta()), create(SPACE, 0, {id: 0, name: 'system'}), create(AUTHZ_GRANT, 1, {userId: 7}), create(NODE, 1, {}), create(NODE_STATUS, 1, {nodeId: 1, updatedAt: 5, isConnected: true})])]));
    applyMessage(tree, snapshot(2, [entry(SPACE, 1, {id: 1, name: 'global'}, {updatedSeq: 2})]));
    assert.equal(tree.deployments.size, 0);
    assert.equal(tree.nodeStatuses.size, 0);
    assert.deepEqual([...tree.spaces.keys()], [1]);
    assert.equal(tree.authzGrants.size, 0);
    assert.equal(tree.seq, 2);
});

test('pins survive desired updates and prune once a newer instance supersedes the final run', () => {
    const tree = createTree();
    applyMessage(tree, message([...deploymentEvents(1, 2, 3), event(4, [create(SCHEDULED_INSTANCE, 10, instance(10, 1, 2))])]));
    assert.deepEqual([...tree.deployments.get(7).keys()], [1, 3]);
    applyMessage(tree, message([event(5, [create(SCHEDULED_INSTANCE, 11, instance(11, 3))])]));
    assert.deepEqual([...tree.deployments.get(7).keys()], [3]);
    assert.deepEqual([...tree.scheduledInstances.keys()], [11]);
});

test('a deployment delete keeps live pins as a tombstone until finalization, then drops the entity', () => {
    const tree = createTree();
    applyMessage(tree, message([...deploymentEvents(1), event(2, [create(SCHEDULED_INSTANCE, 10, instance(10))])]));
    applyMessage(tree, message([event(3, [remove(DEPLOYMENT, 7)])]));
    const versions = tree.deployments.get(7);
    assert.deepEqual([...versions.keys()], [1, 2]);
    assert.equal(versions.get(2).eventType, 3);
    assert.equal(versions.get(2).seq, 3);
    applyMessage(tree, message([event(4, [update(SCHEDULED_INSTANCE, 10, instance(10, 1, 2))])]));
    assert.equal(tree.deployments.size, 0);
    assert.equal(tree.scheduledInstances.size, 0);
    assert.equal(applyMessage(tree, message([event(5, [remove(DEPLOYMENT, 8)])])).size, 0, 'a delete of an unknown deployment is ignored');
});

test('deletes remove histories, events, and documents', () => {
    const tree = createTree();
    applyMessage(tree, message([event(1, [create(SECRET, 2, {}, {valueVersion: 1}), create(NETWORK_POLICY, 3, {action: 1}), create(SPACE, 4, {id: 4}), create(NODE, 1, {}), create(NODE_STATUS, 1, {nodeId: 1, updatedAt: 5, isConnected: true})])]));
    applyMessage(tree, message([event(2, [remove(SECRET, 2), remove(NETWORK_POLICY, 3), remove(SPACE, 4), remove(NODE, 1)])]));
    assert.equal(tree.secrets.size + tree.networkPolicies.size + tree.spaces.size + tree.nodes.size + tree.nodeStatuses.size + tree.nodeStatusClocks.size, 0);
});

test('late observed statuses for pruned instances do not create orphan state', () => {
    const tree = createTree();
    applyMessage(tree, message([...deploymentEvents(1), event(2, [create(SCHEDULED_INSTANCE, 10, instance(10, 1, 2))])]));
    applyMessage(tree, message([event(3, [create(SCHEDULED_INSTANCE, 11, instance(11))])]));
    applyMessage(tree, message([event(4, [create(SCHEDULED_INSTANCE_STATUS, 10, {scheduledInstanceId: 10, updatedAt: new Date(100), runner: {status: 2}})])]));
    assert.equal(tree.instanceStatuses.has(10), false);
    assert.equal(tree.instanceStatusClocks.has(10), false);
});

test('sessions and system config are latest-only documents', () => {
    const tree = createTree();
    applyMessage(tree, message([event(10, [create(AGENT_SESSION, 1, {id: 'mine', status: 1}), create(USER_SESSION, 2, {id: 'u1'}), create(SYSTEM_CONFIG, 1, {settings: {a: 1}})])]));
    applyMessage(tree, message([event(11, [update(AGENT_SESSION, 1, {id: 'mine', status: 2}), create(USER_SESSION, 3, {id: 'u2'}), update(SYSTEM_CONFIG, 1, {settings: {a: 2}})])]));
    assert.equal(tree.agentSessions.get(1).status, 2);
    assert.equal(tree.userSessions.size, 2);
    assert.deepEqual(tree.systemConfig, {seq: 11, settings: {a: 2}});
});

test('observed tombstones clear live and reconnected views and retain their clock', () => {
    const parents = [event(40, [create(DEPLOYMENT, 7, deployment(), deploymentMeta()), create(SCHEDULED_INSTANCE, 11, instance(11)), create(NODE, 1, {})])];
    const initial = [create(SCHEDULED_INSTANCE_STATUS, 11, {scheduledInstanceId: 11, updatedAt: new Date(10), runner: {status: 1}}), create(NODE_STATUS, 1, {nodeId: 1, updatedAt: new Date(10), isConnected: true})];
    const clearedInstance = {scheduledInstanceId: 11, updatedAt: new Date(20), preparer: {}, runner: {lastRestartAt: new Date(0), networkDiagnostics: []}};
    const clearedNode = {nodeId: 1, updatedAt: new Date(20), lastConnectedAt: new Date(0), isConnected: false};
    const cleared = [update(SCHEDULED_INSTANCE_STATUS, 11, clearedInstance), update(NODE_STATUS, 1, clearedNode)];
    const live = createTree(), reconnect = createTree();
    applyMessage(live, message([...parents, event(41, initial)]));
    applyMessage(live, message([event(42, cleared)]));
    applyMessage(reconnect, snapshot(42, [
        entry(DEPLOYMENT, 7, deployment(), deploymentMeta(1, {updatedSeq: 40})), entry(SCHEDULED_INSTANCE, 11, instance(11), {updatedSeq: 40}), entry(NODE, 1, {}, {updatedSeq: 40}),
        entry(SCHEDULED_INSTANCE_STATUS, 11, clearedInstance, {updatedSeq: 42}), entry(NODE_STATUS, 1, clearedNode, {updatedSeq: 42}),
    ]));
    for (const tree of [live, reconnect]) {
        assert.equal(tree.instanceStatuses.size, 0);
        assert.equal(tree.nodeStatuses.size, 0);
        applyMessage(tree, message([event(43, initial)]));
        assert.equal(tree.instanceStatuses.size, 0, 'delayed instance status must stay cleared');
        assert.equal(tree.nodeStatuses.size, 0, 'delayed node status must stay cleared');
        applyMessage(tree, message([event(44, [update(NODE_STATUS, 1, {nodeId: 1, updatedAt: new Date(21), isConnected: true})])]));
        assert.equal(tree.nodeStatuses.get(1).isConnected, true);
        assert.equal(tree.seq, 44);
    }
    assert.deepEqual(live, reconnect);
});

test('heartbeats advance the seq replays are judged against', () => {
    const tree = createTree();
    applyMessage(tree, message([event(3, [create(SPACE, 1, {id: 1})])]));
    applyMessage(tree, {seq: 9, heartbeat: true});
    assert.equal(tree.seq, 9);
    assert.equal(applyMessage(tree, message([event(8, [create(SPACE, 2, {id: 2})])])).size, 0, 'an event at or below the seq is a replay');
});

test('grant events merge by identity and deletes survive replay and reconnect', () => {
    const tree = createTree();
    const grant = () => ({userId: 7, templateId: 1, spec: {}});
    applyMessage(tree, message([event(1, [create(AUTHZ_GRANT, 10, grant(), {createdTime: 10})], 4)]));
    applyMessage(tree, message([event(2, [create(AUTHZ_GRANT, 11, grant(), {createdTime: 11})], 4)]));
    assert.deepEqual([...tree.authzGrants.keys()], [10, 11]);
    applyMessage(tree, message([event(3, [remove(AUTHZ_GRANT, 10)])]));
    assert.deepEqual([...tree.authzGrants.keys()], [11]);
    assert.equal(applyMessage(tree, message([event(2, [create(AUTHZ_GRANT, 10, grant(), {createdTime: 10})])])).size, 0);
    const reconnected = createTree();
    applyMessage(reconnected, snapshot(2, [entry(AUTHZ_GRANT, 11, grant(), {createdTime: 11, updatedSeq: 2, updatedActor: 4})]));
    applyMessage(reconnected, {seq: 3, heartbeat: true});
    assert.deepEqual(tree.authzGrants, reconnected.authzGrants);
    assert.deepEqual(tree.authzGrants.get(11), {authzGrantId: 11, seq: 2, author: 4, createdTime: 11, value: grant()});
});

test('written reads the entity a write response addressed', () => {
    const response = event(9, [create(CONFIG, 2, config('one'), {valueVersion: 1, createdTime: 9000}), update(SCHEDULED_INSTANCE, 3, instance(3))], 4);
    assert.deepEqual(written(response, CONFIG), {id: 2, seq: 9, time: 9000, actor: 4, created: true,
        entity: config('one'), meta: {valueVersion: 1, createdTime: 9000}});
    assert.equal(written(response, SCHEDULED_INSTANCE).created, false);
    assert.equal(written(response, DEPLOYMENT), undefined);
    assert.equal(written(undefined, CONFIG), undefined);
});

test('deploymentFromRecord renders a REST record as the tree entry shape, a tombstone included', () => {
    const live = deploymentFromRecord({deployment: {id: 7, name: 'api', spaceId: 1}, meta: {createdTime: 1000, updatedTime: 5000, updatedSeq: 42, updatedActor: 3, version: 2, specVersion: 1}});
    assert.deepEqual([live.deploymentId, live.version, live.specVersion, live.eventType, live.seq, live.author, live.value.name], [7, 2, 1, UPDATE, 42, 3, 'api']);
    assert.deepEqual([live.eventTime.getTime(), live.createdTime.getTime()], [5000, 1000]);
    const first = deploymentFromRecord({deployment: {id: 7}, meta: {version: 1, specVersion: 1}});
    assert.equal(first.eventType, CREATE);
    const gone = deploymentFromRecord({deployment: {id: 7, name: 'api'}, meta: {version: 2, specVersion: 1, deleted: true, updatedSeq: 50}});
    assert.deepEqual([gone.eventType, gone.version, gone.seq], [DELETE, 2, 50]);
    assert.equal(deploymentFromRecord({status: {}}), null);
});
