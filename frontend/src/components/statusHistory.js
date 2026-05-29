import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {format} from "date-fns";

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

const STABLE_WINDOW_MS = 10 * 60 * 1000;

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

const tsMs = (t) => (t instanceof Date ? t.getTime() : 0);

export function deploymentStatusHistory(deploymentId, label, onClose) {
    const statuses = van.state(null);
    const error = van.state('');

    const load = async () => {
        try {
            const decoded = await capi.postV1DeploymentStatusHistory({ deploymentId });
            statuses.val = decoded?.statuses || [];
        } catch (e) {
            console.error('Failed to load status history:', e);
            error.val = 'Connection error';
            statuses.val = [];
        }
    };

    setTimeout(load, 0);

    return div(
        {class: "min-h-0 bg-gray-900 flex flex-col h-full"},
        div(
            {class: "flex items-center justify-between p-3 border-b border-gray-700"},
            h2({class: "text-sm font-semibold text-gray-300"}, `Status history: ${label || `#${deploymentId}`}`),
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
                if (statuses.val === null) {
                    return p({class: "p-4 text-sm text-gray-500"}, "Loading...");
                }

                // Statuses arrive oldest-first. Drop seq_no=0 placeholder rows
                // (inserted to satisfy the status-never-nil invariant — no
                // preparer/runner data, would render as empty "status update").
                const asc = statuses.val.filter(s => s.statusSeqNo > 0);
                if (asc.length === 0) {
                    return p({class: "p-4 text-sm text-gray-500"}, "No history.");
                }

                const lines = asc.map((s, i) => {
                    const prev = i > 0 ? asc[i - 1] : null;
                    const next = i < asc.length - 1 ? asc[i + 1] : null;
                    const desc = describeStatusEntry(s, prev);
                    const ts = tsMs(s.timestamp) > 0 ? format(s.timestamp, "MMM d HH:mm:ss") : '';

                    const transitionedToRunning = runnerChanged(s, prev)
                        && s.runner && s.runner.status === 2;
                    // Stable if it's the latest entry, or nothing changed for a while after it.
                    const stable = !next || (tsMs(next.timestamp) - tsMs(s.timestamp) > STABLE_WINDOW_MS);
                    const color = transitionedToRunning && stable ? "text-green-500" : "text-gray-500";

                    return div(
                        {class: `px-3 py-0.5 text-xs font-mono ${color}`},
                        span(ts),
                        span("  "),
                        span(desc),
                    );
                });

                // Display newest-first.
                lines.reverse();
                return div({class: "flex flex-col"}, ...lines);
            }
        )
    );
}
