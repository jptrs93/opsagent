import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {formatClockTime, formatHistoryTime} from "../lib/date.js";
import {resolveUserDisplayName} from "../lib/users.js";
import {deploymentDeleted, deploymentRestartEvent, deploymentWorkload, desiredRunning} from "../lib/deployment.js";
import {rollupLabel, rollupOf, inputsLabel, imageLabel, InputsStatus, ImageStatus} from "../lib/preparerStatus.js";
import {crashReasonLabel} from "../lib/crashReason.js";
import {fitColumns} from "./columnGrip.js";

const {button, div, input, label, p, span, table, tbody, td, th, thead, tr} = van.tags;

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
        if (desiredRunning(config) !== desiredRunning(prevConfig)) {
            parts.push(desiredRunning(config) ? 'running=true' : 'running=false');
        }
        if (config.deployment?.spaceId !== prevConfig.deployment?.spaceId) {
            parts.push(`moved to space ${config.deployment?.spaceId}`);
        }
        if (deploymentDeleted(config) && !deploymentDeleted(prevConfig)) {
            parts.push('deleted');
        }
        if (!deploymentDeleted(config) && deploymentDeleted(prevConfig)) {
            parts.push('restored');
        }
        if (parts.length === 0 && deploymentRestartEvent(config, prevConfig)) {
            parts.push('restarted');
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
        || (a.crashReason || 0) !== (b.crashReason || 0)
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
    if (crashReasonLabel(r.crashReason)) extras.push(`reason="${crashReasonLabel(r.crashReason)}"`);
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

const COLUMNS = [
    {key: "time", label: "Time"},
    {key: "type", label: "Type"},
    {key: "version", label: "V"},
    {key: "by", label: "By"},
    {key: "change", label: "Change", flex: true},
];

const columns = fitColumns({storageKey: "opsagent_history_col_widths", prefix: "hist", columns: COLUMNS});

const miniTh = (column) => th(
    {class: `dep-mini-th relative px-1.5 ${columns.cellClass(column.key)}`, style: columns.cellStyle(column.key)},
    span({class: "block truncate"}, column.label),
    columns.grip(column.key));

const miniTd = (key, extra, ...children) => td(
    {class: `dep-mini-td truncate px-1.5 ${columns.cellClass(key)} ${extra}`, style: columns.cellStyle(key), "data-col": key},
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
            entries.val = (decoded?.entries || []).map(e => ({config: e.value?.deployment || null, status: e.value?.status}));
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
        const versionOf = config => Number(config?.meta?.version || 0);
        const configsSorted = [...configEntries].sort((a, b) => versionOf(a.config) - versionOf(b.config));
        const currentConfigVersion = configsSorted.length > 0
            ? versionOf(configsSorted[configsSorted.length - 1].config)
            : 0;
        const prevByVersion = {};
        let prevConfig = null;
        for (const e of configsSorted) {
            prevByVersion[versionOf(e.config)] = prevConfig;
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
                const meta = e.config.meta || {};
                return {
                    at: new Date(Number(meta.updatedTime || 0)),
                    kind: 'config',
                    v: versionOf(e.config),
                    by: resolveUserDisplayName(Number(meta.updatedActor || 0)) || '',
                    change: describeConfigEntry(e.config, prevByVersion[versionOf(e.config)]),
                    config: e.config,
                    canRevert: Boolean(targetVersion) && versionOf(e.config) !== currentConfigVersion,
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
            {class: "w-full table-auto border-collapse text-xs", style: columns.tableStyle, "data-testid": "deployment-history-table"},
            thead(tr(...COLUMNS.map(miniTh))),
            tbody(...rows.map((entry) => tr(
                {class: "dep-mini-row"},
                miniTd("time", "font-mono text-gray-500 tabular-nums", formatHistoryTime(entry.at)),
                miniTd("type", entry.kind === 'config' ? "text-orange-300 graphite:text-gray-300" : "text-gray-500", entry.kind),
                miniTd("version", "font-mono text-gray-500 tabular-nums",
                    entry.v === null ? "" : span(`v${entry.v}`),
                    entry.canRevert
                        ? span(" ",
                            button({
                                type: "button",
                                title: "Revert target version to this config's version",
                                class: "p-0 text-[11px] text-blue-400 underline slate:no-underline slate:hover:underline graphite:no-underline graphite:hover:underline hover:text-blue-300 cursor-pointer",
                                onclick: () => onRevertTargetVersion(deploymentId, entry.config),
                            }, "revert"))
                        : ""),
                miniTd("by", "text-gray-500", span({title: entry.by}, entry.by)),
                miniTd("change", `font-mono ${entry.kind === 'config' ? 'text-orange-300 graphite:text-gray-200' : 'text-gray-400'}`,
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
        div({class: "flex flex-none items-center justify-between px-3 py-1.5 slate:py-2 graphite:py-2"},
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
