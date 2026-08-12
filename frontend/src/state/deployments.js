import van from "vanjs-core";
import { capi } from "../capi/index.js";
import { loginS } from "./login.js";
import {applyScheduledInstanceUpdate, mergeDeploymentState} from "./deploymentMerge.js";

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
export const primaryConfigS = van.state(null);
// Authz collections arrive as full snapshots on every change rather than
// per-item updates, so applying them is a straight replacement.
export const authzTemplatesS = van.state([]);
export const authzGrantsS = van.state([]);
export const authzGlobalRulesS = van.state([]);
const SEEDED_SPACES = [{id: 0, name: '_system'}, {id: 1, name: 'global'}];

export const spacesS = van.state(SEEDED_SPACES);
export const deploymentsStreamS = van.state({
    status: 'offline',
    sentence: 'offline',
    lastError: '',
});

const STREAM_INACTIVITY_TIMEOUT_MS = 10000;

let activeToken = null;
let sessionGeneration = 0;
let reconnectAttempt = 0;
let streamAbortController = null;
let streamRetryTimer = null;
let streamInactivityTimer = null;
let desiredConfigsById = new Map();
let scheduledInstancesById = new Map();

const publishDeployments = () => {
    deploymentsS.val = mergeDeploymentState(desiredConfigsById, scheduledInstancesById);
};

const hasStateStreamAccess = () => loginS.val?.scopes?.includes('default') === true;

const setStreamState = (status, sentence, lastError = '') => {
    deploymentsStreamS.val = { status, sentence, lastError };
};

const clearInactivityTimer = () => {
    if (streamInactivityTimer) {
        clearTimeout(streamInactivityTimer);
        streamInactivityTimer = null;
    }
};

const armInactivityTimer = (generation) => {
    clearInactivityTimer();
    streamInactivityTimer = setTimeout(() => {
        if (!loginS.val || generation !== sessionGeneration) return;
        if (streamAbortController) {
            streamAbortController.abort();
        }
        scheduleReconnect(generation, 'stream heartbeat timed out');
    }, STREAM_INACTIVITY_TIMEOUT_MS);
};

const stopDeploymentsStream = ({ clearDeployments = false } = {}) => {
    if (streamRetryTimer) {
        clearTimeout(streamRetryTimer);
        streamRetryTimer = null;
    }
    clearInactivityTimer();
    if (streamAbortController) {
        streamAbortController.abort();
        streamAbortController = null;
    }
    reconnectAttempt = 0;
    if (clearDeployments) {
        desiredConfigsById = new Map();
        scheduledInstancesById = new Map();
        deploymentsS.val = [];
        usersMapS.val = new Map();
        machinesS.val = [];
        nodesS.val = [];
        nodeStatusesS.val = [];
        enrollmentsS.val = [];
        agentSessionsS.val = [];
        secretRefsS.val = [];
        userConfigRefsS.val = [];
        secretsStatusS.val = null;
        backupStatusS.val = null;
        secretMetasS.val = [];
        userConfigsS.val = [];
        valueDirectoriesS.val = [];
        assetMetasS.val = [];
        assetDirectoriesS.val = [];
        primaryConfigS.val = null;
        authzTemplatesS.val = [];
        authzGrantsS.val = [];
        authzGlobalRulesS.val = [];
        spacesS.val = SEEDED_SPACES;
    }
    setStreamState('offline', 'offline');
};

const handleStateMessage = (message) => {
    if (!message) return;

    if (message.deploymentConfigsSnapshot) {
        desiredConfigsById = new Map((message.deploymentConfigsSnapshot.items || [])
            .filter(config => config?.id && !config.deleted)
            .map(config => [Number(config.id), config]));
    }

    if (message.scheduledInstancesSnapshot) {
        // Finalized instances are kept: the snapshot carries the last one an
        // ordinal ran so a stopped deployment still shows how it ended.
        scheduledInstancesById = new Map((message.scheduledInstancesSnapshot.items || [])
            .filter(state => state?.instance?.id)
            .map(state => [Number(state.instance.id), state]));
    }

    if (message.deploymentConfigUpdate?.id) {
        const config = message.deploymentConfigUpdate;
        if (config.deleted) {
            desiredConfigsById.delete(Number(config.id));
        } else {
            desiredConfigsById.set(Number(config.id), config);
        }
    }

    if (message.scheduledInstanceUpdate?.instance?.id) {
        const update = message.scheduledInstanceUpdate;
        scheduledInstancesById = applyScheduledInstanceUpdate(scheduledInstancesById, update);
    }

    if (message.deploymentConfigsSnapshot || message.scheduledInstancesSnapshot ||
        message.deploymentConfigUpdate?.id || message.scheduledInstanceUpdate?.instance?.id) {
        publishDeployments();
    }

    if (message.usersSnapshot && message.usersSnapshot.length > 0) {
        const next = new Map();
        for (const u of message.usersSnapshot) {
            next.set(u.id, {name: u.name, createdAt: Number(u.createdAt || 0), lastLoginAt: Number(u.lastLoginAt || 0)});
        }
        usersMapS.val = next;
    }

    if (message.userUpdate?.id) {
        const u = message.userUpdate;
        const next = new Map(usersMapS.val);
        next.set(u.id, {name: u.name, createdAt: Number(u.createdAt || 0), lastLoginAt: Number(u.lastLoginAt || 0)});
        usersMapS.val = next;
    }

    if (message.nodesSnapshot) {
        nodesS.val = sortByName(message.nodesSnapshot.items || []);
        refreshMachinesFromNodes();
    }

    if (message.nodeUpdate?.id) {
        nodesS.val = applyItemUpdate(nodesS.val, message.nodeUpdate);
        refreshMachinesFromNodes();
    }

    if (message.nodeStatusesSnapshot) {
        nodeStatusesS.val = message.nodeStatusesSnapshot.items || [];
        refreshMachinesFromNodes();
    }

    if (message.nodeStatusUpdate?.id) {
        const next = new Map((nodeStatusesS.val || []).map((status) => [status.id, status]));
        next.set(message.nodeStatusUpdate.id, message.nodeStatusUpdate);
        nodeStatusesS.val = Array.from(next.values());
        refreshMachinesFromNodes();
    }

    if (message.enrollmentsSnapshot) {
        enrollmentsS.val = message.enrollmentsSnapshot.items || [];
    }

    if (message.enrollmentUpdate?.id) {
        const next = new Map((enrollmentsS.val || []).map((enrollment) => [enrollment.id, enrollment]));
        next.set(message.enrollmentUpdate.id, message.enrollmentUpdate);
        enrollmentsS.val = Array.from(next.values());
    }

    if (message.agentSessionsSnapshot) {
        agentSessionsS.val = message.agentSessionsSnapshot.items || [];
    }

    if (message.agentSessionUpdate?.id) {
        const update = message.agentSessionUpdate;
        const current = agentSessionsS.val || [];
        // A session new to us goes on the front: the server orders newest
        // first, and a session we have not seen before is the newest.
        agentSessionsS.val = current.some((s) => s.id === update.id)
            ? current.map((s) => (s.id === update.id ? update : s))
            : [update, ...current];
    }

    if (message.secretsStatusSnapshot) {
        secretsStatusS.val = message.secretsStatusSnapshot;
    }

    if (message.backupStatusSnapshot) {
        backupStatusS.val = message.backupStatusSnapshot;
    }

    if (message.backupStatusUpdate) {
        backupStatusS.val = message.backupStatusUpdate;
    }

    if (message.secretMetasSnapshot) {
        secretMetasS.val = sortByName(message.secretMetasSnapshot.items || []);
        secretRefsS.val = expandValueVersionRefs(secretMetasS.val);
    }

    if (message.secretMetaUpdate?.id) {
        secretMetasS.val = applyItemUpdate(secretMetasS.val, message.secretMetaUpdate);
        secretRefsS.val = expandValueVersionRefs(secretMetasS.val);
    }

    if (message.userConfigValuesSnapshot) {
        userConfigsS.val = sortByName(message.userConfigValuesSnapshot.items || []);
        userConfigRefsS.val = expandValueVersionRefs(userConfigsS.val);
    }

    if (message.userConfigValueUpdate?.id) {
        userConfigsS.val = applyItemUpdate(userConfigsS.val, message.userConfigValueUpdate);
        userConfigRefsS.val = expandValueVersionRefs(userConfigsS.val);
    }

    if (message.valueDirectoriesSnapshot) {
        valueDirectoriesS.val = sortByName(message.valueDirectoriesSnapshot.items || []);
    }

    if (message.valueDirectoryUpdate?.id) {
        valueDirectoriesS.val = applyItemUpdate(valueDirectoriesS.val, message.valueDirectoryUpdate);
    }

    if (message.spacesSnapshot) {
        spacesS.val = sortSpaces(message.spacesSnapshot.items || []);
    }

    if (message.spaceUpdate && message.spaceUpdate.id !== undefined) {
        spacesS.val = applySpaceUpdate(spacesS.val, message.spaceUpdate);
    }

    if (message.assetsSnapshot) {
        assetMetasS.val = sortAssets(message.assetsSnapshot.items || []);
    }

    if (message.assetUpdate && (message.assetUpdate.id || message.assetUpdate.key)) {
        assetMetasS.val = applyAssetUpdate(assetMetasS.val, message.assetUpdate);
    }

    if (message.assetDirectoriesSnapshot) {
        assetDirectoriesS.val = sortAssets(message.assetDirectoriesSnapshot.items || []);
    }

    if (message.assetDirectoryUpdate?.id) {
        assetDirectoriesS.val = applyAssetUpdate(assetDirectoriesS.val, message.assetDirectoryUpdate);
    }

    if (message.configSnapshot) {
        primaryConfigS.val = message.configSnapshot;
    }

    if (message.authzRuleTemplatesSnapshot) {
        authzTemplatesS.val = message.authzRuleTemplatesSnapshot.items || [];
    }

    if (message.authzGrantsSnapshot) {
        authzGrantsS.val = message.authzGrantsSnapshot.items || [];
    }

    if (message.authzGlobalRulesSnapshot) {
        authzGlobalRulesS.val = message.authzGlobalRulesSnapshot.items || [];
    }
};

const sortByName = (items) => [...items].sort((a, b) => (a.name || '').localeCompare(b.name || ''));
const sortSpaces = (items) => [...items].sort((a, b) => (a.id || 0) - (b.id || 0) || (a.name || '').localeCompare(b.name || ''));
const sortAssets = (items) => [...items].sort((a, b) => (a.key || '').localeCompare(b.key || '') || Number(a.id || 0) - Number(b.id || 0));

const refreshMachinesFromNodes = () => {
    const statusesByNodeId = new Map((nodeStatusesS.val || []).map((status) => [status.nodeId, status]));
    machinesS.val = sortByName(nodesS.val.map((node) => {
        const status = statusesByNodeId.get(node.id) || {};
        return {
            id: node.id,
            name: node.name,
            identifier: node.identifier,
            isPrimary: (node.roles || []).includes(0),
            connected: status.isConnected === true,
            connectedAt: status.lastConnectedAt,
            addresses: node.addresses || [],
            // Spaces whose deployments may be placed here. The server always
            // includes the opendeploy space, so an empty list means the node
            // record has not arrived yet, not that nothing is allowed.
            allowedSpaces: node.allowedSpaces || [],
        };
    }));
};

const applyItemUpdate = (items, update) => {
    const next = new Map((items || []).map((item) => [item.id, item]));
    if (update.deleted) {
        next.delete(update.id);
    } else {
        next.set(update.id, update);
    }
    return sortByName(Array.from(next.values()));
};

// expandValueVersionRefs flattens secret/config metas into one entry per
// version row, the shape reference pickers join deployment env refs against.
// `id` is the version row id (what specs pin); `stableId` is the identity.
export const expandValueVersionRefs = (metas) => (metas || []).flatMap((meta) => (meta.versionRefs || []).map((ref) => ({
    id: ref.id,
    stableId: meta.id,
    name: meta.name,
    spaceId: meta.spaceId,
    version: ref.version,
    value: ref.value,
    createdAt: ref.createdAt,
    createdBy: ref.createdBy,
})));

const applySpaceUpdate = (items, update) => {
    const next = new Map((items || []).map((item) => [item.id, item]));
    if (update.deleted) {
        next.delete(update.id);
    } else {
        next.set(update.id, update);
    }
    return sortSpaces(Array.from(next.values()));
};

const applyAssetUpdate = (items, update) => {
    // Metas are one per asset, keyed by the stable asset id. Matching on key
    // would also hit a same-named asset in another space.
    const next = (items || []).filter((item) => item.id !== update.id);
    if (!update.deleted) next.push(update);
    return sortAssets(next);
};

const scheduleReconnect = (generation, lastError) => {
    if (!hasStateStreamAccess() || generation !== sessionGeneration) return;

    if (streamRetryTimer) {
        clearTimeout(streamRetryTimer);
    }

    reconnectAttempt += 1;
    setStreamState('reconnecting', `Re-connecting (attempt ${reconnectAttempt})`, lastError || '');
    streamRetryTimer = setTimeout(() => {
        streamRetryTimer = null;
        void startDeploymentsStream(generation);
    }, 1000);
};

async function startDeploymentsStream(generation = sessionGeneration) {
    if (!hasStateStreamAccess() || generation !== sessionGeneration) return;

    if (streamRetryTimer) {
        clearTimeout(streamRetryTimer);
        streamRetryTimer = null;
    }

    if (streamAbortController) {
        streamAbortController.abort();
    }

    streamAbortController = new AbortController();
    setStreamState(
        reconnectAttempt > 0 ? 'reconnecting' : 'connecting',
        reconnectAttempt > 0 ? `Re-connecting (attempt ${reconnectAttempt})` : 'Connecting'
    );

    let connected = false;
    try {
        const stream = capi.postV1GlobalStateStream({ signal: streamAbortController.signal });
        for await (const message of stream) {
            if (!connected) {
                connected = true;
                reconnectAttempt = 0;
                setStreamState('connected', 'Connection healthy');
            }
            armInactivityTimer(generation);
            handleStateMessage(message);
        }
        throw new Error('stream closed by server');
    } catch (e) {
        if (e.name === 'AbortError') {
            return;
        }
        console.error('state stream ended:', e.message);
        scheduleReconnect(generation, e.message);
    } finally {
        clearInactivityTimer();
    }
}

van.derive(() => {
    const token = loginS.val?.token || null;

    if (!token || !hasStateStreamAccess()) {
        activeToken = null;
        sessionGeneration += 1;
        stopDeploymentsStream({ clearDeployments: true });
        return;
    }

    if (token === activeToken) {
        return;
    }

    activeToken = token;
    sessionGeneration += 1;
    stopDeploymentsStream();
    void startDeploymentsStream(sessionGeneration);
});
