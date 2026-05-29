import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {format} from "date-fns";
import {resolveUserDisplayName} from "../lib/users.js";

const { div, h2, span, button, p } = van.tags;

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

const tsMs = (t) => (t instanceof Date ? t.getTime() : 0);

export function deploymentConfigHistory(deploymentId, label, onClose) {
    const configs = van.state(null);
    const error = van.state('');

    const load = async () => {
        try {
            const decoded = await capi.postV1DeploymentConfigHistory({ deploymentId });
            configs.val = decoded?.configs || [];
        } catch (e) {
            console.error('Failed to load config history:', e);
            error.val = 'Connection error';
            configs.val = [];
        }
    };

    setTimeout(load, 0);

    return div(
        {class: "min-h-0 bg-gray-900 flex flex-col h-full"},
        div(
            {class: "flex items-center justify-between p-3 border-b border-gray-700"},
            h2({class: "text-sm font-semibold text-gray-300"}, `Config history: ${label || `#${deploymentId}`}`),
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
                if (configs.val === null) {
                    return p({class: "p-4 text-sm text-gray-500"}, "Loading...");
                }
                if (configs.val.length === 0) {
                    return p({class: "p-4 text-sm text-gray-500"}, "No history.");
                }

                // Walk oldest-first so each entry can be diffed against the prior version.
                const asc = [...configs.val].sort((a, b) => a.version - b.version);
                const lines = asc.map((c, i) => {
                    const prev = i > 0 ? asc[i - 1] : null;
                    const desc = describeConfigEntry(c, prev);
                    const ts = tsMs(c.updatedAt) > 0 ? format(c.updatedAt, "MMM d HH:mm:ss") : '';
                    const userName = resolveUserDisplayName(c.updatedBy);

                    return div(
                        {class: "px-3 py-0.5 text-xs font-mono text-orange-400"},
                        span(ts),
                        span("  "),
                        span(`v${c.version} `),
                        span(desc),
                        userName ? span({class: "text-orange-300"}, ` [${userName}]`) : null,
                    );
                });

                // Display newest-first.
                lines.reverse();
                return div({class: "flex flex-col"}, ...lines);
            }
        )
    );
}
