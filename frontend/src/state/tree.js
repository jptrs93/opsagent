import {selectInstanceEvents} from './deploymentMerge.js';

export const CREATE = 1;
export const UPDATE = 2;
export const DELETE = 3;

const DEPLOYMENT = 1, SCHEDULED_INSTANCE = 2, NODE = 3, SECRET = 4, CONFIG = 5, ASSET = 6, NETWORK_POLICY = 7, SPACE = 8, USER = 9,
    VALUE_DIRECTORY = 10, ASSET_DIRECTORY = 11, AUTHZ_RULE_TEMPLATE = 12, AUTHZ_GRANT = 13, AUTHZ_GLOBAL_RULE = 14, SYSTEM_CONFIG = 15,
    SCHEDULED_INSTANCE_STATUS = 16, NODE_STATUS = 17, AGENT_SESSION = 18, USER_SESSION = 19;

const fields = [undefined, 'deployment', 'scheduledInstance', 'node', 'secret', 'config', 'asset', 'networkPolicy', 'space', 'user',
    'valueDirectory', 'assetDirectory', 'authzRuleTemplate', 'authzGrant', 'authzGlobalRule', 'systemConfig',
    'scheduledInstanceStatus', 'nodeStatus', 'agentSession', 'userSession'];

const documents = {
    [SPACE]: 'spaces', [USER]: 'users', [VALUE_DIRECTORY]: 'valueDirectories', [ASSET_DIRECTORY]: 'assetDirectories',
    [AUTHZ_RULE_TEMPLATE]: 'authzRuleTemplates', [AUTHZ_GLOBAL_RULE]: 'authzGlobalRules',
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
    for (const event of versions?.values() || []) if (!latest || event.version > latest.version) latest = event;
    return latest;
};

const reducers = {
    [DEPLOYMENT](tree, op, id, entity, meta) {
        const versions = tree.deployments.get(id) || new Map();
        let event;
        if (op === DELETE) {
            const prev = latestDeployment(versions);
            if (!prev) return undefined;
            event = {...prev, version: prev.version + 1, eventType: DELETE, seq: meta.seq, eventTime: new Date(meta.time), author: meta.actor};
        } else {
            event = {
                deploymentId: id, version: entity.version, specVersion: entity.specVersion, createdTime: entity.createdTime,
                eventType: op, seq: meta.seq, eventTime: new Date(meta.time), author: meta.actor, value: entity,
            };
        }
        versions.set(event.version, event);
        tree.deployments.set(id, versions);
        return 'deployments';
    },
    [SCHEDULED_INSTANCE](tree, op, id, entity, meta) {
        if (op === DELETE) tree.scheduledInstances.delete(id);
        else tree.scheduledInstances.set(id, {scheduledInstanceId: id, seq: meta.seq, value: entity});
        return 'scheduledInstances';
    },
    [NODE](tree, op, id, entity, meta) {
        if (op === DELETE) tree.nodes.delete(id);
        else tree.nodes.set(id, {nodeId: id, seq: meta.seq, eventTime: meta.time, createdTime: entity.createdTime, value: entity});
        return 'nodes';
    },
    [NETWORK_POLICY](tree, op, id, entity, meta) {
        if (op === DELETE) tree.networkPolicies.delete(id);
        else tree.networkPolicies.set(id, {networkPolicyId: id, seq: meta.seq, eventTime: meta.time, author: meta.actor, value: entity});
        return 'networkPolicies';
    },
    [AUTHZ_GRANT](tree, op, id, entity, meta) {
        if (op === DELETE) tree.authzGrants.delete(id);
        else tree.authzGrants.set(id, {authzGrantId: id, seq: meta.seq, author: entity.author, createdTime: entity.createdTime, value: entity});
        return 'authzGrants';
    },
    [SYSTEM_CONFIG](tree, op, id, entity, meta) {
        tree.systemConfig = op === DELETE ? undefined : {seq: meta.seq, ...entity};
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
    reducers[type] = (tree, op, id, entity, meta) => {
        if (op === DELETE) tree[name].delete(id);
        else {
            const entry = {[idField]: id, seq: meta.seq, eventTime: meta.time, author: meta.actor, eventType: op, valueVersion: entity.valueVersion, value: entity};
            tree[name].set(id, [...(tree[name].get(id) || []), entry]);
        }
        return name;
    };
}
for (const [type, name] of Object.entries(documents)) {
    reducers[type] = (tree, op, id, entity) => {
        if (op === DELETE) tree[name].delete(id);
        else tree[name].set(id, entity);
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
        const value = event.value;
        if (!pins.has(value.deploymentId)) pins.set(value.deploymentId, new Set());
        pins.get(value.deploymentId).add(value.deploymentVersion);
    }
    for (const [id, versions] of tree.deployments) {
        const latest = latestDeployment(versions);
        const keep = pins.get(id) || new Set();
        if (latest.eventType !== DELETE || keep.size) keep.add(latest.version);
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
    const meta = {seq, time: Number(event.time || 0), actor: Number(event.actor || 0)};
    for (const mutation of event.mutations || []) {
        const op = mutation.create ? CREATE : mutation.update ? UPDATE : mutation.delete ? DELETE : 0;
        if (!op) continue;
        const body = mutation.create || mutation.update || mutation.delete;
        const type = Number(body.entityType || 0);
        const entity = op === DELETE ? undefined : body.entity?.[fields[type]];
        if (op !== DELETE && !entity) continue;
        const name = reducers[type]?.(tree, op, Number(body.entityId), entity, meta);
        if (name) changed.add(name);
    }
    if (seq > tree.seq) tree.seq = seq;
};

export function resetTree(tree, {preserveSidecars = true} = {}) {
    const held = preserveSidecars ? Object.fromEntries(sidecars.map(name => [name, tree[name]])) : {};
    Object.assign(tree, createTree(), held);
    return new Set([...maps, 'systemConfig', ...sidecars]);
}

export function applyMessage(tree, message) {
    const changed = message.reset ? resetTree(tree) : new Set();
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
