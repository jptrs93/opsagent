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

function preparerChanged(cur, prev) {
    const a = cur && cur.preparer;
    const b = prev && prev.preparer;
    if (!a && !b) return false;
    if (!a || !b) return true;
    return a.status !== b.status
        || a.deploymentConfigVersion !== b.deploymentConfigVersion
        || a.artifact !== b.artifact;
}

function runnerChanged(cur, prev) {
    const a = cur && cur.runner;
    const b = prev && prev.runner;
    if (!a && !b) return false;
    if (!a || !b) return true;
    const ta = a.lastRestartAt instanceof Date ? a.lastRestartAt.getTime() : 0;
    const tb = b.lastRestartAt instanceof Date ? b.lastRestartAt.getTime() : 0;
    return a.status !== b.status
        || a.deploymentConfigVersion !== b.deploymentConfigVersion
        || a.runningPid !== b.runningPid
        || a.runningArtifact !== b.runningArtifact
        || a.numberOfRestarts !== b.numberOfRestarts
        || ta !== tb;
}

function formatPreparer(p) {
    const label = preparerStatusLabels[p.status] || `preparer=${p.status}`;
    return `prepare: ${label}`;
}

function formatRunner(r) {
    const label = runnerStatusLabels[r.status] || `runner=${r.status}`;
    const extras = [`pid=${r.runningPid || 0}`, `restarts=${r.numberOfRestarts || 0}`];
    if (r.lastRestartAt instanceof Date && r.lastRestartAt.getTime() > 0) {
        extras.push(`last_restart=${format(r.lastRestartAt, "HH:mm:ss")}`);
    }
    return `run: ${label} ${extras.join(' ')}`;
}

function describeStatusEntry(status, prev) {
    const showPreparer = preparerChanged(status, prev);
    const showRunner = runnerChanged(status, prev);
    const parts = [];
    if (showPreparer && status.preparer) parts.push(formatPreparer(status.preparer));
    if (showRunner && status.runner) parts.push(formatRunner(status.runner));
    if (parts.length > 0) return parts.join(', ');
    // No detectable change — fall back to whichever side exists so the row isn't empty.
    if (status.preparer) parts.push(formatPreparer(status.preparer));
    if (status.runner) parts.push(formatRunner(status.runner));
    return parts.length > 0 ? parts.join(', ') : 'status update';
}

export function deploymentHistory(deploymentId, label, onClose) {
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
            h2({class: "text-sm font-semibold text-gray-300"}, `History: ${label || `#${deploymentId}`}`),
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

                // Drop seq_no=0 placeholder status rows inserted by older
                // versions of the primary to satisfy the status-never-nil
                // invariant — they carry no preparer/runner data and render
                // as meaningless "status update" lines.
                const visibleEntries = entries.val.filter(e => !e.status || e.status.statusSeqNo > 0);

                // Build a map of config entries by seqNo for diffing.
                const configEntries = visibleEntries.filter(e => e.config);
                const configByVersion = {};
                const configsSorted = [...configEntries].sort((a, b) => a.config.version - b.config.version);
                let prevConfig = null;
                for (const e of configsSorted) {
                    configByVersion[e.config.version] = { config: e.config, prev: prevConfig };
                    prevConfig = e.config;
                }

                // Entries are newest-first. Walk chronologically (reverse) to
                // record each status entry's prior status for diff rendering.
                const prevStatusByEntry = new Map();
                let lastStatus = null;
                for (let i = visibleEntries.length - 1; i >= 0; i--) {
                    const e = visibleEntries[i];
                    if (e.status) {
                        prevStatusByEntry.set(e, lastStatus);
                        lastStatus = e.status;
                    }
                }

                const entryTime = (e) => {
                    const t = e.config ? e.config.updatedAt : e.status.timestamp;
                    return t instanceof Date ? t.getTime() : 0;
                };
                const stableWindowMs = 10 * 60 * 1000;

                const lines = visibleEntries.map((e, i) => {
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
                        const prev = prevStatusByEntry.get(e);
                        const desc = describeStatusEntry(e.status, prev);
                        const transitionedToRunning = runnerChanged(e.status, prev)
                            && e.status.runner && e.status.runner.status === 2;
                        const nextTs = i > 0 ? entryTime(visibleEntries[i - 1]) : 0;
                        const curTs = entryTime(e);
                        const stable = i === 0 || (nextTs > 0 && curTs > 0 && nextTs - curTs > stableWindowMs);
                        const color = transitionedToRunning && stable ? "text-green-500" : "text-gray-500";
                        return div(
                            {class: `px-3 py-0.5 text-xs font-mono ${color}`},
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
