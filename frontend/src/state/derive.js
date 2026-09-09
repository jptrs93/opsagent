import van from 'vanjs-core';
import {deriveDeploymentRows} from './deploymentMerge.js';

// deploymentsS is the one-row-per-desired-deployment UI view. Each row merges
// the latest desired config with all non-final scheduled instances and keeps
// newest-instance aliases for consumers that only need one runtime.
export const deploymentsS = van.state([]);
// usersMapS holds a Map<userId, {name, createdAt, lastLoginAt}> for resolving
// display names and account dates. Timestamps are unix millis, 0 when unknown.
export const usersMapS = van.state(new Map());
export const machinesS = van.state([]);
export const nodesS = van.state([]);
export const nodeStatusesS = van.state([]);
export const enrollmentsS = van.state([]);
// agentSessionsS holds only the signed-in user's own agent sessions; the server
// filters the stream before sending them.
export const agentSessionsS = van.state([]);
export const secretRefsS = van.state([]);
export const userConfigRefsS = van.state([]);
export const secretsStatusS = van.state(null);
export const backupStatusS = van.state(null);
export const secretMetasS = van.state([]);
export const userConfigsS = van.state([]);
// valueDirectoriesS holds the shared secrets/configs folder tree across all
// spaces; parentId 0 is a space's implicit root.
export const valueDirectoriesS = van.state([]);
export const assetMetasS = van.state([]);
// assetDirectoriesS holds the asset folder tree across all spaces; parentId 0
// is a space's implicit root. Directories carry `key`, not `name`.
export const assetDirectoriesS = van.state([]);
export const systemConfigS = van.state(null);
// Templates and global rules use replacements; grants derive from live events.
export const authzTemplatesS = van.state([]);
export const authzGrantsS = van.state([]);
export const authzGlobalRulesS = van.state([]);
// Network policy rows derive the latest live event per policy.
export const networkPoliciesS = van.state([]);
// ingressDiagnosticsS arrives as a full snapshot whenever the primary
// re-renders the cluster network map: per-deployment ingress warnings.
export const ingressDiagnosticsS = van.state([]);
export const SEEDED_SPACES = [{id: 0, name: '_system'}, {id: 1, name: 'global'}];

export const spacesS = van.state(SEEDED_SPACES);

export const seqS = van.state(0);
const sortByName = items => [...items].sort((a, b) => (a.name || '').localeCompare(b.name || '') || Number(a.id) - Number(b.id));
const sortAssets = items => [...items].sort((a, b) => (a.key || '').localeCompare(b.key || '') || Number(a.id) - Number(b.id));

// A value pin addresses the event that changed its value facet, never a later
// rename or space move carrying that value forward.
export const valueVersions = history => history.filter((event, i) => i === 0 || event.valueVersion !== history[i - 1].valueVersion)
    .map(event => ({id: event.eventId, version: event.valueVersion, createdAt: new Date(event.eventTime), author: event.author,
        value: event.value.value, sha256: event.value.sha256, sizeBytes: event.value.sizeBytes, globalSeq: event.seq})).reverse();

const spaceVersions = history => history.filter((event, i) => i === 0 || event.spaceVersion !== history[i - 1].spaceVersion)
    .map(event => ({id: event.eventId, spaceId: event.value.spaceId, createdAt: new Date(event.eventTime), author: event.author, globalSeq: event.seq})).reverse();

const valueViewModel = (history, idField) => {
    const event = history.at(-1);
    if (!event) return undefined;
    return {
        id: event[idField], version: event.version, seq: event.seq, fs: event.value.fs,
        spaceId: event.value.spaceId, spaceVersions: spaceVersions(history),
        name: event.value.fs?.name || '', key: event.value.fs?.key || '',
        valueDirectoryId: Number(event.value.fs?.directoryId || 0), directoryId: Number(event.value.fs?.directoryId || 0),
        deleted: event.eventType === 3,
    };
};
export const secretViewModel = history => ({...valueViewModel(history, 'secretId'), versions: valueVersions(history)});
export const configViewModel = history => ({...valueViewModel(history, 'configId'), valueVersions: valueVersions(history)});
export const assetViewModel = history => ({...valueViewModel(history, 'assetId'), contentVersions: valueVersions(history)});

export const expandValueVersionRefs = metas => (metas || []).flatMap(meta => (meta.valueVersions || meta.versions || []).map(ref => ({
    id: ref.id, stableId: meta.id, name: meta.name, spaceId: meta.spaceId, directoryId: Number(meta.valueDirectoryId || 0),
    version: ref.version, value: ref.value, createdAt: ref.createdAt, author: ref.author,
})));

const memberStatus = status => status >= 4 && status <= 7;
export const nodeViewModel = event => ({
    id: event.nodeId, version: event.version, seq: event.seq,
    name: event.value.operator?.name || '', identifier: event.value.reported?.identifier || '',
    roles: event.value.operator?.roles || [], allowedSpaces: event.value.operator?.allowedSpaces || [],
    enrolledAt: new Date(event.value.operator?.enrolledTime || 0),
    status: event.value.status, enrollmentRequestedAt: event.value.enrollmentRequestedAt,
    addresses: event.value.reported?.underlayAddress ? [event.value.reported.underlayAddress] : [],
    hostAddresses: event.value.reported?.hostAddresses || [], wgPublicKey: event.value.reported?.wgPublicKey || '',
});

export const authzGrantViewModel = event => ({
    ...event.value, id: event.authzGrantId, author: event.author, createdAt: event.createdTime,
});

export function deriveEnrollments(tree) {
    return [...tree.nodes.values()].filter(event => event.value.enrollmentRequestedAt || !memberStatus(event.value.status)).map(event => {
        const observed = tree.nodeStatuses.get(event.nodeId) || {};
        return {
            id: event.nodeId, version: event.version,
            createdAt: new Date(event.value.enrollmentRequestedAt || event.createdTime),
            requestingMachineId: event.value.reported?.identifier || '', requestingIpAddress: observed.remoteAddress || '',
            underlayAddress: event.value.reported?.underlayAddress || '', hostAddresses: event.value.reported?.hostAddresses || [],
            opendeployVersion: observed.opendeployVersion || '', isConnected: observed.isConnected === true,
            status: event.value.enrollmentRequestedAt ? 1 : event.value.status,
        };
    }).sort((a, b) => b.createdAt - a.createdAt || b.id - a.id);
}

// Called once after an entire transaction has reduced. VanJS batches these
// assignments into one render; unaffected collection states retain identity.
export function publishDerived(tree, changed) {
    const any = (...names) => names.some(name => changed.has(name));
    seqS.val = tree.seq;
    if (any('deployments', 'scheduledInstances', 'instanceStatuses')) deploymentsS.val = deriveDeploymentRows(tree);
    if (any('nodes', 'nodeStatuses')) {
        nodesS.val = sortByName([...tree.nodes.values()].filter(e => memberStatus(e.value.status)).map(nodeViewModel));
        nodeStatusesS.val = [...tree.nodeStatuses.values()];
        enrollmentsS.val = deriveEnrollments(tree);
        machinesS.val = nodesS.val.map(node => {
            const status = tree.nodeStatuses.get(node.id) || {};
            return {...node, isPrimary: node.roles.includes(0), connected: status.isConnected === true, connectedAt: status.lastConnectedAt};
        });
    }
    if (any('users')) usersMapS.val = new Map([...tree.users].map(([id, user]) => [id, {...user, createdAt: Number(user.createdAt || 0), lastLoginAt: Number(user.lastLoginAt || 0)}]));
    if (any('agentSessions')) agentSessionsS.val = [...tree.agentSessions.values()].sort((a, b) => new Date(b.createdAt) - new Date(a.createdAt));
    if (any('secrets')) {secretMetasS.val = sortByName([...tree.secrets.values()].map(secretViewModel)); secretRefsS.val = expandValueVersionRefs(secretMetasS.val);}
    if (any('configs')) {userConfigsS.val = sortByName([...tree.configs.values()].map(configViewModel)); userConfigRefsS.val = expandValueVersionRefs(userConfigsS.val);}
    if (any('assets')) assetMetasS.val = sortAssets([...tree.assets.values()].map(assetViewModel));
    if (any('spaces')) spacesS.val = [...tree.spaces.values()].sort((a, b) => a.id - b.id);
    if (any('valueDirectories')) valueDirectoriesS.val = sortByName([...tree.valueDirectories.values()]);
    if (any('assetDirectories')) assetDirectoriesS.val = sortAssets([...tree.assetDirectories.values()]);
    if (any('networkPolicies')) networkPoliciesS.val = [...tree.networkPolicies.values()].map(e => ({...e.value, id: e.networkPolicyId, version: e.version})).sort((a, b) => a.id - b.id);
    if (any('authzGrants')) authzGrantsS.val = [...tree.authzGrants.values()].map(authzGrantViewModel).sort((a, b) => a.id - b.id);
    for (const [field, state] of [['authzRuleTemplates', authzTemplatesS], ['authzGlobalRules', authzGlobalRulesS], ['ingressDiagnostics', ingressDiagnosticsS]]) {
        if (any(field)) state.val = tree[field]?.items || [];
    }
    for (const [field, state] of [['secretsStatus', secretsStatusS], ['backupStatus', backupStatusS], ['systemConfig', systemConfigS]]) {
        if (any(field)) state.val = tree[field] || null;
    }
}
