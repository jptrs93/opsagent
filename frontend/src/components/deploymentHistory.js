import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {format} from "date-fns";
import {decodeDeployments} from "../capi/model.js";

const { div, h2, span, button, p } = van.tags;

const deploymentStatusLabel = {
    0: 'Unknown', 1: 'No Deployment', 2: 'Running', 3: 'Stopped', 4: 'Starting', 5: 'Crashed',
};

const preparationLabel = {
    0: 'Unknown', 2: 'Preparing', 3: 'Downloading', 4: 'Ready', 5: 'Failed',
};

// Deployment records are now pure status event logs — no more DesiredState
// embedded in them, so describeChange is purely about observed transitions.
function describeChange(record, prevRecord) {
    if (!prevRecord) {
        return 'deployment created';
    }

    const parts = [];
    const curr = { running: record.runningStatus || {}, prep: record.preparation || {} };
    const prev = { running: prevRecord.runningStatus || {}, prep: prevRecord.preparation || {} };

    if (curr.running.version !== prev.running.version && curr.running.version) {
        parts.push(`running_version=${curr.running.version.substring(0, 7)}`);
    }
    if (curr.running.status !== prev.running.status) {
        parts.push(`running_status=${deploymentStatusLabel[curr.running.status] || 'Unknown'}`);
    }
    if (curr.prep.status !== prev.prep.status) {
        parts.push(`preparation=${preparationLabel[curr.prep.status] || 'Unknown'}`);
    }
    if (curr.prep.version !== prev.prep.version && curr.prep.version) {
        parts.push(`prepare_version=${curr.prep.version.substring(0, 7)}`);
    }
    if (curr.running.pid !== prev.running.pid && curr.running.pid > 0) {
        parts.push(`pid=${curr.running.pid}`);
    }

    return parts.length > 0 ? parts.join(', ') : '(no change)';
}

function splitKey(key) {
    const idx = key.indexOf(':');
    if (idx === -1) {
        return { machine: '', deploymentName: key };
    }
    return {
        machine: key.slice(0, idx),
        deploymentName: key.slice(idx + 1),
    };
}

export function deploymentHistory(key, onClose) {
    const entries = van.state(null);
    const error = van.state('');
    const { machine, deploymentName } = splitKey(key);

    const load = async () => {
        try {
            const headers = capi.headerProvider() || {};
            headers['Accept'] = 'application/x-protobuf';
            let url = `/v1/deployment/history?deployment=${encodeURIComponent(deploymentName)}`;
            if (machine) {
                url += `&machine=${encodeURIComponent(machine)}`;
            }

            const response = await fetch(url, {
                headers,
                credentials: 'include',
            });
            if (!response.ok) throw new Error(`HTTP ${response.status}`);
            const decoded = decodeDeployments(await response.arrayBuffer());
            entries.val = decoded?.deployments || [];
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
                // entries are newest-first from the API; reverse for prev-record logic
                const chronological = [...entries.val].reverse();
                const lines = chronological.map((e, i) => {
                    const prev = i > 0 ? chronological[i - 1] : null;
                    const next = i < chronological.length - 1 ? chronological[i + 1] : null;
                    const ts = e.timestamp instanceof Date && e.timestamp.getTime() > 0
                        ? format(e.timestamp, "MMM d HH:mm:ss")
                        : '';
                    const desc = describeChange(e, prev);
                    if (i > 0 && desc === '(no change)') {
                        return null;
                    }

                    const currStatus = (e.runningStatus || {}).status || 0;
                    const prevStatus = prev ? ((prev.runningStatus || {}).status || 0) : -1;
                    const becameRunning = currStatus === 2 && prevStatus !== 2;
                    const becameCrashed = currStatus === 5 && prevStatus !== 5;

                    let color;
                    if (becameCrashed) {
                        color = "text-red-400";
                    } else if (becameRunning) {
                        const isHead = !next;
                        const gapMs = next && next.timestamp instanceof Date && e.timestamp instanceof Date
                            ? next.timestamp.getTime() - e.timestamp.getTime() : Infinity;
                        color = (isHead || gapMs > 30000) ? "text-green-400" : "text-gray-500";
                    } else {
                        color = "text-gray-500";
                    }

                    return div(
                        {class: `px-3 py-0.5 text-xs font-mono ${color}`},
                        span(ts),
                        span("  "),
                        span(desc),
                    );
                }).filter(Boolean);

                if (lines.length === 0) {
                    return p({class: "p-4 text-sm text-gray-500"}, "No meaningful changes yet.");
                }

                // display newest first
                lines.reverse();
                return div({class: "flex flex-col"}, ...lines);
            }
        )
    );
}
