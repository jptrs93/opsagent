import van from "vanjs-core";
import { capi } from "../capi/index.js";
import { loginS } from "./login.js";

// deploymentsS holds the current DeploymentWithStatus[] snapshot.
// Each entry has {config: DeploymentConfig, status: DeploymentStatus}.
export const deploymentsS = van.state([]);
// usersMapS holds a Map<userId, userName> for resolving display names.
export const usersMapS = van.state(new Map());
export const machinesS = van.state([]);
export const enrollmentsS = van.state([]);
export const secretRefsS = van.state([]);
export const userConfigRefsS = van.state([]);
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
        enrollmentsS.val = [];
        secretRefsS.val = [];
        userConfigRefsS.val = [];
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

    if (message.machinesSnapshot) {
        machinesS.val = message.machinesSnapshot.items || [];
    }

    if (message.machineUpdate?.name) {
        const next = new Map((machinesS.val || []).map((machine) => [machine.name, machine]));
        if (message.machineUpdate.connected) {
            next.set(message.machineUpdate.name, message.machineUpdate);
        } else {
            next.delete(message.machineUpdate.name);
        }
        machinesS.val = Array.from(next.values());
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
