import {selectInstanceEvents} from './deploymentMerge.js';

export const CREATE = 1;
export const UPDATE = 2;
export const DELETE = 3;

export const DEPLOYMENT = 1, SCHEDULED_INSTANCE = 2, NODE = 3, SECRET = 4, CONFIG = 5, ASSET = 6, NETWORK_POLICY = 7, SPACE = 8, USER = 9,
    VALUE_DIRECTORY = 10, ASSET_DIRECTORY = 11, AUTHZ_GRANT_TEMPLATE = 12, AUTHZ_GRANT = 13, AUTHZ_GLOBAL_RULE = 14, SYSTEM_CONFIG = 15,
    SCHEDULED_INSTANCE_STATUS = 16, NODE_STATUS = 17, AGENT_SESSION = 18, USER_SESSION = 19;

const fields = [undefined, 'deployment', 'scheduledInstance', 'node', 'secret', 'config', 'asset', 'networkPolicy', 'space', 'user',
    'valueDirectory', 'assetDirectory', 'authzGrantTemplate', 'authzGrant', 'authzGlobalRule', 'systemConfig',
    'scheduledInstanceStatus', 'nodeStatus', 'agentSession', 'userSession'];
const entityOf = (entity, type) => entity?.value?.[fields[type]];

const documents = {
    [SPACE]: 'spaces', [USER]: 'users', [VALUE_DIRECTORY]: 'valueDirectories', [ASSET_DIRECTORY]: 'assetDirectories',
    [AUTHZ_GRANT_TEMPLATE]: 'authzGrantTemplates', [AUTHZ_GLOBAL_RULE]: 'authzGlobalRules',
    [AGENT_SESSION]: 'agentSessions', [USER_SESSION]: 'userSessions',
};
const histories = {[SECRET]: ['secrets', 'secretId'], [CONFIG]: ['configs', 'configId'], [ASSET]: ['assets', 'assetId']};
const sidecars = ['secretsStatus', 'backupStatus', 'ingressDiagnostics'];
const maps = ['deployments', 'scheduledInstances', 'instanceStatuses', 'nodes', 'nodeStatuses', 'secrets', 'configs', 'assets',
    'networkPolicies', 'authzGrants', ...Object.values(documents)];

export const createTree = () => ({
    seq: 0,
    instanceStatusClocks: new Map(),
    nodeStatusClocks: new Map(),
    ...Object.fromEntries(maps.map(name => [name, new Map()])),
    systemConfig: undefined,
    ...Object.fromEntries(sidecars.map(name => [name, undefined])),
});

// Preserve the HLC's sub-millisecond precision when comparing protobuf Dates.
export const observedClock = (clock) => {
    if (typeof clock?.epochNanoseconds === 'bigint') return clock.epochNanoseconds;
    if (clock instanceof Date) return BigInt(clock.getTime()) * 1000000n;
    return BigInt(clock || 0) * 1000000n;
};

// Retain clocks after tombstones clear the visible entry. A delayed packet must
// not resurrect an observation, including after a reconnect bootstrap.
const emptyPayload = value => {
    if (value instanceof Date) return value.getTime() === 0;
    if (Array.isArray(value)) return value.length === 0;
    if (value && typeof value === 'object') return Object.entries(value).every(([key, item]) => key === 'exitCode' ? item == null : emptyPayload(item));
    return !value;
};
const instanceTombstone = status => emptyPayload(status.preparer) && emptyPayload(status.runner);
const nodeTombstone = status => !status.isConnected && emptyPayload(status.lastConnectedAt) && !status.remoteAddress && !status.opendeployVersion && !status.runtimeVersions;

const observed = (map, clocks, id, status, tombstone) => {
    const clock = observedClock(status.updatedAt);
    if (clocks.has(id) && clock <= clocks.get(id)) return false;
    clocks.set(id, clock);
    if (tombstone(status)) map.delete(id);
    else map.set(id, status);
    return true;
};

export const latestDeployment = versions => {
    let latest;
    for (const record of versions?.values() || []) if (!latest || record.meta.version > latest.meta.version) latest = record;
    return latest;
};

// Deployment versions are kept as DeploymentRecord {deployment, meta}, the
// shape the get, history, and recently-deleted responses hand out, so the
// deployment helpers read both alike. A delete is a record of the last
// version under the next version number with meta.deleted set.
const deploymentRecord = (entity, env, meta) => ({
    deployment: entity,
    meta: {
        ...meta, version: Number(meta.version || 0), specVersion: Number(meta.specVersion || 0), createdTime: Number(meta.createdTime || 0),
        updatedSeq: env.seq, updatedTime: env.time, updatedActor: env.actor, deleted: false,
    },
});

// A reducer takes the commit envelope (seq, time, actor) and, for a create
// or update, the entity's meta as the server's rows hold it: creation time,
// last write, and the version counters of its type.
const reducers = {
    [DEPLOYMENT](tree, op, id, entity, env, meta) {
        const versions = tree.deployments.get(id) || new Map();
        let record;
        if (op === DELETE) {
            const prev = latestDeployment(versions);
            if (!prev) return undefined;
            record = {deployment: prev.deployment, meta: {...prev.meta, version: prev.meta.version + 1, updatedSeq: env.seq, updatedTime: env.time, updatedActor: env.actor, deleted: true}};
        } else {
            record = deploymentRecord(entity, env, meta);
        }
        versions.set(record.meta.version, record);
        tree.deployments.set(id, versions);
        return 'deployments';
    },
    [SCHEDULED_INSTANCE](tree, op, id, entity, env) {
        if (op === DELETE) tree.scheduledInstances.delete(id);
        else tree.scheduledInstances.set(id, {scheduledInstanceId: id, seq: env.seq, value: entity});
        return 'scheduledInstances';
    },
    [NODE](tree, op, id, entity, env, meta) {
        if (op === DELETE) tree.nodes.delete(id);
        else tree.nodes.set(id, {nodeId: id, seq: env.seq, eventTime: env.time, createdTime: Number(meta.createdTime || 0), value: entity});
        return 'nodes';
    },
    [NETWORK_POLICY](tree, op, id, entity, env) {
        if (op === DELETE) tree.networkPolicies.delete(id);
        else tree.networkPolicies.set(id, {networkPolicyId: id, seq: env.seq, eventTime: env.time, author: env.actor, value: entity});
        return 'networkPolicies';
    },
    [AUTHZ_GRANT](tree, op, id, entity, env, meta) {
        if (op === DELETE) tree.authzGrants.delete(id);
        else tree.authzGrants.set(id, {authzGrantId: id, seq: env.seq, author: env.actor, createdTime: Number(meta.createdTime || 0), value: entity});
        return 'authzGrants';
    },
    [SYSTEM_CONFIG](tree, op, id, entity, env) {
        tree.systemConfig = op === DELETE ? undefined : {seq: env.seq, ...entity};
        return 'systemConfig';
    },
    [SCHEDULED_INSTANCE_STATUS](tree, op, id, entity) {
        if (op === DELETE) {
            tree.instanceStatuses.delete(id);
            tree.instanceStatusClocks.delete(id);
            return 'instanceStatuses';
        }
        return observed(tree.instanceStatuses, tree.instanceStatusClocks, id, entity, instanceTombstone) ? 'instanceStatuses' : undefined;
    },
    [NODE_STATUS](tree, op, id, entity) {
        if (op === DELETE) {
            tree.nodeStatuses.delete(id);
            tree.nodeStatusClocks.delete(id);
            return 'nodeStatuses';
        }
        return observed(tree.nodeStatuses, tree.nodeStatusClocks, id, entity, nodeTombstone) ? 'nodeStatuses' : undefined;
    },
};
for (const [type, [name, idField]] of Object.entries(histories)) {
    reducers[type] = (tree, op, id, entity, env, meta) => {
        if (op === DELETE) tree[name].delete(id);
        else {
            const entry = {[idField]: id, seq: env.seq, eventTime: env.time, author: env.actor, eventType: op, valueVersion: Number(meta.valueVersion || 0), value: entity};
            tree[name].set(id, [...(tree[name].get(id) || []), entry]);
        }
        return name;
    };
}
for (const [type, name] of Object.entries(documents)) {
    reducers[type] = (tree, op, id, entity, env, meta) => {
        if (op === DELETE) tree[name].delete(id);
        else tree[name].set(id, {...entity, createdAt: new Date(Number(meta.createdTime || 0)), author: Number(meta.updatedActor || 0)});
        return name;
    };
}

// The row selector owns which finalized incarnation still belongs to the view.
// Pruning uses that same read, so reducers do not implement the ordinal rule.
const pruneVersions = tree => {
    const selected = selectInstanceEvents(tree);
    const held = new Set(selected.map(event => event.scheduledInstanceId));
    for (const [id, event] of tree.scheduledInstances) {
        if (event.value.state === 2 && !held.has(id)) tree.scheduledInstances.delete(id);
    }
    const pins = new Map();
    for (const event of selected) {
        const ref = event.value.deployment || {};
        if (!pins.has(ref.deploymentId)) pins.set(ref.deploymentId, new Set());
        pins.get(ref.deploymentId).add(ref.version);
    }
    for (const [id, versions] of tree.deployments) {
        const latest = latestDeployment(versions);
        const keep = pins.get(id) || new Set();
        if (!latest.meta.deleted || keep.size) keep.add(latest.meta.version);
        for (const version of versions.keys()) if (!keep.has(version)) versions.delete(version);
        if (!versions.size) tree.deployments.delete(id);
    }
};

// Statuses may precede their parent inside one message, so orphans are swept
// once the whole message has reduced instead of being refused on arrival.
const sweepOrphans = tree => {
    const changed = new Set();
    for (const [name, clocks, parents] of [['instanceStatuses', tree.instanceStatusClocks, tree.scheduledInstances], ['nodeStatuses', tree.nodeStatusClocks, tree.nodes]]) {
        for (const id of tree[name].keys()) {
            if (parents.has(id)) continue;
            tree[name].delete(id);
            changed.add(name);
        }
        for (const id of clocks.keys()) if (!parents.has(id)) clocks.delete(id);
    }
    return changed;
};

const applyEvent = (tree, event, changed) => {
    const seq = Number(event.seq || 0);
    if (seq && seq <= tree.seq) return;
    const env = {seq, time: Number(event.time || 0), actor: Number(event.actor || 0)};
    for (const mutation of event.mutations || []) {
        const value = mutation.value || {};
        const op = value.create ? CREATE : value.update ? UPDATE : value.delete ? DELETE : 0;
        if (!op) continue;
        const body = value.create || value.update || value.delete;
        const type = Number(body.entityType || 0);
        const entity = op === DELETE ? undefined : entityOf(body.entity, type);
        if (op !== DELETE && !entity) continue;
        const name = reducers[type]?.(tree, op, Number(body.entityId), entity, env, body.meta || {});
        if (name) changed.add(name);
    }
    if (seq > tree.seq) tree.seq = seq;
};

// A snapshot replaces the tree. Every entry opens its entity with a create
// under the envelope of its last write; a retained version of a deleted
// deployment is followed by its tombstone under the envelope of the delete.
const applySnapshot = (tree, snapshot, changed) => {
    for (const name of resetTree(tree)) changed.add(name);
    for (const entry of snapshot.entities || []) {
        const type = Number(entry.entityType || 0);
        const entity = entityOf(entry.entity, type);
        if (!entity) continue;
        const meta = entry.meta || {};
        const env = {seq: Number(meta.updatedSeq || 0), time: Number(meta.updatedTime || 0), actor: Number(meta.updatedActor || 0)};
        const id = Number(entry.entityId);
        const name = reducers[type]?.(tree, CREATE, id, entity, env, meta);
        if (name) changed.add(name);
        if (meta.deleted && type === DEPLOYMENT) reducers[type](tree, DELETE, id, undefined, env, meta);
    }
    const seq = Number(snapshot.seq || 0);
    if (seq > tree.seq) tree.seq = seq;
};

export function resetTree(tree, {preserveSidecars = true} = {}) {
    const held = preserveSidecars ? Object.fromEntries(sidecars.map(name => [name, tree[name]])) : {};
    Object.assign(tree, createTree(), held);
    return new Set([...maps, 'systemConfig', ...sidecars]);
}

export function applyMessage(tree, message) {
    const changed = new Set();
    if (message.snapshot) applySnapshot(tree, message.snapshot, changed);
    for (const event of message.events || []) applyEvent(tree, event, changed);
    if (changed.has('deployments') || changed.has('scheduledInstances')) pruneVersions(tree);
    for (const name of sweepOrphans(tree)) changed.add(name);
    for (const name of sidecars) {
        if (message[name] == null) continue;
        tree[name] = message[name];
        changed.add(name);
    }
    const seq = Number(message.seq || 0);
    if (seq > tree.seq) tree.seq = seq;
    return changed;
}

export function written(update, type) {
    for (const mutation of update?.mutations || []) {
        const value = mutation.value || {};
        const body = value.create || value.update;
        if (!body || Number(body.entityType || 0) !== type) continue;
        return {
            id: Number(body.entityId), seq: Number(update.seq || 0), time: Number(update.time || 0), actor: Number(update.actor || 0),
            created: Boolean(value.create), entity: entityOf(body.entity, type), meta: body.meta,
        };
    }
    return undefined;
}
