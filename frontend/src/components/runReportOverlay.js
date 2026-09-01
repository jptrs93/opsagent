import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {deploymentsS, machinesS} from "../state/deployments.js";
import {nodeDisplayName} from "../lib/machines.js";
import {resolveUserDisplayName} from "../lib/users.js";
import {formatHistoryTime} from "../lib/date.js";
import {caretRightIcon} from "../lib/icons.js";

const {div, h2, span, pre, button, p} = van.tags;

const STATUS_RUNNING = 2;
const STATUS_STOPPED = 3;
const STATUS_STARTING = 4;
const STATUS_CRASHED = 5;

const STATUS_NAMES = {[STATUS_RUNNING]: 'Running', [STATUS_STOPPED]: 'Stopped', [STATUS_STARTING]: 'Starting', [STATUS_CRASHED]: 'Crashed'};

const formatDuration = (started, stopped) => {
    if (!(started instanceof Date) || started.getTime() <= 0) return '';
    if (!(stopped instanceof Date) || stopped.getTime() <= 0) return '';
    let secs = Math.max(0, Math.round((stopped.getTime() - started.getTime()) / 1000));
    const parts = [];
    if (secs >= 3600) { parts.push(`${Math.floor(secs / 3600)}h`); secs %= 3600; }
    if (secs >= 60) { parts.push(`${Math.floor(secs / 60)}m`); secs %= 60; }
    parts.push(`${secs}s`);
    return parts.join(' ');
};

export function runReportOverlay(target, onClose) {
    const {deploymentId, version} = target;
    const expanded = van.state(new Set(target.preselect ? [target.preselect.instanceId] : []));
    const selected = van.state(target.preselect || null);
    const report = van.state(null);
    const error = van.state('');
    let loadSeq = 0;

    const deployment = () => (Array.isArray(deploymentsS.val) ? deploymentsS.val : [])
        .find((d) => d.config?.id === deploymentId) || null;

    const versionMeta = van.state(null);
    const resolveVersionMeta = async () => {
        const cfg = deployment()?.config;
        if (cfg?.specVersion === version) {
            versionMeta.val = {at: cfg.eventTime, by: cfg.author || 0};
            return;
        }
        try {
            const resp = await capi.postV1DeploymentsHistory({deploymentId});
            const entry = (resp.entries || []).find((e) => e.config?.specVersion === version);
            if (entry) versionMeta.val = {at: entry.config.eventTime, by: entry.config.author || 0};
        } catch {}
    };
    void resolveVersionMeta();

    const instances = () => {
        const d = deployment();
        return (d?.scheduledInstances || [])
            .filter((s) => (s.instance?.deploymentSpecVersion || 0) === version && (s.instance?.id || 0) > 0)
            .map((s) => ({
                id: s.instance.id,
                ordinal: s.instance.instanceOrdinal || 0,
                node: nodeDisplayName(Number(s.instance.nodeId || 0), machinesS.val),
                runner: s.status?.runner || null,
            }))
            .sort((a, b) => b.id - a.id);
    };

    const fetchReport = async (sel) => {
        const seq = ++loadSeq;
        report.val = null;
        error.val = '';
        try {
            const resp = await capi.postV1DeploymentsRunReport({scheduledInstanceId: sel.instanceId, run: sel.run});
            if (seq === loadSeq) report.val = resp;
        } catch (e) {
            if (seq === loadSeq) error.val = e.message || 'Request failed';
        }
    };

    const selectRun = (instanceId, run) => {
        selected.val = {instanceId, run};
        void fetchReport(selected.val);
    };

    if (target.preselect) void fetchReport(target.preselect);

    const toggleExpanded = (instanceId) => {
        const next = new Set(expanded.val);
        next.has(instanceId) ? next.delete(instanceId) : next.add(instanceId);
        expanded.val = next;
    };

    const runRow = (inst, run, isCurrent, inProgress) => button({
        type: "button",
        class: () => "flex w-full cursor-pointer items-center gap-1.5 py-0.5 pr-2 pl-7 text-left text-[11px] " +
            (selected.val?.instanceId === inst.id && selected.val?.run === run
                ? "bg-brand/20 text-gray-100"
                : "text-gray-400 hover:bg-gray-800/40 hover:text-gray-200"),
        "data-testid": `run-report-run-${inst.id}-${run}`,
        onclick: () => selectRun(inst.id, run),
    },
        span({class: "font-mono"}, `run ${run}`),
        isCurrent && inProgress
            ? span({class: "inline-block h-1.5 w-1.5 flex-none rounded-full bg-green-500"})
            : '');

    const instanceEntry = (inst) => {
        const runCount = inst.runner ? (inst.runner.numberOfRestarts || 0) + 1 : 0;
        const inProgress = inst.runner?.status === STATUS_RUNNING || inst.runner?.status === STATUS_STARTING;
        const isOpen = expanded.val.has(inst.id);
        return div(
            div({
                class: "flex cursor-pointer items-center gap-1 px-2 py-1 hover:bg-gray-800/40",
                "data-testid": `run-report-instance-${inst.id}`,
                onclick: () => toggleExpanded(inst.id),
            },
                caretRightIcon({class: `h-3 w-3 flex-none text-gray-600 transition-transform ${isOpen ? 'rotate-90' : ''}`}),
                span({class: "font-mono text-[11px] text-gray-300"}, `#${inst.id}`),
                span({class: "min-w-0 flex-1 truncate text-[11px] text-gray-500"}, inst.node || '-'),
                span({class: "text-[10px] tabular-nums text-gray-600"}, String(runCount))),
            isOpen && runCount > 0
                ? div(...Array.from({length: runCount}, (_, i) => runCount - i)
                    .map((run) => runRow(inst, run, run === runCount, inProgress)))
                : '',
            isOpen && runCount === 0
                ? p({class: "py-0.5 pr-2 pl-7 text-[11px] text-gray-600"}, "no runs recorded")
                : '',
        );
    };

    const sidebar = () => {
        const list = instances();
        return div({class: "app-scroll w-56 flex-none overflow-y-auto border-r border-gray-800 bg-gray-950/40 py-1"},
            list.length === 0
                ? p({class: "px-3 py-4 text-xs text-gray-500"}, `No instances recorded for v${version}.`)
                : list.map(instanceEntry));
    };

    const exitChip = (r) => {
        if (r.running) {
            return span({class: "inline-flex items-center gap-1.5 rounded-full bg-green-500/10 px-2 py-0.5 text-[11px] text-green-300"},
                span({class: "inline-block h-1.5 w-1.5 rounded-full bg-green-500"}), STATUS_NAMES[r.status] || 'Running');
        }
        if (r.exitCode === undefined || r.exitCode === null) {
            return span({class: "rounded-full bg-gray-700/40 px-2 py-0.5 font-mono text-[11px] text-gray-400"}, "exit unknown");
        }
        return span({class: `rounded-full px-2 py-0.5 font-mono text-[11px] ${r.exitCode === 0 ? 'bg-green-500/10 text-green-300' : 'bg-red-500/10 text-red-300'}`},
            `exit ${r.exitCode}`);
    };

    const fact = (label, ...value) => div({class: "flex min-w-0 items-baseline gap-1.5"},
        span({class: "text-[10px] font-semibold uppercase tracking-wide text-gray-500"}, label),
        span({class: "truncate font-mono text-xs text-gray-300"}, ...value));

    const reportView = (r) => div({class: "flex min-h-0 flex-1 flex-col"},
        div({class: "flex flex-none flex-wrap items-center gap-x-5 gap-y-1.5 border-b border-gray-800 px-4 py-2.5"},
            fact("Node", nodeDisplayName(r.nodeId, machinesS.val) || String(r.nodeId || '-')),
            fact("Instance", `#${selected.val?.instanceId ?? '-'} · ord ${r.instanceOrdinal}`),
            fact("Run", String(r.run)),
            fact("Started", formatHistoryTime(r.startedAt) || '-'),
            !r.running && (r.status === STATUS_STOPPED || r.status === STATUS_CRASHED) && formatHistoryTime(r.stoppedAt)
                ? fact(STATUS_NAMES[r.status], formatHistoryTime(r.stoppedAt))
                : '',
            formatDuration(r.startedAt, r.stoppedAt) ? fact("Duration", formatDuration(r.startedAt, r.stoppedAt)) : '',
            exitChip(r)),
        (r.warnings || []).length > 0
            ? div({class: "flex-none border-b border-gray-800 px-4 py-1.5"},
                ...(r.warnings || []).map((w) => p({class: "text-[11px] text-amber-400"}, w)))
            : '',
        r.running
            ? p({class: "px-4 py-6 text-sm text-gray-500"}, "Run is still in progress — log tail only shown for ended deployments.")
            : (r.logLines || []).length > 0
                ? div({class: "flex min-h-0 flex-1 flex-col"},
                    p({class: "flex-none px-4 pt-2 pb-1 text-[10px] font-semibold uppercase tracking-wide text-gray-500"},
                        `Last ${(r.logLines || []).length} log lines`),
                    pre({
                        "data-testid": "run-report-logs",
                        class: "app-scroll min-h-0 flex-1 overflow-auto bg-gray-950 px-4 py-2 font-mono text-[11px] leading-5 whitespace-pre-wrap break-all text-gray-200",
                    }, r.logLines.join('\n')))
                : p({class: "px-4 py-6 text-sm text-gray-500"}, "No log lines found for this run."),
    );

    const body = () => {
        if (!selected.val) return p({class: "px-4 py-6 text-sm text-gray-500"}, "Select a run to view its report.");
        if (error.val) return p({class: "px-4 py-6 text-sm text-red-400"}, error.val);
        if (report.val === null) return p({class: "px-4 py-6 text-sm text-gray-400"}, "Loading...");
        return reportView(report.val);
    };

    const headerMeta = () => {
        const meta = versionMeta.val;
        if (!meta) return '';
        const at = formatHistoryTime(meta.at);
        const by = resolveUserDisplayName(meta.by) || 'unknown';
        return span({class: "text-xs text-gray-500"}, `${at ? at + ' · ' : ''}${by}`);
    };

    const name = () => deployment()?.config?.def?.name || `#${deploymentId}`;

    return div(
        div({class: "fixed inset-0 bg-black/70 z-40", onclick: onClose}),
        div(
            {class: "fixed inset-0 z-50 flex items-center justify-center p-3 md:p-6 pointer-events-none", "data-testid": "run-report-overlay"},
            div(
                {class: "w-full h-full max-w-[1600px] max-h-[94vh] bg-gray-900 border border-gray-700 rounded-xl shadow-2xl flex flex-col overflow-hidden pointer-events-auto", onclick: (e) => e.stopPropagation()},
                div(
                    {class: "flex items-center justify-between gap-4 px-4 py-3 border-b border-gray-700"},
                    div(
                        {class: "flex min-w-0 items-baseline gap-2.5"},
                        h2({class: "text-sm font-semibold text-gray-200 truncate"},
                            () => `Run report: ${name()} · #${deploymentId} · v${version}`),
                        headerMeta,
                    ),
                    button({class: "text-sm text-gray-400 hover:text-gray-200 cursor-pointer px-3 py-1.5", onclick: onClose}, "Close"),
                ),
                div({class: "flex min-h-0 flex-1"},
                    () => sidebar(),
                    div({class: "flex min-h-0 min-w-0 flex-1 flex-col bg-gray-900"}, body)),
            ),
        ),
    );
}
