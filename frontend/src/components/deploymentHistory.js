import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {formatClockTime, formatHistoryTime} from "../lib/date.js";
import {resolveUserDisplayName} from "../lib/users.js";
import {deploymentWorkload} from "../lib/deploymentConfig.js";
import {rollupLabel, rollupOf, inputsLabel, imageLabel, InputsStatus, ImageStatus} from "../lib/preparerStatus.js";

const { div, h2, span, button, p } = van.tags;

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
    const desired = deploymentWorkload(config) || {};

    if (!prevConfig) {
        parts.push('created');
    } else {
        const prevDesired = deploymentWorkload(prevConfig) || {};
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
    return a.inputs !== b.inputs
        || a.image !== b.image
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

// History is a per-transition audit trail, so it spells out both stages rather
// than reducing them to one phrase the way the status table does. Stages are
// omitted when unset, which is how rows written before the split read.
function formatPreparer(preparer) {
    const parts = [`prepare: ${rollupLabel(rollupOf(preparer))}`];
    if ((preparer.inputs || 0) !== InputsStatus.UNKNOWN) parts.push(`inputs=${inputsLabel(preparer.inputs)}`);
    if ((preparer.image || 0) !== ImageStatus.UNKNOWN) parts.push(`image=${imageLabel(preparer.image)}`);
    return parts.join(' ');
}

function formatRunner(r) {
    const label = runnerStatusLabels[r.status] || `runner=${r.status}`;
    const extras = [`pid=${r.runningPid || 0}`, `restarts=${r.numberOfRestarts || 0}`];
    if (r.lastRestartAt instanceof Date && r.lastRestartAt.getTime() > 0) {
        extras.push(`last_restart=${formatClockTime(r.lastRestartAt)}`);
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

const tsMs = (t) => (t instanceof Date ? t.getTime() : 0);

export function deploymentHistory(deploymentId, label, onClose, onRevertTargetVersion = () => {}) {
    const entries = van.state(null);
    const error = van.state('');
    const showStatusUpdates = van.state(true);

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
                    class: () => `flex items-center gap-2 rounded-full border px-2.5 py-1 text-xs transition-colors cursor-pointer ${showStatusUpdates.val ? 'border-brand bg-brand/20 text-blue-200' : 'border-gray-600 bg-gray-800 text-gray-400'}`,
                    onclick: () => { showStatusUpdates.val = !showStatusUpdates.val; },
                    type: "button",
                    "aria-pressed": () => showStatusUpdates.val ? "true" : "false",
                    title: "Toggle status update history lines",
                },
                    span({class: () => `h-3.5 w-6 rounded-full relative transition-colors ${showStatusUpdates.val ? 'bg-brand' : 'bg-gray-600'}`},
                        span({class: () => `absolute top-0.5 h-2.5 w-2.5 rounded-full bg-white transition-all ${showStatusUpdates.val ? 'left-3' : 'left-0.5'}`}),
                    ),
                    span("Show status updates"),
                ),
                button({
                    class: "text-gray-400 hover:text-gray-200 text-sm px-2",
                    onclick: onClose,
                }, "Close"),
            ),
        ),
        div(
            {class: "app-scroll flex-1 min-h-0 overflow-auto p-2"},
            () => {
                if (entries.val === null) {
                    return p({class: "p-4 text-sm text-gray-500"}, "Loading...");
                }
                if (entries.val.length === 0) {
                    return p({class: "p-4 text-sm text-gray-500"}, "No history.");
                }

                // Drop placeholder status rows with a zero updated_at clock
                // (inserted to satisfy the status-never-nil invariant — they
                // carry no preparer/runner data and render as meaningless
                // "status update" lines).
                const visibleEntries = entries.val.filter(e => !e.status || tsMs(e.status.updatedAt) > 0);
                const renderEntries = showStatusUpdates.val
                    ? visibleEntries
                    : visibleEntries.filter(e => e.config);

                // Build a map of config entries by version for diffing.
                const configEntries = visibleEntries.filter(e => e.config);
                const configByVersion = {};
                const configsSorted = [...configEntries].sort((a, b) => a.config.version - b.config.version);
                const currentConfigVersion = configsSorted.length > 0
                    ? configsSorted[configsSorted.length - 1].config.version
                    : 0;
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
                    const t = e.config ? e.config.updatedAt : e.status.updatedAt;
                    return tsMs(t);
                };
                const stableWindowMs = 10 * 60 * 1000;

                const lines = renderEntries.map((e, i) => {
                    const isConfig = !!e.config;
                    const ts = entryTime(e) > 0
                        ? formatHistoryTime(isConfig ? e.config.updatedAt : e.status.updatedAt)
                        : '';

                    if (isConfig) {
                        const info = configByVersion[e.config.version];
                        const desc = describeConfigEntry(e.config, info?.prev);
                        const userName = resolveUserDisplayName(e.config.updatedBy);
                        const user = userName ? ` [${userName}]` : '';
                        const targetVersion = deploymentWorkload(e.config)?.version || '';
                        const canRevertTargetVersion = targetVersion && e.config.version !== currentConfigVersion;
                        return div(
                            {class: "px-3 py-0.5 text-xs font-mono text-orange-400"},
                            span(ts),
                            span("  "),
                            span(`v${e.config.version} `),
                            span(desc),
                            user ? span({class: "text-orange-300"}, user) : '',
                            canRevertTargetVersion ? button({
                                type: "button",
                                class: "ml-2 p-0 text-xs font-mono text-blue-400 underline hover:text-blue-300 cursor-pointer",
                                onclick: () => onRevertTargetVersion(deploymentId, e.config),
                            }, "revert to this version") : '',
                        );
                    } else {
                        const prev = prevStatusByEntry.get(e);
                        const desc = describeStatusEntry(e.status, prev);
                        const transitionedToRunning = runnerChanged(e.status, prev)
                            && e.status.runner && e.status.runner.status === 2;
                        const nextTs = i > 0 ? entryTime(renderEntries[i - 1]) : 0;
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
                    return p({class: "p-4 text-sm text-gray-500"}, showStatusUpdates.val ? "No history." : "No config changes.");
                }

                return div({class: "flex flex-col"}, ...lines);
            }
        )
    );
}
