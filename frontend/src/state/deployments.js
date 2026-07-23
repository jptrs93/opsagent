import van from "vanjs-core";
import { capi } from "../capi/index.js";
import { loginS } from "./login.js";

// deploymentsS holds the current DeploymentWithStatus[] snapshot.
// Each entry has {config: DeploymentConfig2, status: DeploymentStatus}.
export const deploymentsS = van.state([]);
// usersMapS holds a Map<userId, userName> for resolving display names.
export const usersMapS = van.state(new Map());
export const machinesS = van.state([]);
export const nodesS = van.state([]);
export const nodeStatusesS = van.state([]);
export const enrollmentsS = van.state([]);
export const secretRefsS = van.state([]);
export const userConfigRefsS = van.state([]);
export const secretsStatusS = van.state(null);
export const backupStatusS = van.state(null);
export const secretMetasS = van.state([]);
export const userConfigsS = van.state([]);
export const assetMetasS = van.state([]);
export const primaryConfigS = van.state(null);
const SEEDED_SPACES = [{id: 0, name: 'opendeploy'}, {id: 1, name: 'default'}];

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
        deploymentsS.val = [];
        usersMapS.val = new Map();
        machinesS.val = [];
        nodesS.val = [];
        nodeStatusesS.val = [];
        enrollmentsS.val = [];
        secretRefsS.val = [];
        userConfigRefsS.val = [];
        secretsStatusS.val = null;
        backupStatusS.val = null;
        secretMetasS.val = [];
        userConfigsS.val = [];
        assetMetasS.val = [];
        primaryConfigS.val = null;
        spacesS.val = SEEDED_SPACES;
    }
    setStreamState('offline', 'offline');
};

const handleStateMessage = (message) => {
    if (!message) return;

    if (message.deploymentsSnapshot) {
        deploymentsS.val = message.deploymentsSnapshot.items || [];
    }

    if (message.deploymentUpdate?.config?.id) {
        const updateId = message.deploymentUpdate.config.id;
        const next = new Map((deploymentsS.val || []).map((item) => [item.config.id, item]));
        next.set(updateId, message.deploymentUpdate);
        deploymentsS.val = Array.from(next.values());
    }

    if (message.usersSnapshot && message.usersSnapshot.length > 0) {
        const next = new Map();
        for (const u of message.usersSnapshot) {
            next.set(u.id, u.name);
        }
        usersMapS.val = next;
    }

    if (message.userUpdate?.id) {
        const next = new Map(usersMapS.val);
        next.set(message.userUpdate.id, message.userUpdate.name);
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

    if (message.secretsSnapshot) {
        secretRefsS.val = message.secretsSnapshot.items || [];
    }

    if (message.secretUpdate?.id) {
        secretRefsS.val = applyReferenceUpdate(secretRefsS.val, message.secretUpdate);
    }

    if (message.userConfigsSnapshot) {
        userConfigRefsS.val = message.userConfigsSnapshot.items || [];
    }

    if (message.userConfigUpdate?.id) {
        userConfigRefsS.val = applyReferenceUpdate(userConfigRefsS.val, message.userConfigUpdate);
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
    }

    if (message.secretMetaUpdate?.id) {
        secretMetasS.val = applyItemUpdate(secretMetasS.val, message.secretMetaUpdate);
    }

    if (message.userConfigValuesSnapshot) {
        userConfigsS.val = sortByName(message.userConfigValuesSnapshot.items || []);
    }

    if (message.userConfigValueUpdate?.id) {
        userConfigsS.val = applyItemUpdate(userConfigsS.val, message.userConfigValueUpdate);
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

    if (message.configSnapshot) {
        primaryConfigS.val = message.configSnapshot;
    }
};

const sortByName = (items) => [...items].sort((a, b) => (a.name || '').localeCompare(b.name || ''));
const sortSpaces = (items) => [...items].sort((a, b) => (a.id || 0) - (b.id || 0) || (a.name || '').localeCompare(b.name || ''));
const sortAssets = (items) => [...items].sort((a, b) => (a.key || '').localeCompare(b.key || '') || Number(a.version || 0) - Number(b.version || 0));

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

const applyReferenceUpdate = (items, update) => {
    const next = new Map((items || []).map((item) => [item.id, item]));
    if (update.deleted) {
        next.delete(update.id);
    } else {
        next.set(update.id, update);
    }
    return Array.from(next.values()).sort((a, b) => (a.name || '').localeCompare(b.name || ''));
};

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
    if (update.deleted) {
        return sortAssets((items || []).filter((item) => item.key !== update.key));
    }
    const next = (items || []).filter((item) => item.id !== update.id);
    next.push(update);
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
        const stream = capi.postV1StateStream({ signal: streamAbortController.signal });
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
