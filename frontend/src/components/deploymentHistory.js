import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {format} from "date-fns";
import {resolveUserDisplayName} from "../lib/users.js";

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

export function deploymentHistory(deploymentId, onClose) {
    const entries = van.state(null);
    const error = van.state('');

    const load = async () => {
        try {
            const decoded = await capi.postV1DeploymentHistory({ deploymentId });
            entries.val = decoded?.entries || [];
        } catch (e) {
            console.error('Failed to load deployment history:', e);
            error.val = 'Connection error';
            entries.val = [];
        }
    };

    setTimeout(load, 0);

    return div(
        {class: "min-h-0 bg-gray-900 flex flex-col h-full"},
        div(
            {class: "flex items-center justify-between p-3 border-b border-gray-700"},
            h2({class: "text-sm font-semibold text-gray-300"}, `History: #${deploymentId}`),
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
                const configByVersion = {};
                const configsSorted = [...configEntries].sort((a, b) => a.config.version - b.config.version);
                let prevConfig = null;
                for (const e of configsSorted) {
                    configByVersion[e.config.version] = { config: e.config, prev: prevConfig };
                    prevConfig = e.config;
                }

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
                        const info = configByVersion[e.config.version];
                        const desc = describeConfigEntry(e.config, info?.prev);
                        const userName = resolveUserDisplayName(e.config.updatedBy);
                        const user = userName ? ` [${userName}]` : '';
                        return div(
                            {class: "px-3 py-0.5 text-xs font-mono text-orange-400"},
                            span(ts),
                            span("  "),
                            span(`v${e.config.version} `),
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
