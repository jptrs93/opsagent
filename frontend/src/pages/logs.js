import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {checkIcon, copyIcon} from "../lib/icons.js";
import {loginS} from "../state/login.js";
import {deploymentsS, machinesS, spacesS} from "../state/deployments.js";
import {machineDisplayName} from "../lib/machines.js";

const {div, p, select, option, input, button, pre, span, label} = van.tags;

const SYSTEM_SPACE_ID = 0;
const SYSTEM_DEPLOYMENT_NAME = 'opendeploy';
const DEFAULT_LOG_LINE_LIMIT = 10000;
const MAX_CONFIG_VERSION_OPTIONS = 20;
const LOG_LEVELS = ['DEBUG', 'INFO', 'WARN', 'ERROR'];
const textDecoder = new TextDecoder();

function toLocalInputValue(date) {
    const pad = (n) => String(n).padStart(2, '0');
    return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

function fromLocalInputValue(value) {
    if (!value) return null;
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? null : date;
}

function deploymentLabel(item, machines) {
    const cfg = item?.config || {};
    const cid = cfg.configId || {};
    const machine = cid.machine ? machineDisplayName(cid.machine, machines) : '';
    return [machine, cid.name].filter(Boolean).join(' / ') || `#${cfg.id}`;
}

function deploymentSpaceID(item) {
    return item?.config?.configId?.spaceId || 0;
}

function selectedDeployment(items, id) {
    return items.find(item => item.config?.id === id) || null;
}

function isSystemDeployment(item) {
    const cid = item?.config?.configId || {};
    return cid.name === SYSTEM_DEPLOYMENT_NAME && (
        deploymentSpaceID(item) === SYSTEM_SPACE_ID || Boolean(item?.config?.spec?.runner?.systemd)
    );
}

function deploymentConfigVersion(item) {
    return Number(item?.config?.version || 0);
}

function configVersionOptions(item) {
    const current = deploymentConfigVersion(item);
    if (!current) return [];
    const min = Math.max(1, current - MAX_CONFIG_VERSION_OPTIONS + 1);
    const versions = [];
    for (let version = current; version >= min; version--) {
        versions.push(version);
    }
    return versions;
}

function formatLine(line) {
    if (line.line instanceof Uint8Array) return textDecoder.decode(line.line);
    return String(line.line || '');
}

function formatSummaryDate(date) {
    if (!(date instanceof Date) || Number.isNaN(date.getTime())) return '';
    return date.toLocaleString(undefined, {
        year: 'numeric',
        month: 'short',
        day: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
    });
}

function durationAgo(date, now = new Date()) {
    if (!(date instanceof Date) || Number.isNaN(date.getTime())) return 'unknown';
    const seconds = Math.max(0, Math.floor((now.getTime() - date.getTime()) / 1000));
    if (seconds < 5) return 'just now';
    if (seconds < 60) return `${seconds} seconds ago`;
    const minutes = Math.floor(seconds / 60);
    if (minutes < 60) return `${minutes} minute${minutes === 1 ? '' : 's'} ago`;
    const hours = Math.floor(minutes / 60);
    if (hours < 24) return `${hours} hour${hours === 1 ? '' : 's'} ago`;
    const days = Math.floor(hours / 24);
    return `${days} day${days === 1 ? '' : 's'} ago`;
}

export function logsPage(selectedDeploymentId) {
    const now = new Date();
    const spaceId = van.state('');
    const deploymentId = van.state(selectedDeploymentId.val || 0);
    const configVersion = van.state(0);
    const levelMin = van.state('');
    const searchStr = van.state('');
    const resultSearchStr = van.state('');
    const timeStart = van.state(toLocalInputValue(new Date(now.getTime() - 24 * 60 * 60 * 1000)));
    const timeEnd = van.state('');
    const output = van.state('');
    const status = van.state('Choose filters, then search.');
    const lastSearch = van.state(null);
    const loading = van.state(false);
    const logDirCopied = van.state(false);
    let activeAbort = null;
    let autoSearchedDeploymentId = 0;

    const spaceSelect = select({
        "data-testid": "logs-space-select",
        class: "input min-w-48",
        onchange: (e) => {
            spaceId.val = e.target.value;
            const items = (deploymentsS.val || []).filter(item => item.config?.id && !item.config.deleted);
            const current = selectedDeployment(items, Number(deploymentId.val || 0));
            if (current && spaceId.val !== '' && deploymentSpaceID(current) !== Number(spaceId.val)) {
                deploymentId.val = 0;
                configVersion.val = 0;
            }
        },
    });

    const deploymentSelect = select({
        "data-testid": "logs-deployment-select",
        class: "input min-w-72",
        onchange: (e) => {
            deploymentId.val = Number(e.target.value || 0);
            configVersion.val = 0;
        },
    });

    const configVersionSelect = select({
        "data-testid": "logs-config-version-select",
        class: "input min-w-36",
        onchange: (e) => { configVersion.val = Number(e.target.value || 0); },
    });

    const levelSelect = select({
        "data-testid": "logs-level-select",
        class: "input min-w-36",
        onchange: (e) => { levelMin.val = e.target.value; },
    },
        option({value: ""}, "All levels"),
        ...LOG_LEVELS.map(level => option({value: level}, level)),
    );

    van.derive(() => {
        if (selectedDeploymentId.val && selectedDeploymentId.val !== deploymentId.val) {
            deploymentId.val = selectedDeploymentId.val;
            configVersion.val = 0;
        }
    });

    van.derive(() => {
        const items = (deploymentsS.val || []).filter(item => item.config?.id && !item.config.deleted);
        const activeSpaceIDs = new Set(items.map(deploymentSpaceID));
        const spaces = (spacesS.val || []).filter(space => activeSpaceIDs.has(space.id));
        spaceSelect.replaceChildren(
            option({value: ""}, "All spaces"),
            ...spaces.map(space => option({value: String(space.id)}, space.name || `space ${space.id}`)),
        );
        spaceSelect.value = spaceId.val;
    });

    van.derive(() => {
        const items = (deploymentsS.val || []).filter(item => item.config?.id && !item.config.deleted);
        const filtered = spaceId.val !== '' ? items.filter(item => deploymentSpaceID(item) === Number(spaceId.val)) : items;
        if (deploymentId.val && filtered.length > 0 && !selectedDeployment(filtered, Number(deploymentId.val))) {
            deploymentId.val = 0;
            configVersion.val = 0;
        }
        deploymentSelect.replaceChildren(
            option({value: ""}, "Select deployment"),
            ...filtered.map(item => option({value: String(item.config.id)}, deploymentLabel(item, machinesS.val))),
        );
        deploymentSelect.value = String(deploymentId.val || '');
    });

    van.derive(() => {
        const items = (deploymentsS.val || []).filter(item => item.config?.id && !item.config.deleted);
        const selected = selectedDeployment(items, Number(deploymentId.val || 0));
        const systemDeployment = isSystemDeployment(selected);
        const versions = systemDeployment ? [] : configVersionOptions(selected);
        if (configVersion.val && !versions.includes(Number(configVersion.val))) {
            configVersion.val = 0;
        }
        configVersionSelect.replaceChildren(
            option({value: ""}, systemDeployment ? "All system versions" : "All versions"),
            ...versions.map(version => option({value: String(version)}, `Config v${version}`)),
        );
        configVersionSelect.value = String(configVersion.val || '');
        configVersionSelect.disabled = !selected || systemDeployment;
    });

    const runSearch = async () => {
        if (activeAbort) activeAbort.abort();
        output.val = '';
        lastSearch.val = null;
        logDirCopied.val = false;
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
        let logDir = '';
        try {
            const items = (deploymentsS.val || []).filter(item => item.config?.id && !item.config.deleted);
            const selected = selectedDeployment(items, id);
            const selectedLabel = deploymentLabel(selected, machinesS.val);
            const systemDeployment = isSystemDeployment(selected);
            const machine = selected?.config?.configId?.machine || '';
            const selectedConfigVersion = systemDeployment ? 0 : Number(configVersion.val || 0);
            const payload = {
                deploymentId: systemDeployment ? 0 : id,
                timeStart: start,
                timeEnd: end || undefined,
                searchKeys: systemDeployment ? {machine} : undefined,
                logLineLimit: DEFAULT_LOG_LINE_LIMIT,
                configVersion: selectedConfigVersion,
                levelMin: levelMin.val || undefined,
                searchStr: searchStr.val || undefined,
            };
            for await (const batch of capi.postV1DeploymentLogSearch(payload, {signal: activeAbort.signal})) {
                if (batch.logDir) {
                    logDir = batch.logDir;
                }
                const lines = batch.lines || [];
                count += lines.length;
                output.val += lines.map(formatLine).join('');
            }
            status.val = count >= DEFAULT_LOG_LINE_LIMIT
                ? `Showing newest ${DEFAULT_LOG_LINE_LIMIT.toLocaleString()} log lines.`
                : `${count} log line${count === 1 ? '' : 's'} returned.`;
            const refreshedAt = new Date();
            lastSearch.val = {
                deploymentName: selectedLabel,
                configVersion: selectedConfigVersion,
                start,
                end: end || refreshedAt,
                count,
                refreshedAt,
                logDir,
            };
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
        {class: "flex flex-col gap-0 text-[11px] tracking-wide text-gray-500"},
        span(String(caption).toLowerCase()),
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

    const filteredOutput = () => {
        const search = resultSearchStr.val;
        if (!search) return output.val;
        const lines = output.val.match(/[^\n]*\n|[^\n]+/g) || [];
        return lines.filter(line => line.includes(search)).join('');
    };

    const summaryLine = () => {
        if (loading.val) return 'Searching logs...';
        const search = lastSearch.val;
        if (!search) return 'No logs search made.';
        const lineWord = search.count === 1 ? 'log line' : 'log lines';
        const versionText = search.configVersion ? ` config v${search.configVersion}` : ' all versions';
        return `Showing${versionText} logs for ${search.deploymentName} from ${formatSummaryDate(search.start)} to ${formatSummaryDate(search.end)}. Result ${search.count.toLocaleString()} ${lineWord}. Refreshed ${durationAgo(search.refreshedAt)}.`;
    };

    const copyLogDir = async () => {
        const logDir = lastSearch.val?.logDir;
        if (!logDir) return;
        await navigator.clipboard.writeText(logDir);
        logDirCopied.val = true;
        setTimeout(() => { logDirCopied.val = false; }, 1500);
    };

    const logDirLine = () => {
        const logDir = lastSearch.val?.logDir;
        if (!logDir) return '';
        return div(
            {class: "flex min-w-0 max-w-full items-center gap-1.5 justify-self-end text-gray-400 sm:max-w-[45vw]"},
            span({class: "whitespace-nowrap"}, "Log dir:"),
            span({class: "truncate font-mono text-gray-300", title: logDir}, logDir),
            button({
                type: "button",
                class: "rounded p-1 text-gray-400 transition-colors hover:bg-gray-800 hover:text-gray-200 cursor-pointer",
                title: () => logDirCopied.val ? "Copied" : "Copy log directory",
                "aria-label": () => logDirCopied.val ? "Copied" : "Copy log directory",
                onclick: copyLogDir,
            }, () => logDirCopied.val
                ? checkIcon({class: "w-3.5 h-3.5 text-green-400"})
                : copyIcon({class: "w-3.5 h-3.5"})),
        );
    };

    return div(
        {class: "h-full min-h-0 overflow-hidden p-3 flex flex-col gap-2"},
        div(
            {class: "card p-2 flex flex-wrap items-stretch gap-2"},
            div(
                {class: "flex flex-col gap-1"},
                field("space", spaceSelect),
                field("deployment", deploymentSelect),
            ),
            div(
                {class: "flex flex-col gap-1"},
                field("version", configVersionSelect),
                field("level", levelSelect),
            ),
            div(
                {class: "flex flex-col gap-1"},
                field("from", input({
                    "data-testid": "logs-time-start-input",
                    class: "input",
                    type: "datetime-local",
                    value: () => timeStart.val,
                    oninput: (e) => { timeStart.val = e.target.value; },
                })),
                field("to", input({
                    "data-testid": "logs-time-end-input",
                    class: "input",
                    type: "datetime-local",
                    value: () => timeEnd.val,
                    oninput: (e) => { timeEnd.val = e.target.value; },
                })),
            ),
            div(
                {class: "flex flex-col justify-end gap-1"},
                div({class: "flex gap-1.5"},
                    quickRangeButton("Last 10min", 10 * 60 * 1000),
                    quickRangeButton("Last hour", 60 * 60 * 1000),
                ),
                div({class: "flex gap-1.5"},
                    quickRangeButton("Last day", 24 * 60 * 60 * 1000),
                    quickRangeButton("Last 3 days", 3 * 24 * 60 * 60 * 1000),
                ),
            ),
            div(
                {class: "flex items-center"},
                field("search", input({
                    "data-testid": "logs-search-str-input",
                    class: "input min-w-56",
                    placeholder: "case-sensitive line match",
                    value: () => searchStr.val,
                    oninput: (e) => { searchStr.val = e.target.value; },
                })),
            ),
            div({class: "flex-1"}),
            button({
                "data-testid": "logs-search-button",
                class: "btn-primary px-4 py-2 text-sm cursor-pointer disabled:opacity-50",
                disabled: () => loading.val || !loginS.val,
                onclick: runSearch,
            }, () => loading.val ? 'Searching...' : 'Search'),
        ),
        div(
            {class: "px-1 grid grid-cols-1 items-center gap-x-4 gap-y-1 text-xs md:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)]"},
            p({class: "min-w-0 text-gray-400"}, summaryLine),
            input({
                "data-testid": "logs-result-filter-input",
                class: "input h-7 w-full px-2 py-1 text-xs md:w-64",
                placeholder: "filter returned lines",
                value: () => resultSearchStr.val,
                oninput: (e) => { resultSearchStr.val = e.target.value; },
            }),
            logDirLine,
        ),
        p({class: "sr-only", "aria-live": "polite"}, () => status.val),
        pre(
            {"data-testid": "logs-output", class: "rounded-lg bg-gray-950 border border-gray-800 p-3 overflow-auto flex-1 min-h-0 text-xs font-mono whitespace-pre-wrap break-all leading-5 text-gray-200"},
            filteredOutput,
        ),
    );
}
