import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {loginS} from "../state/login.js";
import {deploymentsS} from "../state/deployments.js";

const {div, p, select, option, input, button, pre, span, label} = van.tags;

const LEVELS = ['', 'TRACE', 'DEBUG', 'INFO', 'WARN', 'ERROR', 'FATAL'];
const SYSTEM_ENVIRONMENT = 'OPENDEPLOY';
const SYSTEM_DEPLOYMENT_NAME = 'opendeploy';
const DEFAULT_LOG_LINE_LIMIT = 10000;

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
    return [cid.machine, cid.name].filter(Boolean).join(' / ') || `#${cfg.id}`;
}

function deploymentEnvironment(item) {
    return item?.config?.configId?.environment || '';
}

function selectedDeployment(items, id) {
    return items.find(item => item.config?.id === id) || null;
}

function isSystemDeployment(item) {
    const cid = item?.config?.configId || {};
    return cid.environment === SYSTEM_ENVIRONMENT && cid.name === SYSTEM_DEPLOYMENT_NAME;
}

function environmentSort(a, b) {
    if (a === SYSTEM_ENVIRONMENT && b !== SYSTEM_ENVIRONMENT) return 1;
    if (b === SYSTEM_ENVIRONMENT && a !== SYSTEM_ENVIRONMENT) return -1;
    return a.localeCompare(b);
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
    const environment = van.state('');
    const deploymentId = van.state(selectedDeploymentId.val || 0);
    const timeStart = van.state(toLocalInputValue(new Date(now.getTime() - 24 * 60 * 60 * 1000)));
    const timeEnd = van.state('');
    const levelMin = van.state('');
    const output = van.state('');
    const status = van.state('Choose filters, then search.');
    const loading = van.state(false);
    let activeAbort = null;
    let autoSearchedDeploymentId = 0;

    const environmentSelect = select({
        "data-testid": "logs-environment-select",
        class: "input min-w-48",
        onchange: (e) => {
            environment.val = e.target.value;
            const items = (deploymentsS.val || []).filter(item => item.config?.id && !item.config.deleted);
            const current = selectedDeployment(items, Number(deploymentId.val || 0));
            if (current && environment.val && deploymentEnvironment(current) !== environment.val) {
                deploymentId.val = 0;
            }
        },
    });

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
        const environments = [...new Set(items.map(deploymentEnvironment))].sort(environmentSort);
        environmentSelect.replaceChildren(
            option({value: ""}, "All environments"),
            ...environments.map(env => option({value: env}, env || 'No environment')),
        );
        environmentSelect.value = environment.val;
    });

    van.derive(() => {
        const items = (deploymentsS.val || []).filter(item => item.config?.id && !item.config.deleted);
        const filtered = environment.val ? items.filter(item => deploymentEnvironment(item) === environment.val) : items;
        if (deploymentId.val && filtered.length > 0 && !selectedDeployment(filtered, Number(deploymentId.val))) {
            deploymentId.val = 0;
        }
        deploymentSelect.replaceChildren(
            option({value: ""}, "Select deployment"),
            ...filtered.map(item => option({value: String(item.config.id)}, deploymentLabel(item))),
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
            const items = (deploymentsS.val || []).filter(item => item.config?.id && !item.config.deleted);
            const selected = selectedDeployment(items, id);
            const systemDeployment = isSystemDeployment(selected);
            const machine = selected?.config?.configId?.machine || '';
            const payload = {
                deploymentId: systemDeployment ? 0 : id,
                timeStart: start,
                timeEnd: end || undefined,
                levelMin: levelMin.val,
                searchKeys: systemDeployment ? {machine} : undefined,
                logLineLimit: DEFAULT_LOG_LINE_LIMIT,
            };
            for await (const batch of capi.postV1DeploymentLogSearch(payload, {signal: activeAbort.signal})) {
                const lines = batch.lines || [];
                count += lines.length;
                output.val += lines.map(line => `${formatLine(line)}\n`).join('');
            }
            status.val = count >= DEFAULT_LOG_LINE_LIMIT
                ? `Showing newest ${DEFAULT_LOG_LINE_LIMIT.toLocaleString()} log lines.`
                : `${count} log line${count === 1 ? '' : 's'} returned.`;
        } catch (e) {
            if (e.name !== 'AbortError') {
                status.val = `Search failed: ${e.message || e}`;
            }
        } finally {
            loading.val = false;
        }
    };

    van.derive(() => {
        const id = Number(deploymentId.val || 0);
        if (!id || !loginS.val || autoSearchedDeploymentId === id) return;
        autoSearchedDeploymentId = id;
        setTimeout(() => {
            if (Number(deploymentId.val || 0) === id) void runSearch();
        }, 0);
    });

    const field = (caption, node) => label(
        {class: "flex flex-col gap-1 text-xs uppercase tracking-wide text-gray-500"},
        span(caption),
        node,
    );

    const setQuickRange = (durationMs) => {
        const end = new Date();
        timeStart.val = toLocalInputValue(new Date(end.getTime() - durationMs));
        timeEnd.val = toLocalInputValue(end);
    };

    const quickRangeButton = (text, durationMs) => button({
        type: "button",
        class: "whitespace-nowrap rounded-lg bg-gray-700 px-2.5 py-1.5 text-xs text-gray-200 transition-colors cursor-pointer hover:bg-gray-600",
        onclick: () => setQuickRange(durationMs),
    }, text);

    return div(
        {class: "h-full min-h-0 overflow-hidden p-3 flex flex-col gap-2"},
        div(
            {class: "card p-3 flex flex-wrap items-end gap-2"},
            field("Environment", environmentSelect),
            field("Deployment", deploymentSelect),
            field("Minimum level", select({
                    "data-testid": "logs-level-min-select",
                    class: "input",
                    value: () => levelMin.val,
                    onchange: (e) => { levelMin.val = e.target.value; },
                },
                ...LEVELS.map(level => option({value: level}, level || 'Any')),
            )),
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
            div(
                {class: "flex flex-wrap items-center gap-1.5 pb-0.5"},
                quickRangeButton("Last 10min", 10 * 60 * 1000),
                quickRangeButton("Last hour", 60 * 60 * 1000),
                quickRangeButton("Last day", 24 * 60 * 60 * 1000),
                quickRangeButton("Last 3 days", 3 * 24 * 60 * 60 * 1000),
            ),
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
            {"data-testid": "logs-output", class: "rounded-lg bg-gray-950 border border-gray-800 p-3 overflow-auto flex-1 min-h-0 text-xs font-mono whitespace-pre-wrap break-all leading-5 text-gray-200"},
            () => output.val || 'No log lines loaded.',
        ),
    );
}
