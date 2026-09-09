import {selectInstanceEvents} from './deploymentMerge.js';

const DELETE = 3;
const eventCollections = {
    nodeEvents: ['nodes', 'nodeId'],
    networkPolicyEvents: ['networkPolicies', 'networkPolicyId'],
    authzGrantEvents: ['authzGrants', 'authzGrantId'],
};
const historyCollections = {
    secretEvents: ['secrets', 'secretId'],
    configEvents: ['configs', 'configId'],
    assetEvents: ['assets', 'assetId'],
};
const flags = ['valueDirectories', 'assetDirectories', 'spaces', 'users'];
const sidecars = ['secretsStatus', 'backupStatus', 'ingressDiagnostics', 'agentSessions'];
const replacements = ['authzRuleTemplates', 'authzGlobalRules', 'systemConfig'];
const maps = ['deployments', 'scheduledInstances', 'instanceStatuses', 'nodes', 'nodeStatuses', 'secrets', 'configs', 'assets', 'networkPolicies', 'authzGrants', 'agentSessions', ...flags];

export const createTree = () => ({
    seq: 0,
    instanceStatusClocks: new Map(),
    nodeStatusClocks: new Map(),
    ...Object.fromEntries(maps.map(name => [name, new Map()])),
    ...Object.fromEntries([...replacements, ...sidecars.filter(name => name !== 'agentSessions')].map(name => [name, undefined])),
});

// Preserve the HLC's sub-millisecond precision when comparing protobuf Dates.
export const observedClock = (clock) => {
    if (typeof clock?.epochNanoseconds === 'bigint') return clock.epochNanoseconds;
    if (clock instanceof Date) return BigInt(clock.getTime()) * 1000000n;
    return BigInt(clock || 0) * 1000000n;
};

// Retain clocks after tombstones clear the visible entry. A delayed packet must
// not resurrect an observation, including after a reconnect snapshot.
const emptyPayload = value => {
    if (value instanceof Date) return value.getTime() === 0;
    if (Array.isArray(value)) return value.length === 0;
    if (value && typeof value === 'object') return Object.entries(value).every(([key, item]) => key === 'exitCode' ? item == null : emptyPayload(item));
    return !value;
};
const observed = (map, clocks, parents, items, idField, tombstone) => {
    let changed = false;
    for (const status of items || []) {
        const id = status[idField];
        if (!parents.has(id)) continue;
        const clock = observedClock(status.updatedAt);
        if (clocks.has(id) && clock <= clocks.get(id)) continue;
        clocks.set(id, clock);
        if (tombstone(status)) map.delete(id);
        else map.set(id, status);
        changed = true;
    }
    return changed;
};

const reduce = (tree, update, authored, snapshot) => {
    const changed = new Set();
    if (authored) {
        for (const event of update.deploymentEvents || []) {
            const versions = tree.deployments.get(event.deploymentId) || new Map();
            versions.set(event.version, event);
            tree.deployments.set(event.deploymentId, versions);
            changed.add('deployments');
        }
        for (const event of update.scheduledInstanceEvents || []) {
            if (event.eventType === DELETE) {
                tree.scheduledInstances.delete(event.scheduledInstanceId);
                tree.instanceStatuses.delete(event.scheduledInstanceId);
                tree.instanceStatusClocks.delete(event.scheduledInstanceId);
            } else tree.scheduledInstances.set(event.scheduledInstanceId, event);
            changed.add('scheduledInstances');
        }
        for (const [field, [name, idField]] of Object.entries(eventCollections)) {
            for (const event of update[field] || []) {
                if (event.eventType === DELETE) {
                    tree[name].delete(event[idField]);
                    if (name === 'nodes') {
                        tree.nodeStatuses.delete(event[idField]);
                        tree.nodeStatusClocks.delete(event[idField]);
                    }
                } else tree[name].set(event[idField], event);
                changed.add(name);
            }
        }
        for (const [field, [name, idField]] of Object.entries(historyCollections)) {
            for (const event of update[field] || []) {
                const id = event[idField];
                if (event.eventType === DELETE) tree[name].delete(id);
                else {
                    const events = tree[name].get(id) || [];
                    if (!events.some(previous => previous.version === event.version)) {
                        tree[name].set(id, [...events, event].sort((a, b) => a.version - b.version));
                    }
                }
                changed.add(name);
            }
        }
        for (const name of flags) {
            for (const item of update[name] || []) {
                if (item.deleted) tree[name].delete(item.id);
                else tree[name].set(item.id, item);
                changed.add(name);
            }
        }
        for (const name of replacements) {
            if (update[name] == null) continue;
            tree[name] = snapshot && name.startsWith('authz') ? {items: update[name]} : update[name];
            changed.add(name);
        }
    }
    if (changed.has('deployments') || changed.has('scheduledInstances')) pruneVersions(tree);
    return changed;
};

export const latestDeployment = versions => {
    let latest;
    for (const event of versions?.values() || []) if (!latest || event.version > latest.version) latest = event;
    return latest;
};

// The row selector owns which finalized incarnation still belongs to the view.
// Pruning uses that same read, so reducers do not implement the ordinal rule.
const pruneVersions = tree => {
    const selected = selectInstanceEvents(tree);
    const held = new Set(selected.map(event => event.scheduledInstanceId));
    for (const [id, event] of tree.scheduledInstances) {
        if (event.value.state === 2 && !held.has(id)) {
            tree.scheduledInstances.delete(id);
            tree.instanceStatuses.delete(id);
            tree.instanceStatusClocks.delete(id);
        }
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

export function applyObserved(tree, update) {
    const changed = new Set();
    const seq = Number(update.seq || 0);
    if (seq && seq <= tree.seq) return changed;
    if (observed(tree.instanceStatuses, tree.instanceStatusClocks, tree.scheduledInstances, update.instanceStatuses, 'scheduledInstanceId', status => emptyPayload(status.preparer) && emptyPayload(status.runner))) changed.add('instanceStatuses');
    if (observed(tree.nodeStatuses, tree.nodeStatusClocks, tree.nodes, update.nodeStatuses, 'nodeId', status => !status.isConnected && emptyPayload(status.lastConnectedAt) && !status.remoteAddress && !status.opendeployVersion)) changed.add('nodeStatuses');
    if (seq) tree.seq = seq;
    return changed;
}

const replaceSidecar = (tree, name, value) => {
    tree[name] = value;
    return new Set([name]);
};
export const applyBackupStatus = (tree, value) => replaceSidecar(tree, 'backupStatus', value);
export const applySecretsStatus = (tree, value) => replaceSidecar(tree, 'secretsStatus', value);
export const applyIngressDiagnostics = (tree, value) => replaceSidecar(tree, 'ingressDiagnostics', value);
export const applyAgentSessions = (tree, value) => replaceSidecar(tree, 'agentSessions', new Map((value.items || []).map(item => [item.id, item])));

export function applySnapshot(tree, snapshot, {preserveSidecars = true} = {}) {
    const held = preserveSidecars ? Object.fromEntries(sidecars.map(name => [name, tree[name]])) : {};
    Object.assign(tree, createTree(), held);
    reduce(tree, snapshot, true, true);
    applyObserved(tree, snapshot);
    for (const name of sidecars) {
        if (snapshot[name] == null) continue;
        if (name === 'agentSessions') applyAgentSessions(tree, {items: snapshot[name]});
        else tree[name] = snapshot[name];
    }
    tree.seq = Number(snapshot.seq || 0);
    return new Set([...maps, ...replacements, ...sidecars]);
}

export function applyCore(tree, update) {
    if (Number(update.seq || 0) <= tree.seq) return new Set();
    const changed = reduce(tree, update, true, false);
    for (const name of applyObserved(tree, update)) changed.add(name);
    tree.seq = Number(update.seq);
    return changed;
}
