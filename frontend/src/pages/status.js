import van from "vanjs-core";
import {Info} from "vanjs-feather";
import {deploymentsS, deploymentsStreamS} from "../state/deployments.js";
import {statusRow} from "../components/statusCard.js";
import {deploymentLogs} from "../components/deploymentLogs.js";
import {deploymentHistory} from "../components/deploymentHistory.js";
import {deployOverlay} from "../components/deployOverlay.js";
import {createOverlay} from "../components/createOverlay.js";

const { div, p, button, table, thead, tbody, tr, th, span } = van.tags;

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
const SIDEBAR_HISTORY = 'history';

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
    version: 'Currently running commit or GitHub release tag. Orange when it differs from the desired version.',
    prepare: 'Latest prepare/build/download result. Click to view prepare logs.',
    restarts: 'Runner restart count and last restart time for the current deployment version.',
    deployedBy: 'User who made the latest deployment config change.',
    deployedAt: 'Timestamp of the latest deployment config change.',
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
        if (spec.prepare?.nixDockerBuild) {
            variant = 'nixDockerBuild';
            repo = spec.prepare.nixDockerBuild.repo || '';
        } else if (spec.prepare?.githubRelease) {
            variant = 'githubRelease';
            repo = spec.prepare.githubRelease.repo || '';
        } else if (spec.prepare?.containerImage) {
            variant = 'containerImage';
            repo = spec.prepare.containerImage.image || '';
        }

        const runnerType = cid.environment === 'OPENDEPLOY' || spec.runner?.systemd ? 'systemd' : 'container';

        return {
            id,
            name: cid.name || '',
            machine: cid.machine || '',
            environment: cid.environment || '',
            variant,
            repo,
            runnerType,
            existingStatus: runner.status || 0,
            existingVersion: runner.runningVersion || '',
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
    const all = deploymentsS.rawVal;
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

    // Overlay nodes are created from explicit user events, not from a derive,
    // so child-local state reads inside overlay construction are not captured by
    // the page-level render path.
    const overlayNode = van.state('');
    const createOverlayNode = van.state('');
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
    const onShowHistory = (deployment) => openSidebar(deployment, SIDEBAR_HISTORY);
    const onShowPrepareOutput = (deployment) => openSidebar(deployment, SIDEBAR_PREPARE);

    const closeOverlay = () => {
        overlayNode.val = '';
    };

    const onUpdate = (deployment) => {
        const rawConfig = findRawConfig(deployment.id);
        overlayNode.val = deployOverlay(deployment, rawConfig, closeOverlay);
    };

    const closeCreateOverlay = () => {
        createOverlayNode.val = '';
    };

    const openCreateOverlay = () => {
        createOverlayNode.val = createOverlay(closeCreateOverlay);
    };

    const toggleEnvironmentGroup = (environment) => {
        collapsedEnvironmentGroups.val = {
            ...collapsedEnvironmentGroups.val,
            [environment]: !collapsedEnvironmentGroups.val[environment],
        };
    };

    const deploymentTable = (rows, showEnvironmentColumn) => table(
        {class: "min-w-full w-max text-left text-sm whitespace-nowrap"},
        thead(
            tr(
                {class: "border-b border-gray-700 text-xs uppercase tracking-wide text-gray-500"},
                tableHeader("Deployment", headerTips.deployment, "py-3 pl-4 pr-3 font-medium"),
                showEnvironmentColumn ? tableHeader("Environment", headerTips.environment, "py-3 px-3 font-medium") : '',
                tableHeader("Machine", headerTips.machine, "py-3 px-3 font-medium"),
                tableHeader("Status", headerTips.status, "py-3 px-3 font-medium"),
                tableHeader("Running Version", headerTips.version, "py-3 px-3 font-medium"),
                tableHeader("Prepare", headerTips.prepare, "py-3 px-3 font-medium"),
                tableHeader("Restarts", headerTips.restarts, "py-3 px-3 font-medium"),
                tableHeader("Deployed by", headerTips.deployedBy, "py-3 px-3 font-medium"),
                tableHeader("Deployed at", headerTips.deployedAt, "py-3 px-3 font-medium"),
                tableHeader("Actions", headerTips.actions, "py-3 pl-3 pr-4 font-medium text-right", true),
            ),
        ),
        tbody(
            ...rows.map(s => statusRow(
                s,
                onShowHistory,
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

    const infoTip = (text, alignRight = false) => {
        // pinned keeps the tooltip open after a click until the next click
        // anywhere else; hover still shows it transiently when not pinned.
        const pinned = van.state(false);
        let offHandler = null;

        const unpin = () => {
            pinned.val = false;
            if (offHandler) {
                document.removeEventListener('mousedown', offHandler);
                offHandler = null;
            }
        };

        const toggle = (e) => {
            e.stopPropagation();
            if (pinned.val) {
                unpin();
                return;
            }
            pinned.val = true;
            offHandler = () => unpin();
            // Attach on the next tick so the opening click doesn't close it.
            setTimeout(() => document.addEventListener('mousedown', offHandler), 0);
        };

        return span(
            {class: "relative group inline-flex normal-case tracking-normal"},
            Info({
                class: "icon hover:text-gray-300 text-gray-600 w-3.5 h-3.5 cursor-pointer",
                onmousedown: toggle,
            }),
            span(
                {
                    // Clicks inside the pinned tooltip shouldn't dismiss it.
                    onmousedown: (e) => { if (pinned.val) e.stopPropagation(); },
                    class: () => `${pinned.val ? 'visible opacity-100' : 'invisible opacity-0 group-hover:visible group-hover:opacity-100'} transition-opacity absolute top-full mt-1 bg-gray-900 border border-gray-700 text-white text-xs px-3 py-2 rounded-lg w-56 z-20 ${alignRight ? 'right-0' : 'left-0'}`,
                },
                text,
            ),
        );
    };

    const deploymentTableCard = (rows, showEnvironmentColumn, header = null) => div(
        {class: "w-max min-w-full rounded-lg bg-surface border border-gray-700 p-2"},
        header,
        deploymentTable(rows, showEnvironmentColumn),
    );

    const mainContent = div(
        {class: "flex flex-col gap-6 w-max min-w-full"},
        div(
            {class: "flex items-center justify-end"},
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
                    "data-testid": "add-deployment-button",
                    class: "btn-primary text-sm py-1.5 px-4 cursor-pointer",
                    onclick: openCreateOverlay,
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

            // Sort: OPENDEPLOY last, then by environment, name, machine,
            // and finally id so the order is fully deterministic across
            // stream snapshots and reconnects.
            const sorted = [...filtered].sort((a, b) => {
                const aSystem = a.environment === 'OPENDEPLOY' ? 1 : 0;
                const bSystem = b.environment === 'OPENDEPLOY' ? 1 : 0;
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
                {class: "flex flex-col gap-4 w-max min-w-full"},
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
                        {class: "w-max min-w-full rounded-lg bg-surface border border-gray-700 p-2"},
                        header,
                        collapsed ? '' : deploymentTable(group.rows, false),
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
        if (mode === SIDEBAR_HISTORY) {
            content = deploymentHistory(depId, label, closeSidebar);
        } else {
            abortActiveSidebar();
            const ac = new AbortController();
            activeSidebarAbort = ac;
            content = deploymentLogs(depId, label, mode, ac, closeSidebar);
        }

        sidebarPane.appendChild(content);
        applySidebarLayout(true);
    });

    return div(
        {class: "flex h-full min-h-0 overflow-hidden"},
        mainPane,
        dividerEl,
        sidebarPane,
        () => overlayNode.val || '',
        () => createOverlayNode.val || '',
    );
}
