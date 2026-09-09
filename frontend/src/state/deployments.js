import van from 'vanjs-core';
import {capi} from '../capi/index.js';
import {loginS} from './login.js';
import {createTree, applySnapshot, applyCore, applyBackupStatus, applySecretsStatus, applyIngressDiagnostics, applyAgentSessions} from './tree.js';
import {publishDerived, SEEDED_SPACES} from './derive.js';
export * from './derive.js';

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
const tree = createTree();

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
        publishDerived(tree, applySnapshot(tree, {spaces: SEEDED_SPACES}, {preserveSidecars: false}));
    }
    setStreamState('offline', 'offline');
};

const handleStateMessage = message => {
    if (message?.snapshot) publishDerived(tree, applySnapshot(tree, message.snapshot));
    if (message?.core) publishDerived(tree, applyCore(tree, message.core));
    if (message?.backupStatus) publishDerived(tree, applyBackupStatus(tree, message.backupStatus));
    if (message?.secretsStatus) publishDerived(tree, applySecretsStatus(tree, message.secretsStatus));
    if (message?.ingressDiagnostics) publishDerived(tree, applyIngressDiagnostics(tree, message.ingressDiagnostics));
    if (message?.agentSessions) publishDerived(tree, applyAgentSessions(tree, message.agentSessions));
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
