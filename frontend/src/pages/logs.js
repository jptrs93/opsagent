import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {loginS} from "../state/login.js";
import {deploymentsS} from "../state/deployments.js";

const {div, p, select, option, input, button, pre, span, label} = van.tags;

const LEVELS = ['', 'TRACE', 'DEBUG', 'INFO', 'WARN', 'ERROR', 'FATAL'];

function toLocalInputValue(date) {
    const pad = (n) => String(n).padStart(2, '0');
    return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

function fromLocalInputValue(value) {
    if (!value) return null;
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? null : date;
}

function deploymentLabel(item) {
    const cfg = item?.config || {};
    const cid = cfg.configId || {};
    return [cid.environment, cid.machine, cid.name].filter(Boolean).join(' / ') || `#${cfg.id}`;
}

function formatLogfmtValue(value, forceQuote = false) {
    const s = String(value);
    if (!forceQuote && /^[^\s"=\\]+$/.test(s)) return s;
    return JSON.stringify(s)
        .replace(/</g, '\\u003c')
        .replace(/>/g, '\\u003e')
        .replace(/&/g, '\\u0026');
}

function formatLine(line) {
    const time = line.time instanceof Date ? line.time.toISOString() : '';
    const props = Object.entries(line.props || {})
        .map(([k, v]) => `${k}=${formatLogfmtValue(v)}`)
        .join(' ');
    const fields = [
        `time=${formatLogfmtValue(time)}`,
        `level=${formatLogfmtValue(line.level || '')}`,
    ];
    if (line.msg) fields.push(`msg=${formatLogfmtValue(line.msg, true)}`);
    if (props) fields.push(props);
    return fields.join(' ');
}

export function logsPage(selectedDeploymentId) {
    const now = new Date();
    const deploymentId = van.state(selectedDeploymentId.val || 0);
    const timeStart = van.state(toLocalInputValue(new Date(now.getTime() - 24 * 60 * 60 * 1000)));
    const timeEnd = van.state('');
    const levelMin = van.state('');
    const output = van.state('');
    const status = van.state('Choose filters, then search.');
    const loading = van.state(false);
    let activeAbort = null;

    const deploymentSelect = select({
        "data-testid": "logs-deployment-select",
        class: "input min-w-72",
        onchange: (e) => { deploymentId.val = Number(e.target.value || 0); },
    });

    van.derive(() => {
        if (selectedDeploymentId.val && selectedDeploymentId.val !== deploymentId.val) {
            deploymentId.val = selectedDeploymentId.val;
        }
    });

    van.derive(() => {
        const items = (deploymentsS.val || []).filter(item => item.config?.id && !item.config.deleted);
        deploymentSelect.replaceChildren(
            option({value: ""}, "Select deployment"),
            ...items.map(item => option({value: String(item.config.id)}, deploymentLabel(item))),
        );
        deploymentSelect.value = String(deploymentId.val || '');
    });

    const runSearch = async () => {
        if (activeAbort) activeAbort.abort();
        output.val = '';
        const id = Number(deploymentId.val || 0);
        const start = fromLocalInputValue(timeStart.val);
        const end = fromLocalInputValue(timeEnd.val);
        if (!id || !start) {
            status.val = 'Deployment and start time are required.';
            return;
        }
        activeAbort = new AbortController();
        loading.val = true;
        status.val = 'Searching logs...';
        let count = 0;
        try {
            const payload = {
                deploymentId: id,
                timeStart: start,
                timeEnd: end || undefined,
                levelMin: levelMin.val,
                searchKeys: undefined,
            };
            for await (const line of capi.postV1DeploymentLogSearch(payload, {signal: activeAbort.signal})) {
                count += 1;
                output.val += `${formatLine(line)}\n`;
            }
            status.val = `${count} log line${count === 1 ? '' : 's'} returned.`;
        } catch (e) {
            if (e.name !== 'AbortError') {
                status.val = `Search failed: ${e.message || e}`;
            }
        } finally {
            loading.val = false;
        }
    };

    const field = (caption, node) => label(
        {class: "flex flex-col gap-1 text-xs uppercase tracking-wide text-gray-500"},
        span(caption),
        node,
    );

    return div(
        {class: "h-full min-h-0 overflow-hidden p-6 flex flex-col gap-4"},
        div(
            {class: "card flex flex-wrap items-end gap-3"},
            field("Deployment", deploymentSelect),
            field("From", input({
                "data-testid": "logs-time-start-input",
                class: "input",
                type: "datetime-local",
                value: () => timeStart.val,
                oninput: (e) => { timeStart.val = e.target.value; },
            })),
            field("To", input({
                "data-testid": "logs-time-end-input",
                class: "input",
                type: "datetime-local",
                value: () => timeEnd.val,
                oninput: (e) => { timeEnd.val = e.target.value; },
            })),
            field("Minimum level", select({
                    "data-testid": "logs-level-min-select",
                    class: "input",
                    value: () => levelMin.val,
                    onchange: (e) => { levelMin.val = e.target.value; },
                },
                ...LEVELS.map(level => option({value: level}, level || 'Any')),
            )),
            div({class: "flex-1"}),
            button({
                "data-testid": "logs-search-button",
                class: "btn-primary px-4 py-2 text-sm cursor-pointer disabled:opacity-50",
                disabled: () => loading.val || !loginS.val,
                onclick: runSearch,
            }, () => loading.val ? 'Searching...' : 'Search'),
        ),
        p({class: "sr-only", "aria-live": "polite"}, () => status.val),
        pre(
            {"data-testid": "logs-output", class: "rounded-lg bg-gray-950 border border-gray-800 p-4 overflow-auto flex-1 min-h-0 text-xs font-mono whitespace-pre-wrap break-all leading-5 text-gray-200"},
            () => output.val || 'No log lines loaded.',
        ),
    );
}
