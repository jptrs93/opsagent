import van from "vanjs-core";
import {infoIcon} from "../lib/icons.js";
import {deploymentsS, deploymentsStreamS, machinesS, spacesS} from "../state/deployments.js";
import {statusRow} from "../components/statusCard.js";
import {deploymentHistory} from "../components/deploymentHistory.js";
import {deployOverlay} from "../components/deployOverlay.js";
import {createOverlay} from "../components/createOverlay.js";
import {prepareOutputOverlay} from "../components/prepareOutputOverlay.js";
import {exportConfigOverlay} from "../components/exportConfigOverlay.js";
import {deploymentJsonOverlay} from "../components/deploymentJsonOverlay.js";
import {capi} from "../capi/index.js";

const { div, h2, p, button, input, table, thead, tbody, tr, th, td, span } = van.tags;

const SIDEBAR_WIDTH_KEY = 'opsagent_sidebar_width';
const SHOW_OPENDEPLOY_KEY = 'opsagent_show_opendeploy';
const DEFAULT_SIDEBAR_PCT = 50;
const MIN_SIDEBAR_PCT = 20;
const MAX_SIDEBAR_PCT = 80;
const OPENDEPLOY_SPACE_ID = 0;
const STATUS_RUNNING = 2;
const STATUS_STOPPED = 3;

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

function loadShowOpendeploy() {
    try { return localStorage.getItem(SHOW_OPENDEPLOY_KEY) === 'true'; } catch {}
    return false;
}

function saveShowOpendeploy(value) {
    try { localStorage.setItem(SHOW_OPENDEPLOY_KEY, value ? 'true' : 'false'); } catch {}
}

// Sidebar modes
const SIDEBAR_NONE = null;
const SIDEBAR_PREPARE = 'prepare';
const SIDEBAR_RUN = 'run';
const SIDEBAR_HISTORY = 'history';

const formatDeploymentLabel = (deployment) => {
    if (!deployment) return 'unknown deployment';
    const parts = [deployment.spaceName, deployment.machine, deployment.name].filter(Boolean);
    return parts.length > 0 ? parts.join(' / ') : `#${deployment.id}`;
};

function deleteDeploymentOverlay(deployment, close) {
    const saving = van.state(false);
    const error = van.state('');
    const label = formatDeploymentLabel(deployment);

    const confirmDelete = async () => {
        if (saving.val) return;
        error.val = '';
        saving.val = true;
        try {
            await capi.postV1DeploymentDelete({
                deploymentId: deployment.id,
                version: (deployment.currentVersion || 0) + 1,
            });
            close();
        } catch (e) {
            error.val = e?.message || 'Deleting deployment failed.';
        } finally {
            saving.val = false;
        }
    };

    return div(
        {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4", "data-testid": "deployment-delete-overlay"},
        div(
            {class: "card w-full max-w-md flex flex-col gap-4 shadow-2xl"},
            h2({class: "text-base font-semibold"}, "Delete deployment"),
            p({class: "text-sm text-gray-300"}, `Are you sure you want to delete ${label}? This removes it from the deployment list.`),
            () => error.val ? p({class: "text-sm text-red-400"}, error.val) : '',
            div({class: "flex items-center justify-end gap-2"},
                button({
                    type: "button",
                    class: "text-xs px-3 py-1 rounded-md font-medium bg-gray-700 text-gray-200 hover:bg-gray-600 disabled:opacity-60 cursor-pointer",
                    disabled: () => saving.val,
                    onclick: close,
                }, "Cancel"),
                button({
                    type: "button",
                    class: "text-xs px-3 py-1 rounded-md font-medium bg-red-600 text-white hover:bg-red-500 disabled:opacity-60 cursor-pointer",
                    disabled: () => saving.val,
                    onclick: confirmDelete,
                }, () => saving.val ? "Deleting..." : "Delete"),
            ),
        ),
    );
}

const spaceLabel = (space) => space?.name || `space ${space?.id ?? 0}`;

const groupDeploymentsBySpace = (deployments) => {
    const groups = new Map();
    for (const deployment of deployments) {
        const spaceId = deployment.spaceId || 0;
        if (!groups.has(spaceId)) {
            groups.set(spaceId, []);
        }
        groups.get(spaceId).push(deployment);
    }
    return [...groups.entries()]
        .sort(([a], [b]) => a - b)
        .map(([spaceId, rows]) => ({spaceId, rows}));
};

const headerTips = {
    deployment: 'Deployment name. Use history to inspect config and status changes.',
    space: 'Logical space for grouping deployments.',
    machine: 'Cluster machine where this deployment is reconciled.',
    status: 'Current runner status. Click the badge to view run output.',
    version: 'Currently running commit or GitHub release tag. Orange when it differs from the desired version.',
    prepare: 'Latest prepare/build/download result. Click to view prepare logs.',
    restarts: 'Runner restart count and last restart time for the current deployment version.',
    deployedBy: 'User who made the latest deployment config change.',
    deployedAt: 'Timestamp of the latest deployment config change.',
    actions: 'Open the update overlay to deploy, start, or stop this deployment.',
};

// mapDeploymentsToView flattens DeploymentWithStatus[] into the shape
// the status card component expects.
const mapDeploymentsToView = (deployments, spaces, machines) => {
    if (!Array.isArray(deployments)) return [];
    const spaceNames = new Map((spaces || []).map(space => [space.id, space.name]));
    const machineNames = new Set((machines || []).map(machine => machine.name).filter(Boolean));

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

        const runnerType = spec.runner?.systemd ? 'systemd' : 'container';
        const spaceId = cid.spaceId || 0;
        const machine = cid.machine || '';
        const machineMissing = Boolean(machine) && !machineNames.has(machine);
        const existingStatus = runner.status || 0;
        const uiExistingStatus = machineMissing && existingStatus === STATUS_RUNNING ? 0 : existingStatus;

        return {
            id,
            name: cid.name || '',
            machine,
            spaceId,
            spaceName: spaceNames.get(spaceId) || `space ${spaceId}`,
            variant,
            repo,
            runnerType,
            existingStatus: uiExistingStatus,
            canDelete: uiExistingStatus === STATUS_STOPPED || (machineMissing && uiExistingStatus === 0),
            machineMissing,
            existingVersion: runner.runningVersion || '',
            numberOfRestarts: runner.numberOfRestarts || 0,
            lastRestartAt: runner.lastRestartAt,
            deployedBy: d.config.updatedBy || 0,
            deployedAt: d.config.updatedAt,
            deployedVersion: desired.version || '',
            desiredRunning: Boolean(desired.running),
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

export function statusPage(onOpenLogs = () => {}) {
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
    const groupBySpace = van.state(true);
    const showOpendeploy = van.state(loadShowOpendeploy());
    const search = van.state('');

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

    const onShowRunOutput = (deployment) => onOpenLogs(deployment.id);
    const onShowHistory = (deployment) => openSidebar(deployment, SIDEBAR_HISTORY);
    const onShowPrepareOutput = (deployment) => {
        overlayNode.val = prepareOutputOverlay(deployment.id, formatDeploymentLabel(deployment), closeOverlay);
    };

    const closeOverlay = () => {
        overlayNode.val = '';
    };

    const onUpdate = (deployment) => {
        const rawConfig = findRawConfig(deployment.id);
        overlayNode.val = deployOverlay(deployment, rawConfig, closeOverlay);
    };

    const onFork = (deployment) => {
        const rawConfig = findRawConfig(deployment.id);
        if (!rawConfig) return;
        createOverlayNode.val = createOverlay(closeCreateOverlay, undefined, {
            sourceDeployment: deployment,
            sourceDeploymentConfig: rawConfig,
        });
    };

    const onViewJson = (deployment) => {
        overlayNode.val = deploymentJsonOverlay(deployment.id, formatDeploymentLabel(deployment), closeOverlay);
    };

    const onDelete = (deployment) => {
        if (deployment.existingStatus !== STATUS_STOPPED) return;
        overlayNode.val = deleteDeploymentOverlay(deployment, closeOverlay);
    };

    const closeCreateOverlay = () => {
        createOverlayNode.val = '';
    };

    const openCreateOverlay = () => {
        createOverlayNode.val = createOverlay(closeCreateOverlay);
    };

    const openExportOverlay = () => {
        overlayNode.val = exportConfigOverlay(closeOverlay);
    };

    const statusRowNode = (deployment, showSpaceColumn) => statusRow(
        deployment,
        onShowHistory,
        onShowRunOutput,
        onShowPrepareOutput,
        onUpdate,
        onFork,
        {showSpace: showSpaceColumn, onViewJson, onDelete},
    );

    const filterDeployments = (rows) => {
        const query = search.val.trim().toLowerCase();
        if (!query) return rows;
        return rows.filter(row => [
            row.name,
            row.machine,
            row.spaceName,
            row.repo,
            row.runnerType,
            row.existingVersion,
            row.deployedVersion,
        ].some(value => String(value || '').toLowerCase().includes(query)));
    };

    const filterOpendeployDeployments = (rows) => showOpendeploy.val
        ? rows
        : rows.filter(row => row.spaceId !== OPENDEPLOY_SPACE_ID);

    const deploymentTable = (rows, showSpaceColumn) => table(
        {class: "w-full text-left text-sm"},
        thead(
            tr(
                {class: "border-b border-gray-700 text-xs uppercase tracking-wide text-gray-500"},
                tableHeader("Deployment", headerTips.deployment, "py-3 pl-4 pr-3 font-medium"),
                showSpaceColumn ? tableHeader("Space", headerTips.space, "py-3 px-3 font-medium") : '',
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
            ...rows.map(s => statusRowNode(s, showSpaceColumn)),
        ),
    );

    const spaceDividerRow = (space, isFirst) => tr(
        td(
            {colSpan: 9, class: `${isFirst ? 'pt-3 pb-4' : 'py-4'} px-0`},
            div(
                {class: "flex items-center gap-3"},
                span({class: "text-xs font-semibold tracking-wide text-blue-300 whitespace-nowrap"}, spaceLabel(space)),
                div({class: "h-px flex-1 bg-gradient-to-r from-gray-600/80 to-transparent"}),
            ),
        ),
    );

    const groupedDeploymentTable = (groups) => table(
        {class: "w-full text-left text-sm"},
        thead(
            tr(
                {class: "border-b border-gray-700 text-xs uppercase tracking-wide text-gray-500"},
                tableHeader("Deployment", headerTips.deployment, "py-3 pl-4 pr-3 font-medium"),
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
            ...groups.flatMap((group, index) => {
                const space = (spacesS.val || []).find(s => s.id === group.spaceId) || {id: group.spaceId, name: `space ${group.spaceId}`};
                return [
                    spaceDividerRow(space, index === 0),
                    ...group.rows.map(row => statusRowNode(row, false)),
                ];
            }),
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
            infoIcon({
                class: "icon hover:text-gray-300 text-gray-600 w-3.5 h-3.5 cursor-pointer",
                onmousedown: toggle,
            }),
            span(
                {
                    // Clicks inside the pinned tooltip shouldn't dismiss it.
                    onmousedown: (e) => { if (pinned.val) e.stopPropagation(); },
                    class: () => `${pinned.val ? 'block opacity-100' : 'hidden opacity-0 group-hover:block group-hover:opacity-100'} transition-opacity absolute top-full mt-1 bg-gray-900 border border-gray-700 text-white text-xs px-3 py-2 rounded-lg w-56 z-20 ${alignRight ? 'right-0' : 'left-0'}`,
                },
                text,
            ),
        );
    };

    const deploymentTableCard = (tableNode) => div(
        {class: "w-full min-w-0 rounded-lg bg-surface border border-gray-700 p-2"},
        div({class: "w-full overflow-x-auto overflow-y-hidden"}, tableNode),
    );

    const deploymentToolbarButtonBase = "inline-flex items-center justify-center whitespace-nowrap rounded-lg px-3 py-1.5 " +
        "text-sm leading-5 transition-colors cursor-pointer";
    const deploymentToolbarPrimaryButtonClass = `${deploymentToolbarButtonBase} bg-brand text-white hover:brightness-110`;
    const deploymentToolbarSecondaryButtonClass = `${deploymentToolbarButtonBase} bg-gray-700 text-gray-200 hover:bg-gray-600`;

    const mainContent = div(
        {class: "flex flex-col gap-3 w-full min-w-0"},
        div(
            {class: "flex flex-wrap items-center justify-between gap-3"},
            input({
                class: "text-input search-input",
                type: "search",
                placeholder: "Search deployments",
                value: search,
                oninput: (e) => search.val = e.target.value,
            }),
            div(
                {class: "flex items-center gap-4"},
                button({
                    class: () => `flex items-center gap-2 rounded-full border px-3 py-1.5 text-sm transition-colors cursor-pointer ${groupBySpace.val ? 'border-brand bg-brand/20 text-blue-200' : 'border-gray-600 bg-gray-800 text-gray-400'}`,
                    onclick: () => { groupBySpace.val = !groupBySpace.val; },
                    type: "button",
                    "aria-pressed": () => groupBySpace.val ? "true" : "false",
                    title: "Toggle space grouping",
                },
                    span({class: () => `h-4 w-7 rounded-full relative transition-colors ${groupBySpace.val ? 'bg-brand' : 'bg-gray-600'}`},
                        span({class: () => `absolute top-0.5 h-3 w-3 rounded-full bg-white transition-all ${groupBySpace.val ? 'left-3.5' : 'left-0.5'}`}),
                    ),
                    span("Group by space"),
                ),
                button({
                    class: () => `flex items-center gap-2 rounded-full border px-3 py-1.5 text-sm transition-colors cursor-pointer ${showOpendeploy.val ? 'border-brand bg-brand/20 text-blue-200' : 'border-gray-600 bg-gray-800 text-gray-400'}`,
                    onclick: () => {
                        showOpendeploy.val = !showOpendeploy.val;
                        saveShowOpendeploy(showOpendeploy.val);
                    },
                    type: "button",
                    "aria-pressed": () => showOpendeploy.val ? "true" : "false",
                    title: "Toggle opendeploy internal deployments",
                },
                    span({class: () => `h-4 w-7 rounded-full relative transition-colors ${showOpendeploy.val ? 'bg-brand' : 'bg-gray-600'}`},
                        span({class: () => `absolute top-0.5 h-3 w-3 rounded-full bg-white transition-all ${showOpendeploy.val ? 'left-3.5' : 'left-0.5'}`}),
                    ),
                    span("Show opendeploy"),
                ),
                button({
                    "data-testid": "add-deployment-button",
                    class: deploymentToolbarPrimaryButtonClass,
                    onclick: openCreateOverlay,
                }, "Add deployment"),
                button({
                    type: "button",
                    class: deploymentToolbarSecondaryButtonClass,
                    onclick: openExportOverlay,
                }, "Export"),
            ),
        ),
        () => {
            const allRows = mapDeploymentsToView(deploymentsS.val, spacesS.val, machinesS.val);
            const visibleRows = filterOpendeployDeployments(allRows);
            const filtered = filterDeployments(visibleRows);

            if (deploymentsStreamS.val.status !== 'connected' && allRows.length === 0) {
                return p({class: "text-gray-400"}, deploymentsStreamS.val.sentence);
            }

            if (allRows.length === 0) {
                return div(
                    {class: "card"},
                    p(
                        {class: "text-gray-400"},
                        "No deployments configured. Create a deployment config first."
                    )
                );
            }

            if (visibleRows.length === 0) {
                return div(
                    {class: "card"},
                    p({class: "text-gray-400"}, "Only opendeploy deployments are configured. Enable Show opendeploy to display them."),
                );
            }

            if (filtered.length === 0) {
                return div(
                    {class: "card"},
                    p({class: "text-gray-400"}, "No deployments match your search.")
                );
            }

            // Sort: system deployment last, then by space, name, machine,
            // and finally id so the order is fully deterministic across
            // stream snapshots and reconnects.
            const sorted = [...filtered].sort((a, b) => {
                const aSystem = a.runnerType === 'systemd' && a.name === 'opendeploy' ? 1 : 0;
                const bSystem = b.runnerType === 'systemd' && b.name === 'opendeploy' ? 1 : 0;
                return aSystem - bSystem
                    || (a.spaceId - b.spaceId)
                    || (a.name || '').localeCompare(b.name || '')
                    || (a.machine || '').localeCompare(b.machine || '')
                    || (a.id - b.id);
            });

            if (!groupBySpace.val) {
                return deploymentTableCard(deploymentTable(sorted, true));
            }

            const groups = groupDeploymentsBySpace(sorted);
            return deploymentTableCard(groupedDeploymentTable(groups));
        }
    );

    let currentWidthPct = loadSidebarWidth();

    // Persistent DOM nodes — widths are updated directly during drag
    // so VanJS doesn't rebuild the sidebar on every mouse move.
    const mainPane = div(
        {class: "min-w-0 min-h-0 overflow-y-auto overflow-x-hidden p-3 flex flex-col gap-6", style: "width:100%"},
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
		if (mode !== SIDEBAR_HISTORY) {
			applySidebarLayout(false);
			return;
		}
		content = deploymentHistory(depId, label, closeSidebar);

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
