import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {formatClockTime, formatHistoryTime} from "../lib/date.js";
import {resolveUserDisplayName} from "../lib/users.js";
import {deploymentWorkload} from "../lib/deployment.js";
import {rollupLabel, rollupOf, inputsLabel, imageLabel, InputsStatus, ImageStatus} from "../lib/preparerStatus.js";

const {button, col, colgroup, div, input, label, p, span, table, tbody, td, th, thead, tr} = van.tags;

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
        if (config.spaceId !== prevConfig.spaceId) {
            parts.push(`moved to space ${config.spaceId}`);
        }
        if (config.deleted && !prevConfig.deleted) {
            parts.push('deleted');
        }
        if (!config.deleted && prevConfig.deleted) {
            parts.push('restored');
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
        || a.deploymentSpecVersion !== b.deploymentSpecVersion
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
        || a.deploymentSpecVersion !== b.deploymentSpecVersion
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

// History shows only config rows by default; status rows are opt-in. Module
// state so the toggle survives switching deployments and reopening the
// inspector.
const showStatusRows = van.state(false);

const miniTh = (text, extra = "") => th(
    {class: `border-b border-gray-700/70 border-r border-r-gray-800/40 last:border-r-0 bg-gray-950/40 px-2 py-1 text-left text-[10px] font-medium uppercase tracking-wide text-gray-500 ${extra}`},
    text);
const miniTd = (extra, ...children) => td(
    {class: `border-b border-gray-800/50 border-r border-r-gray-800/30 last:border-r-0 px-2 py-[3px] whitespace-nowrap overflow-hidden ${extra}`},
    ...children);

// deploymentHistoryPanel renders one deployment's audit trail as the inspector
// History tab: a dense Time | Type | V | By | Change table with an inline
// revert link on non-latest config rows.
export function deploymentHistoryPanel(deploymentId, onRevertTargetVersion = () => {}) {
    const entries = van.state(null);
    const error = van.state('');

    const load = async () => {
        try {
            const decoded = await capi.postV1DeploymentsHistory({ deploymentId });
            entries.val = decoded?.entries || [];
        } catch (e) {
            console.error('Failed to load deployment history:', e);
            error.val = 'Connection error';
            entries.val = [];
        }
    };

    setTimeout(load, 0);

    const buildRows = () => {
        // Drop placeholder status rows with a zero updated_at clock (inserted
        // to satisfy the status-never-nil invariant — they carry no
        // preparer/runner data and render as meaningless lines).
        const visibleEntries = entries.val.filter(e => !e.status || tsMs(e.status.updatedAt) > 0);
        const configEntries = visibleEntries.filter(e => e.config);
        const configsSorted = [...configEntries].sort((a, b) => a.config.version - b.config.version);
        const currentConfigVersion = configsSorted.length > 0
            ? configsSorted[configsSorted.length - 1].config.version
            : 0;
        const prevByVersion = {};
        let prevConfig = null;
        for (const e of configsSorted) {
            prevByVersion[e.config.version] = prevConfig;
            prevConfig = e.config;
        }

        // Entries are newest-first. Walk chronologically (reverse) to record
        // each status entry's prior status for diff rendering.
        const prevStatusByEntry = new Map();
        let lastStatus = null;
        for (let i = visibleEntries.length - 1; i >= 0; i--) {
            const e = visibleEntries[i];
            if (e.status) {
                prevStatusByEntry.set(e, lastStatus);
                lastStatus = e.status;
            }
        }

        return visibleEntries.map((e) => {
            if (e.config) {
                const targetVersion = deploymentWorkload(e.config)?.version || '';
                return {
                    at: e.config.updatedAt,
                    kind: 'config',
                    v: e.config.version,
                    by: resolveUserDisplayName(e.config.author) || '',
                    change: describeConfigEntry(e.config, prevByVersion[e.config.version]),
                    config: e.config,
                    canRevert: Boolean(targetVersion) && e.config.version !== currentConfigVersion,
                };
            }
            return {
                at: e.status.updatedAt,
                kind: 'status',
                v: null,
                by: '',
                change: describeStatusEntry(e.status, prevStatusByEntry.get(e)),
                config: null,
                canRevert: false,
            };
        });
    };

    const historyTable = () => {
        if (entries.val === null) {
            return p({class: "p-4 text-sm text-gray-500"}, "Loading...");
        }
        const allRows = buildRows();
        const rows = showStatusRows.val ? allRows : allRows.filter((r) => r.kind === 'config');
        if (rows.length === 0) {
            return p({class: "p-4 text-sm text-gray-500"}, showStatusRows.val ? "No history." : "No config changes.");
        }
        return table(
            {class: "w-full table-fixed border-collapse text-xs"},
            colgroup(
                col({style: "width:7.2rem"}),
                col({style: "width:3.2rem"}),
                col({style: "width:5.4rem"}),
                col({style: "width:3rem"}),
                col(),
            ),
            thead(tr(miniTh("Time"), miniTh("Type"), miniTh("V"), miniTh("By"), miniTh("Change"))),
            tbody(...rows.map((entry) => tr(
                {class: "hover:bg-gray-700/35"},
                miniTd("font-mono text-gray-500 tabular-nums", formatHistoryTime(entry.at)),
                miniTd(entry.kind === 'config' ? "text-orange-300" : "text-gray-500", entry.kind),
                miniTd("font-mono text-gray-500 tabular-nums",
                    entry.v === null ? "" : span(`v${entry.v}`),
                    entry.canRevert
                        ? span(" ",
                            button({
                                type: "button",
                                title: "Revert target version to this config's version",
                                class: "p-0 text-[11px] text-blue-400 underline hover:text-blue-300 cursor-pointer",
                                onclick: () => onRevertTargetVersion(deploymentId, entry.config),
                            }, "revert"))
                        : ""),
                miniTd("truncate text-gray-500", entry.by),
                miniTd(`font-mono truncate ${entry.kind === 'config' ? 'text-orange-300' : 'text-gray-400'}`,
                    span({title: entry.change}, entry.change)),
            ))),
        );
    };

    const countLine = () => {
        if (entries.val === null) return '';
        const allRows = buildRows();
        const rows = showStatusRows.val ? allRows : allRows.filter((r) => r.kind === 'config');
        return `${rows.length} entries`;
    };

    return div(
        {class: "flex min-h-0 flex-1 flex-col"},
        div({class: "flex flex-none items-center justify-between px-3 py-1.5"},
            span({class: "text-[11px] text-gray-500"},
                () => error.val ? span({class: "text-red-400"}, error.val) : countLine()),
            label({class: "flex cursor-pointer items-center gap-1.5 text-[11px] text-gray-400 select-none"},
                input({
                    type: "checkbox",
                    class: "accent-blue-500",
                    checked: showStatusRows,
                    onchange: (e) => { showStatusRows.val = e.target.checked; },
                }),
                "Status rows")),
        div({class: "app-scroll flex-1 min-h-0 overflow-auto"}, historyTable),
    );
}
