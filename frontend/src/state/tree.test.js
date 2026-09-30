import assert from 'node:assert/strict';
import test from 'node:test';
import {applyMessage, createTree, observedClock, resetTree} from './tree.js';

const DEPLOYMENT = 1, SCHEDULED_INSTANCE = 2, NODE = 3, SECRET = 4, CONFIG = 5, NETWORK_POLICY = 7, SPACE = 8, AUTHZ_GRANT = 13,
    SYSTEM_CONFIG = 15, SCHEDULED_INSTANCE_STATUS = 16, NODE_STATUS = 17, AGENT_SESSION = 18, USER_SESSION = 19;
const fields = {[DEPLOYMENT]: 'deployment', [SCHEDULED_INSTANCE]: 'scheduledInstance', [NODE]: 'node', [SECRET]: 'secret', [CONFIG]: 'config',
    [NETWORK_POLICY]: 'networkPolicy', [SPACE]: 'space', [AUTHZ_GRANT]: 'authzGrant', [SYSTEM_CONFIG]: 'systemConfig',
    [SCHEDULED_INSTANCE_STATUS]: 'scheduledInstanceStatus', [NODE_STATUS]: 'nodeStatus', [AGENT_SESSION]: 'agentSession', [USER_SESSION]: 'userSession'};

export const create = (type, id, entity) => ({create: {entityType: type, entityId: id, entity: {[fields[type]]: entity}}});
export const update = (type, id, entity) => ({update: {entityType: type, entityId: id, entity: {[fields[type]]: entity}}});
export const remove = (type, id) => ({delete: {entityType: type, entityId: id}});
export const event = (seq, mutations, actor = 0) => ({seq, time: seq * 1000, actor, mutations});
export const message = (events, extra = {}) => ({events, seq: events.at(-1)?.seq || 0, ...extra});

export const deployment = (version = 1) => ({version, specVersion: version, createdTime: new Date(0), name: 'api', spaceId: 1, spec: {container1Spec: {running: false}}, scheduling: {running: true}});
export const instance = (id, deploymentVersion = 1, state = 0, instanceOrdinal = 0) => ({id, deploymentId: 7, deploymentVersion, state, instanceOrdinal});
const deploymentEvents = (...versions) => versions.map((version, i) => event(i + 1, [(version === 1 ? create : update)(DEPLOYMENT, 7, deployment(version))]));

test('events fold every collection, advance the seq, and ignore replays', () => {
    const tree = createTree();
    const msg = message([event(5, [create(DEPLOYMENT, 7, deployment()), create(CONFIG, 2, {fs: {name: 'c'}, spaceId: 1, value: 'one', valueVersion: 1})], 9)]);
    const changed = applyMessage(tree, msg);
    assert.equal(tree.seq, 5);
    assert.deepEqual([...changed], ['deployments', 'configs']);
    assert.deepEqual(tree.configs.get(2).map(e => [e.seq, e.author, e.valueVersion, e.value.value]), [[5, 9, 1, 'one']]);
    assert.equal(tree.deployments.get(7).get(1).eventTime.getTime(), 5000);
    assert.equal(applyMessage(tree, msg).size, 0);
    assert.equal(applyMessage(tree, message([event(4, [remove(CONFIG, 2)])])).size, 0);
    assert.equal(tree.configs.get(2).length, 1);
    applyMessage(tree, message([event(6, [update(CONFIG, 2, {fs: {name: 'c'}, spaceId: 1, value: 'two', valueVersion: 2})])]));
    assert.deepEqual(tree.configs.get(2).map(e => e.valueVersion), [1, 2]);
});

test('observed statuses merge by clock, tolerate arriving before their parent, and drop stale replays', () => {
    const tree = createTree();
    const at = nanos => Object.defineProperty(new Date(1000), 'epochNanoseconds', {value: nanos});
    const status = nanos => ({scheduledInstanceId: 11, updatedAt: at(nanos), preparer: {deploymentSpecVersion: 1}});
    const newer = status(1000000002n), older = status(1000000001n);
    applyMessage(tree, message([
        event(7, [create(SCHEDULED_INSTANCE_STATUS, 11, newer), create(NODE_STATUS, 1, {nodeId: 1, updatedAt: 10, isConnected: true})]),
        event(8, [create(DEPLOYMENT, 7, deployment()), create(SCHEDULED_INSTANCE, 11, instance(11)), create(NODE, 1, {})]),
    ]));
    assert.equal(tree.instanceStatuses.get(11), newer, 'a status ahead of its parent in the same message is kept');
    assert.equal(tree.nodeStatuses.get(1).isConnected, true);
    const changed = applyMessage(tree, message([event(9, [update(SCHEDULED_INSTANCE_STATUS, 11, older), update(NODE_STATUS, 1, {nodeId: 1, updatedAt: 9, isConnected: false})])]));
    assert.equal(changed.size, 0, 'older clocks do not replace newer observations');
    assert.equal(tree.instanceStatuses.get(11), newer);
    assert.equal(observedClock(newer.updatedAt) - observedClock(older.updatedAt), 1n);
    assert.equal(tree.seq, 9, 'the message seq advances even when nothing changed');
});

test('sidecars update without advancing the seq and survive a reset', () => {
    const tree = createTree();
    applyMessage(tree, message([event(8, [create(SPACE, 1, {id: 1, name: 'global'})])]));
    const status = {assetPending: 2};
    assert.deepEqual([...applyMessage(tree, {backupStatus: status})], ['backupStatus']);
    assert.equal(tree.backupStatus, status);
    assert.equal(tree.seq, 8);
    applyMessage(tree, message([event(12, [create(SPACE, 2, {id: 2, name: 'other'})])], {reset: true}));
    assert.equal(tree.backupStatus, status);
    assert.deepEqual([...tree.spaces.keys()], [2]);
    assert.equal(tree.seq, 12);
    resetTree(tree, {preserveSidecars: false});
    assert.equal(tree.backupStatus, undefined);
});

test('a reset replaces every collection even when its sequence goes backwards', () => {
    const tree = createTree();
    applyMessage(tree, message([event(9, [create(DEPLOYMENT, 7, deployment()), create(SPACE, 0, {id: 0, name: 'system'}), create(AUTHZ_GRANT, 1, {userId: 7}), create(NODE, 1, {}), create(NODE_STATUS, 1, {nodeId: 1, updatedAt: 5, isConnected: true})])]));
    applyMessage(tree, message([event(2, [create(SPACE, 1, {id: 1, name: 'global'})])], {reset: true}));
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
    applyMessage(tree, message([event(1, [create(SECRET, 2, {valueVersion: 1}), create(NETWORK_POLICY, 3, {action: 1}), create(SPACE, 4, {id: 4}), create(NODE, 1, {}), create(NODE_STATUS, 1, {nodeId: 1, updatedAt: 5, isConnected: true})])]));
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
    const parents = [event(40, [create(DEPLOYMENT, 7, deployment()), create(SCHEDULED_INSTANCE, 11, instance(11)), create(NODE, 1, {})])];
    const initial = [create(SCHEDULED_INSTANCE_STATUS, 11, {scheduledInstanceId: 11, updatedAt: new Date(10), runner: {status: 1}}), create(NODE_STATUS, 1, {nodeId: 1, updatedAt: new Date(10), isConnected: true})];
    const cleared = [update(SCHEDULED_INSTANCE_STATUS, 11, {scheduledInstanceId: 11, updatedAt: new Date(20), preparer: {}, runner: {lastRestartAt: new Date(0), networkDiagnostics: []}}), update(NODE_STATUS, 1, {nodeId: 1, updatedAt: new Date(20), lastConnectedAt: new Date(0), isConnected: false})];
    const live = createTree(), reconnect = createTree();
    applyMessage(live, message([...parents, event(41, initial)]));
    applyMessage(live, message([event(42, cleared)]));
    applyMessage(reconnect, message([...parents, event(42, cleared)], {reset: true}));
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

test('heartbeats advance the seq the next connection resumes from', () => {
    const tree = createTree();
    applyMessage(tree, message([event(3, [create(SPACE, 1, {id: 1})])]));
    applyMessage(tree, {seq: 9, heartbeat: true});
    assert.equal(tree.seq, 9);
    assert.equal(applyMessage(tree, message([event(8, [create(SPACE, 2, {id: 2})])])).size, 0, 'an event at or below the seq is a replay');
});

test('grant events merge by identity and deletes survive replay and reconnect', () => {
    const tree = createTree();
    const grant = id => ({userId: 7, templateId: 1, grant: {}, author: 4, createdTime: id});
    applyMessage(tree, message([event(1, [create(AUTHZ_GRANT, 10, grant(10))])]));
    applyMessage(tree, message([event(2, [create(AUTHZ_GRANT, 11, grant(11))])]));
    assert.deepEqual([...tree.authzGrants.keys()], [10, 11]);
    applyMessage(tree, message([event(3, [remove(AUTHZ_GRANT, 10)])]));
    assert.deepEqual([...tree.authzGrants.keys()], [11]);
    assert.equal(applyMessage(tree, message([event(2, [create(AUTHZ_GRANT, 10, grant(10))])])).size, 0);
    const reconnected = createTree();
    applyMessage(reconnected, message([event(2, [create(AUTHZ_GRANT, 11, grant(11))])], {reset: true}));
    applyMessage(reconnected, {seq: 3, heartbeat: true});
    assert.deepEqual(tree.authzGrants, reconnected.authzGrants);
    assert.deepEqual(tree.authzGrants.get(11), {authzGrantId: 11, seq: 2, author: 4, createdTime: 11, value: grant(11)});
});
