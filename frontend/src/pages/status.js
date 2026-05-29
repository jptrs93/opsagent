import van from "vanjs-core";
import {Info} from "vanjs-feather";
import {deploymentsS, deploymentsStreamS} from "../state/deployments.js";
import {statusRow} from "../components/statusCard.js";
import {deploymentLogs} from "../components/deploymentLogs.js";
import {deploymentStatusHistory} from "../components/statusHistory.js";
import {deploymentConfigHistory} from "../components/configHistory.js";
import {deployOverlay} from "../components/deployOverlay.js";
import {createOverlay} from "../components/createOverlay.js";

const { div, h1, p, button, table, thead, tbody, tr, th, span } = van.tags;

const SIDEBAR_WIDTH_KEY = 'opsagent_sidebar_width';
const DEFAULT_SIDEBAR_PCT = 50;
const MIN_SIDEBAR_PCT = 20;
const MAX_SIDEBAR_PCT = 80;

function loadSidebarWidth() {
    try {
        const v = parseFloat(localStorage.getItem(SIDEBAR_WIDTH_KEY));
        if (v >= MIN_SIDEBAR_PCT && v <= MAX_SIDEBAR_PCT) return v;
    } catch {}
    return DEFAULT_SIDEBAR_PCT;
}

function saveSidebarWidth(pct) {
    try { localStorage.setItem(SIDEBAR_WIDTH_KEY, String(pct)); } catch {}
}

// Sidebar modes
const SIDEBAR_NONE = null;
const SIDEBAR_PREPARE = 'prepare';
const SIDEBAR_RUN = 'run';
const SIDEBAR_STATUS_HISTORY = 'status-history';
const SIDEBAR_CONFIG_HISTORY = 'config-history';

const formatDeploymentLabel = (deployment) => {
    if (!deployment) return 'unknown deployment';
    const parts = [deployment.environment, deployment.machine, deployment.name].filter(Boolean);
    return parts.length > 0 ? parts.join(' / ') : `#${deployment.id}`;
};

const environmentLabel = (environment) => environment || 'No environment';

const groupDeploymentsByEnvironment = (deployments) => {
    const groups = new Map();
    for (const deployment of deployments) {
        const environment = deployment.environment || '';
        if (!groups.has(environment)) {
            groups.set(environment, []);
        }
        groups.get(environment).push(deployment);
    }
    return [...groups.entries()]
        .sort(([a], [b]) => environmentSortRank(a) - environmentSortRank(b) || a.localeCompare(b))
        .map(([environment, rows]) => ({environment, rows}));
};

const headerTips = {
    deployment: 'Deployment name. Use history to inspect config and status changes.',
    environment: 'Logical environment for grouping deployments.',
    machine: 'Cluster machine where this deployment is reconciled.',
    status: 'Current runner status. Click the badge to view run output.',
    version: 'Desired deployed commit or GitHub release tag.',
    prepare: 'Latest prepare/build/download result. Click to view prepare logs.',
    restarts: 'Runner restart count and last restart time for the current deployment version.',
    deployed: 'User and timestamp of the latest deployment config change.',
    actions: 'Open the update overlay to deploy, start, or stop this deployment.',
};

const environmentSortRank = (environment) => {
    if (environment === 'PROD') return 0;
    if (environment === 'STAGING') return 1;
    return 2;
};

// mapDeploymentsToView flattens DeploymentWithStatus[] into the shape
// the status card component expects.
const mapDeploymentsToView = (deployments) => {
    if (!Array.isArray(deployments)) return [];

    return deployments.filter(d => d.config && d.config.id && !d.config.deleted).map((d) => {
        const id = d.config.id; // integer
        const cid = d.config.configId || {};
        const spec = d.config.spec || {};
        const desired = d.config.desiredState || {};
        const runner = d.status?.runner || {};
        const prep = d.status?.preparer || {};

        let variant = '';
        let repo = '';
        if (spec.prepare?.nixBuild) {
            variant = 'nixBuild';
            repo = spec.prepare.nixBuild.repo || '';
        } else if (spec.prepare?.githubRelease) {
            variant = 'githubRelease';
            repo = spec.prepare.githubRelease.repo || '';
        }

        const runnerType = spec.runner?.systemd ? 'systemd' : 'osProcess';

        return {
            id,
            name: cid.name || '',
            machine: cid.machine || '',
            environment: cid.environment || '',
            variant,
            repo,
            runnerType,
            existingStatus: runner.status || 0,
            existingVersion: runner.runningArtifact || '',
            numberOfRestarts: runner.numberOfRestarts || 0,
            lastRestartAt: runner.lastRestartAt,
            deployedBy: d.config.updatedBy || 0,
            deployedAt: d.config.updatedAt,
            deployedVersion: desired.version || '',
            prepareStatus: prep.status || 0,
            prepareVersion: desired.version || '',
            currentVersion: d.config.version || 0,
        };
    });
};

// findRawConfig finds the raw DeploymentWithStatus from deploymentsS for a given deployment ID.
const findRawConfig = (deploymentId) => {
    const all = deploymentsS.val;
    if (!Array.isArray(all)) return null;
    for (const d of all) {
        if (d.config && d.config.id === deploymentId) return d.config;
    }
    return null;
};

export function statusPage() {
    const statuses = van.state([]);
    const sidebarMode = van.state(SIDEBAR_NONE);
    const sidebarDeploymentId = van.state(null);
    const sidebarLabel = van.state('');
    const sidebarRevision = van.state(0);
    let activeSidebarAbort = null;

    // Overlay state
    const overlayDeployment = van.state(null);
    const overlayRevision = van.state(0);
    const showCreateOverlay = van.state(false);
    const groupByEnvironment = van.state(true);
    const collapsedEnvironmentGroups = van.state({});

    // Track deployments
    van.derive(() => {
        statuses.val = mapDeploymentsToView(deploymentsS.val);
    });

    const abortActiveSidebar = () => {
        if (activeSidebarAbort) {
            activeSidebarAbort.abort();
            activeSidebarAbort = null;
        }
    };

    const closeSidebar = () => {
        abortActiveSidebar();
        sidebarMode.val = SIDEBAR_NONE;
        sidebarDeploymentId.val = null;
        sidebarLabel.val = '';
    };

    const openSidebar = (deployment, mode) => {
        sidebarMode.val = mode;
        sidebarDeploymentId.val = deployment.id;
        sidebarLabel.val = formatDeploymentLabel(deployment);
        sidebarRevision.val++;
    };

    const onShowRunOutput = (deployment) => openSidebar(deployment, SIDEBAR_RUN);
    const onShowStatusHistory = (deployment) => openSidebar(deployment, SIDEBAR_STATUS_HISTORY);
    const onShowConfigHistory = (deployment) => openSidebar(deployment, SIDEBAR_CONFIG_HISTORY);
    const onShowPrepareOutput = (deployment) => openSidebar(deployment, SIDEBAR_PREPARE);

    const onUpdate = (deployment) => {
        overlayDeployment.val = deployment;
        overlayRevision.val++;
    };

    const closeOverlay = () => {
        overlayDeployment.val = null;
    };

    const toggleEnvironmentGroup = (environment) => {
        collapsedEnvironmentGroups.val = {
            ...collapsedEnvironmentGroups.val,
            [environment]: !collapsedEnvironmentGroups.val[environment],
        };
    };

    const deploymentTable = (rows, showEnvironmentColumn) => table(
        {class: "w-full text-left text-sm"},
        thead(
            tr(
                {class: "border-b border-gray-700 text-xs uppercase tracking-wide text-gray-500"},
                tableHeader("Deployment", headerTips.deployment, "py-3 pl-4 pr-4 font-medium"),
                showEnvironmentColumn ? tableHeader("Environment", headerTips.environment, "py-3 px-4 font-medium") : null,
                tableHeader("Machine", headerTips.machine, "py-3 px-4 font-medium"),
                tableHeader("Status", headerTips.status, "py-3 px-4 font-medium"),
                tableHeader("Version", headerTips.version, "py-3 px-4 font-medium"),
                tableHeader("Prepare", headerTips.prepare, "py-3 px-4 font-medium"),
                tableHeader("Restarts", headerTips.restarts, "py-3 px-4 font-medium"),
                tableHeader("Deployed", headerTips.deployed, "py-3 px-4 font-medium"),
                tableHeader("Actions", headerTips.actions, "py-3 pl-4 pr-4 font-medium text-right", true),
            ),
        ),
        tbody(
            ...rows.map(s => statusRow(
                s,
                onShowStatusHistory,
                onShowConfigHistory,
                onShowRunOutput,
                onShowPrepareOutput,
                onUpdate,
                {showEnvironment: showEnvironmentColumn},
            )),
        ),
    );

    const tableHeader = (text, tip, classes, alignRight = false) => th(
        {class: classes},
        span(
            {class: `inline-flex items-center gap-1.5 ${alignRight ? 'justify-end' : ''}`},
            text,
            infoTip(tip, alignRight),
        ),
    );

    const infoTip = (text, alignRight = false) => span(
        {class: "relative group inline-flex normal-case tracking-normal"},
        Info({class: "icon hover:text-gray-300 text-gray-600 w-3.5 h-3.5"}),
        span(
            {class: `invisible group-hover:visible opacity-0 group-hover:opacity-100 transition-opacity absolute top-full mt-1 bg-gray-900 text-white text-xs px-2 py-1 rounded w-56 z-20 ${alignRight ? 'right-0' : 'left-0'}`},
            text,
        ),
    );

    const deploymentTableCard = (rows, showEnvironmentColumn, header = null) => div(
        {class: "rounded-lg bg-surface border border-gray-700 overflow-x-auto p-2"},
        header,
        deploymentTable(rows, showEnvironmentColumn),
    );

    const mainContent = div(
        {class: "flex flex-col gap-6"},
        div(
            {class: "flex items-center justify-between"},
            h1({class: "text-xl font-bold"}, "Deployments"),
            div(
                {class: "flex items-center gap-4"},
                button({
                    class: () => `flex items-center gap-2 rounded-full border px-3 py-1.5 text-sm transition-colors cursor-pointer ${groupByEnvironment.val ? 'border-brand bg-brand/20 text-blue-200' : 'border-gray-600 bg-gray-800 text-gray-400'}`,
                    onclick: () => { groupByEnvironment.val = !groupByEnvironment.val; },
                    type: "button",
                    title: "Toggle environment grouping",
                },
                    span({class: () => `h-4 w-7 rounded-full relative transition-colors ${groupByEnvironment.val ? 'bg-brand' : 'bg-gray-600'}`},
                        span({class: () => `absolute top-0.5 h-3 w-3 rounded-full bg-white transition-all ${groupByEnvironment.val ? 'left-3.5' : 'left-0.5'}`}),
                    ),
                    span("Group by environment"),
                ),
                button({
                    class: "btn-primary text-sm py-1.5 px-4 cursor-pointer",
                    onclick: () => { showCreateOverlay.val = true; },
                }, "Add deployment"),
            ),
        ),
        () => {
            if (deploymentsStreamS.val.status !== 'connected' && statuses.val.length === 0) {
                return p({class: "text-gray-400"}, deploymentsStreamS.val.sentence);
            }

            const filtered = statuses.val;

            if (filtered.length === 0) {
                return div(
                    {class: "card"},
                    p(
                        {class: "text-gray-400"},
                        "No deployments configured. Create a deployment config first."
                    )
                );
            }

            // Sort: OPSAGENT_SYSTEM last, then by environment, name, machine,
            // and finally id so the order is fully deterministic across
            // stream snapshots and reconnects.
            const sorted = [...filtered].sort((a, b) => {
                const aSystem = a.environment === 'OPSAGENT_SYSTEM' ? 1 : 0;
                const bSystem = b.environment === 'OPSAGENT_SYSTEM' ? 1 : 0;
                return aSystem - bSystem
                    || (a.environment || '').localeCompare(b.environment || '')
                    || (a.name || '').localeCompare(b.name || '')
                    || (a.machine || '').localeCompare(b.machine || '')
                    || (a.id - b.id);
            });

            if (!groupByEnvironment.val) {
                return deploymentTableCard(sorted, true);
            }

            const groups = groupDeploymentsByEnvironment(sorted);
            const canCollapse = groups.length > 1;
            return div(
                {class: "flex flex-col gap-4"},
                ...groups.map(group => {
                    const collapsed = canCollapse && Boolean(collapsedEnvironmentGroups.val[group.environment]);
                    const header = div(
                        {class: "flex items-center justify-between px-4 pt-1 pb-2 border-b border-gray-700"},
                        div(
                            {class: "flex items-center gap-2"},
                            div({class: "text-xs font-semibold text-gray-300"}, environmentLabel(group.environment)),
                            span({class: "text-xs text-gray-500"}, `${group.rows.length} deployment${group.rows.length === 1 ? '' : 's'}`),
                        ),
                        canCollapse ? button({
                            class: "text-xs text-gray-400 hover:text-gray-200 cursor-pointer px-2 py-1 rounded hover:bg-gray-800",
                            onclick: () => toggleEnvironmentGroup(group.environment),
                            type: "button",
                            title: collapsed ? "Expand" : "Collapse",
                        }, collapsed ? "Expand" : "Collapse") : span(),
                    );
                    return div(
                        {class: "rounded-lg bg-surface border border-gray-700 overflow-x-auto p-2"},
                        header,
                        collapsed ? null : deploymentTable(group.rows, false),
                    );
                }),
            );
        }
    );

    let currentWidthPct = loadSidebarWidth();

    // Persistent DOM nodes — widths are updated directly during drag
    // so VanJS doesn't rebuild the sidebar on every mouse move.
    const mainPane = div(
        {class: "min-h-0 overflow-auto p-6 flex flex-col gap-6", style: "width:100%"},
        mainContent,
    );

    const sidebarPane = div({class: "min-h-0 h-full", style: "display:none"});

    const dividerEl = div({
        class: "w-1 cursor-col-resize bg-gray-700 hover:bg-brand transition-colors flex-shrink-0",
        style: "display:none",
        onmousedown: (e) => {
            e.preventDefault();
            const container = dividerEl.parentElement;
            const rect = container.getBoundingClientRect();
            const onMove = (me) => {
                const pct = ((rect.right - me.clientX) / rect.width) * 100;
                currentWidthPct = Math.round(Math.min(MAX_SIDEBAR_PCT, Math.max(MIN_SIDEBAR_PCT, pct)));
                mainPane.style.width = `${100 - currentWidthPct}%`;
                sidebarPane.style.width = `${currentWidthPct}%`;
            };
            const onUp = () => {
                document.removeEventListener('mousemove', onMove);
                document.removeEventListener('mouseup', onUp);
                saveSidebarWidth(currentWidthPct);
            };
            document.addEventListener('mousemove', onMove);
            document.addEventListener('mouseup', onUp);
        },
    });

    const applySidebarLayout = (open) => {
        if (open) {
            mainPane.style.width = `${100 - currentWidthPct}%`;
            sidebarPane.style.width = `${currentWidthPct}%`;
            sidebarPane.style.display = '';
            dividerEl.style.display = '';
        } else {
            mainPane.style.width = '100%';
            sidebarPane.style.display = 'none';
            dividerEl.style.display = 'none';
        }
    };

    // Reactive sidebar content — only rebuilds when mode/id/rev changes,
    // not on width changes.
    van.derive(() => {
        const mode = sidebarMode.val;
        const depId = sidebarDeploymentId.val;
        const _rev = sidebarRevision.val;

        // Clear previous sidebar content.
        sidebarPane.innerHTML = '';

        if (!mode || !depId) {
            applySidebarLayout(false);
            return;
        }

        const label = sidebarLabel.val;
        let content;
        if (mode === SIDEBAR_STATUS_HISTORY) {
            content = deploymentStatusHistory(depId, label, closeSidebar);
        } else if (mode === SIDEBAR_CONFIG_HISTORY) {
            content = deploymentConfigHistory(depId, label, closeSidebar);
        } else {
            abortActiveSidebar();
            const ac = new AbortController();
            activeSidebarAbort = ac;
            content = deploymentLogs(depId, label, mode, ac, closeSidebar);
        }

        sidebarPane.appendChild(content);
        applySidebarLayout(true);
    });

    // Overlay container — appended to body-level so it floats above everything.
    const overlayContainer = div();

    van.derive(() => {
        const dep = overlayDeployment.val;
        const _rev = overlayRevision.val;
        overlayContainer.innerHTML = '';

        if (!dep) return;

        const rawConfig = findRawConfig(dep.id);
        overlayContainer.appendChild(
            deployOverlay(dep, rawConfig, closeOverlay)
        );
    });

    // Create overlay container
    const createOverlayContainer = div();

    van.derive(() => {
        const show = showCreateOverlay.val;
        createOverlayContainer.innerHTML = '';

        if (!show) return;

        createOverlayContainer.appendChild(
            createOverlay(
                () => { showCreateOverlay.val = false; },
            )
        );
    });

    return div(
        {class: "flex h-full min-h-0 overflow-hidden"},
        mainPane,
        dividerEl,
        sidebarPane,
        overlayContainer,
        createOverlayContainer,
    );
}
