import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {format} from "date-fns";
import {decodeDeploymentHistory} from "../capi/model.js";
import {usersMapS} from "../state/deployments.js";

const { div, h2, span, button, p } = van.tags;

const preparerStatusLabels = {
    0: 'unknown',
    2: 'preparing',
    3: 'downloading',
    4: 'ready',
    5: 'failed',
};

const runnerStatusLabels = {
    0: 'unknown',
    1: 'no deployment',
    2: 'running',
    3: 'stopped',
    4: 'starting',
    5: 'crashed',
};

function describeConfigEntry(config, prevConfig) {
    const parts = [];
    const desired = config.desiredState || {};

    if (!prevConfig) {
        parts.push('created');
    } else {
        const prevDesired = prevConfig.desiredState || {};
        if (desired.version !== prevDesired.version && desired.version) {
            parts.push(`version=${desired.version.substring(0, 7)}`);
        }
        if (desired.running !== prevDesired.running) {
            parts.push(desired.running ? 'running=true' : 'running=false');
        }
        if (config.deleted && !prevConfig.deleted) {
            parts.push('deleted');
        }
    }

    return parts.length > 0 ? parts.join(', ') : 'config update';
}

function describeStatusEntry(status) {
    const parts = [];
    if (status.preparer) {
        const label = preparerStatusLabels[status.preparer.status] || `preparer=${status.preparer.status}`;
        parts.push(`prepare: ${label}`);
    }
    if (status.runner) {
        const label = runnerStatusLabels[status.runner.status] || `runner=${status.runner.status}`;
        parts.push(`run: ${label}`);
    }
    return parts.length > 0 ? parts.join(', ') : 'status update';
}

function resolveUserName(userId) {
    if (!userId) return null;
    return usersMapS.val.get(userId) || 'unknown';
}

function parseKey(key) {
    const parts = key.split(':');
    if (parts.length >= 3) {
        return { environment: parts[0], machine: parts[1], deploymentName: parts.slice(2).join(':') };
    }
    const idx = key.indexOf(':');
    if (idx === -1) {
        return { environment: '', machine: '', deploymentName: key };
    }
    return {
        environment: '',
        machine: key.slice(0, idx),
        deploymentName: key.slice(idx + 1),
    };
}

export function deploymentHistory(key, onClose) {
    const entries = van.state(null);
    const error = van.state('');
    const { environment, machine, deploymentName } = parseKey(key);

    const load = async () => {
        try {
            const headers = capi.headerProvider() || {};
            headers['Accept'] = 'application/x-protobuf';
            let url = `/v1/deployment/history?deployment=${encodeURIComponent(deploymentName)}`;
            if (machine) {
                url += `&machine=${encodeURIComponent(machine)}`;
            }
            if (environment) {
                url += `&environment=${encodeURIComponent(environment)}`;
            }

            const response = await fetch(url, {
                headers,
                credentials: 'include',
            });
            if (!response.ok) throw new Error(`HTTP ${response.status}`);
            const decoded = decodeDeploymentHistory(await response.arrayBuffer());
            entries.val = decoded?.entries || [];
        } catch (e) {
            console.error('Failed to load deployment history:', e);
            error.val = 'Connection error';
            entries.val = [];
        }
    };

    setTimeout(load, 0);

    return div(
        {class: "w-1/2 min-h-0 border-l border-gray-700 bg-gray-900 flex flex-col h-full"},
        div(
            {class: "flex items-center justify-between p-3 border-b border-gray-700"},
            h2({class: "text-sm font-semibold text-gray-300"}, `History: ${deploymentName}`),
            div(
                {class: "flex items-center gap-2"},
                () => error.val ? span({class: "text-xs text-red-400"}, error.val) : span(),
                button({
                    class: "text-gray-400 hover:text-gray-200 text-sm px-2",
                    onclick: onClose,
                }, "Close"),
            ),
        ),
        div(
            {class: "flex-1 min-h-0 overflow-auto p-2"},
            () => {
                if (entries.val === null) {
                    return p({class: "p-4 text-sm text-gray-500"}, "Loading...");
                }
                if (entries.val.length === 0) {
                    return p({class: "p-4 text-sm text-gray-500"}, "No history.");
                }

                // Build a map of config entries by seqNo for diffing.
                const configEntries = entries.val.filter(e => e.config);
                const configBySeq = {};
                // Walk oldest-first to map prev configs correctly.
                const configsSorted = [...configEntries].sort((a, b) => a.config.seqNo - b.config.seqNo);
                let prevConfig = null;
                for (const e of configsSorted) {
                    configBySeq[e.config.seqNo] = { config: e.config, prev: prevConfig };
                    prevConfig = e.config;
                }

                // entries are already time-desc from backend.
                const lines = entries.val.map((e) => {
                    const isConfig = !!e.config;
                    const ts = isConfig
                        ? (e.config.updatedAt instanceof Date && e.config.updatedAt.getTime() > 0
                            ? format(e.config.updatedAt, "MMM d HH:mm:ss")
                            : '')
                        : (e.status.timestamp instanceof Date && e.status.timestamp.getTime() > 0
                            ? format(e.status.timestamp, "MMM d HH:mm:ss")
                            : '');

                    if (isConfig) {
                        const info = configBySeq[e.config.seqNo];
                        const desc = describeConfigEntry(e.config, info?.prev);
                        const userName = resolveUserName(e.config.updatedBy);
                        const user = userName ? ` [${userName}]` : '';
                        return div(
                            {class: "px-3 py-0.5 text-xs font-mono text-orange-400"},
                            span(ts),
                            span("  "),
                            span(`seq=${e.config.seqNo} `),
                            span(desc),
                            user ? span({class: "text-orange-300"}, user) : null,
                        );
                    } else {
                        const desc = describeStatusEntry(e.status);
                        return div(
                            {class: "px-3 py-0.5 text-xs font-mono text-gray-500"},
                            span(ts),
                            span("  "),
                            span(desc),
                        );
                    }
                }).filter(Boolean);

                if (lines.length === 0) {
                    return p({class: "p-4 text-sm text-gray-500"}, "No history.");
                }

                return div({class: "flex flex-col"}, ...lines);
            }
        )
    );
}
