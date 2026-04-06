import van from "vanjs-core";
import { capi } from "../capi/index.js";
import { loginS } from "./login.js";

export const deploymentsS = van.state([]);
export const usersS = van.state([]);
// clusterConfigS holds the ClusterConfig record (user + system config).
// Null before the first snapshot arrives.
export const clusterConfigS = van.state(null);
// desiredStatesS is a plain object keyed by "<machine>:<name>" mapping to
// DesiredState records. Lives in its own store on the backend so that
// deploy clicks don't rewrite the full cluster config.
export const desiredStatesS = van.state({});
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

export const setClusterConfig = (cc) => {
    clusterConfigS.val = cc || null;
};

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
        usersS.val = [];
        clusterConfigS.val = null;
        desiredStatesS.val = {};
    }
    setStreamState('offline', 'offline');
};

const handleStateMessage = (message) => {
    if (!message) return;

    if (message.deploymentsSnapshot) {
        deploymentsS.val = message.deploymentsSnapshot.deployments || [];
    }

    if (message.deploymentUpdate?.key) {
        const next = new Map((deploymentsS.val || []).map((item) => [item.key, item]));
        next.set(message.deploymentUpdate.key, message.deploymentUpdate);
        deploymentsS.val = Array.from(next.values());
    }

    if (message.usersSnapshot) {
        usersS.val = message.usersSnapshot.users || [];
    }

    if (message.userUpdate?.id) {
        const next = new Map((usersS.val || []).map((item) => [item.id, item]));
        next.set(message.userUpdate.id, message.userUpdate);
        usersS.val = Array.from(next.values()).sort((a, b) => a.id - b.id);
    }

    if (message.clusterConfigSnapshot !== undefined) {
        setClusterConfig(message.clusterConfigSnapshot);
    }

    if (message.clusterConfigUpdate !== undefined) {
        setClusterConfig(message.clusterConfigUpdate);
    }

    if (message.desiredStatesSnapshot) {
        const next = {};
        for (const ds of (message.desiredStatesSnapshot.desiredStates || [])) {
            if (ds?.key) next[ds.key] = ds;
        }
        desiredStatesS.val = next;
    }

    if (message.desiredStateUpdate?.key) {
        desiredStatesS.val = {...desiredStatesS.val, [message.desiredStateUpdate.key]: message.desiredStateUpdate};
    }
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
