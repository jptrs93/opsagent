import van from "vanjs-core";
import {infoIcon} from "../lib/icons.js";
import {deploymentsS, deploymentsStreamS, machinesS, spacesS} from "../state/deployments.js";
import {statusRow, systemGroupStatusRow} from "../components/statusCard.js";
import {deploymentHistory} from "../components/deploymentHistory.js";
import {deployOverlay} from "../components/deployOverlay.js";
import {openDeployGroupUpdateOverlay} from "../components/openDeployGroupUpdateOverlay.js";
import {createOverlay} from "../components/createOverlay.js";
import {prepareOutputOverlay} from "../components/prepareOutputOverlay.js";
import {exportConfigOverlay} from "../components/exportConfigOverlay.js";
import {deploymentConfigOverlay} from "../components/deploymentJsonOverlay.js";
import {recentlyDeletedOverlay} from "../components/recentlyDeletedOverlay.js";
import {capi} from "../capi/index.js";
import {nodeDisplayName} from "../lib/machines.js";
import {deploymentWorkload} from "../lib/deploymentConfig.js";

const { div, h2, p, button, input, table, thead, tbody, tr, th, td, span, colgroup, col } = van.tags;

const SIDEBAR_WIDTH_KEY = 'opsagent_sidebar_width';
const SHOW_OPENDEPLOY_KEY = 'opsagent_show_opendeploy';
const DEFAULT_SIDEBAR_PCT = 50;
const MIN_SIDEBAR_PCT = 20;
const MAX_SIDEBAR_PCT = 80;
const OPENDEPLOY_SPACE_ID = 0;
const STATUS_RUNNING = 2;
const STATUS_STOPPED = 3;

const isOpenDeployDeployment = deployment => {
    const name = deployment?.name || deployment?.name || '';
    const spaceId = Number(deployment?.spaceId ?? deployment?.spaceId ?? -1);
    return spaceId === OPENDEPLOY_SPACE_ID && (name === 'opendeploy' || name === 'opendeploy-net');
};

// makeSystemGroups collapses the per-node system deployment rows into one
// merged group row per name. Members are ordered secondaries first (by node
// name), primary last — the same order the group upgrade overlay walks.
const makeSystemGroups = (rows, machines) => {
    const byName = new Map();
    const rest = [];
    for (const row of rows) {
        if (isOpenDeployDeployment(row)) {
            if (!byName.has(row.name)) byName.set(row.name, []);
            byName.get(row.name).push(row);
        } else {
            rest.push(row);
        }
    }
    const isPrimaryNode = (nodeId) => (machines || []).some(machine => machine.isPrimary && Number(machine.id) === Number(nodeId));
    // opendeploy-net first, opendeploy last, matching the pre-group sort that
    // kept the agent's own deployment at the bottom of the table.
    const groups = ['opendeploy-net', 'opendeploy'].flatMap(name => {
        const members = byName.get(name);
        if (!members?.length) return [];
        const sorted = [...members]
            .map(member => ({...member, isPrimaryNode: isPrimaryNode(member.nodeId)}))
            .sort((a, b) => (a.isPrimaryNode - b.isPrimaryNode)
                || (a.node || '').localeCompare(b.node || '')
                || (a.id - b.id));
        return [{
            isSystemGroup: true,
            id: sorted[0].id,
            name,
            spaceId: OPENDEPLOY_SPACE_ID,
            spaceName: sorted[0].spaceName,
            members: sorted,
        }];
    });
    return {rest, groups};
};

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
    const parts = [deployment.spaceName, deployment.node, deployment.name].filter(Boolean);
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
            await capi.postV1DeploymentsDelete({
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

function revertDeploymentTargetVersionOverlay(deploymentId, historyConfig, getCurrentConfig, close) {
    const saving = van.state(false);
    const error = van.state('');
    const targetVersion = deploymentWorkload(historyConfig)?.version || '';
    const historyVersion = historyConfig?.version || 0;
    const currentConfig = getCurrentConfig();
    const label = currentConfig
        ? formatDeploymentLabel({
            id: deploymentId,
            spaceName: `space ${currentConfig.spaceId ?? 0}`,
            node: currentConfig.nodeId ? nodeDisplayName(currentConfig.nodeId, machinesS.val) : '',
            name: currentConfig.name || '',
        })
        : `#${deploymentId}`;

    const confirmRevert = async () => {
        if (saving.val) return;
        error.val = '';
        if (!targetVersion) {
            error.val = 'This history entry does not contain a target version.';
            return;
        }
        const current = getCurrentConfig();
        if (!current) {
            error.val = 'Deployment no longer exists.';
            return;
        }
        saving.val = true;
        try {
            const request = {
                deploymentId,
                version: (current.version || 0) + 1,
            };
            if (current.spec?.container1Spec) {
                request.spec = structuredClone(current.spec);
                request.spec.container1Spec.version = targetVersion;
            } else {
                request.targetVersion = targetVersion;
            }
            await capi.postV1DeploymentsUpdate(request);
            close();
        } catch (e) {
            error.val = e?.message || 'Reverting target version failed.';
        } finally {
            saving.val = false;
        }
    };

    return div(
        {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4", "data-testid": "deployment-revert-target-overlay"},
        div(
            {class: "card w-full max-w-lg flex flex-col gap-4 shadow-2xl"},
            h2({class: "text-base font-semibold"}, "Revert target version"),
            p({class: "text-sm text-gray-300"}, `Revert ${label} to the target version from config v${historyVersion}?`),
            p({class: "text-xs text-gray-400"}, "This only changes the desired target version and preserves the deployment's current running or stopped state."),
            div(
                {class: "rounded-lg border border-gray-700 bg-gray-950/60 px-3 py-2 font-mono text-xs text-blue-200 break-all"},
                targetVersion || 'No target version',
            ),
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
                    class: "text-xs px-3 py-1 rounded-md font-medium bg-blue-600 text-white hover:bg-blue-500 disabled:opacity-60 cursor-pointer",
                    disabled: () => saving.val || !targetVersion,
                    onclick: confirmRevert,
                }, () => saving.val ? "Reverting..." : "Revert target"),
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
    node: 'Cluster node where this deployment is reconciled.',
    status: 'Status for each scheduled instance, oldest first. Click a runner badge to view run output.',
    version: 'Version for each scheduled instance. Uses the pinned target until the runner reports its version; orange when it differs from the latest desired version.',
    prepare: 'Latest prepare/build/download result. Click to view prepare logs.',
    restarts: 'Runner restart count and last restart time for the current deployment version.',
    deployedBy: 'User who made the latest deployment config change.',
    deployedAt: 'Timestamp of the latest deployment config change.',
    actions: 'Open the update overlay to deploy, start, or stop this deployment.',
};

const deploymentSourceView = (config) => {
    const spec = config?.spec || {};
    const container = spec.container1Spec || null;
    const source = container?.source || {};
    if (source.nixDockerBuild) {
        return {variant: 'nixDockerBuild', repo: source.nixDockerBuild.repo || ''};
    }
    if (spec.systemdSpec?.source) {
        return {variant: 'githubRelease', repo: spec.systemdSpec.source.repo || ''};
    }
    if (source.remoteImage) {
        return {variant: 'containerImage', repo: source.remoteImage.image || ''};
    }
    return {variant: '', repo: ''};
};

// mapDeploymentsToView keeps one table row per desired deployment while
// preserving an oldest-first view of every non-final scheduled instance.
const mapDeploymentsToView = (deployments, spaces, machines) => {
    if (!Array.isArray(deployments)) return [];
    const spaceNames = new Map((spaces || []).map(space => [space.id, space.name]));
    const machinesByNodeId = new Map((machines || [])
        .filter(machine => Number(machine.id || 0))
        .map(machine => [Number(machine.id), machine]));

    return deployments.filter(d => d.config && d.config.id && !d.config.deleted).map((d) => {
        const id = d.config.id; // deployment id for API actions
        const instanceId = d.instance?.id || 0;
        const identity = {spaceId: d.config.spaceId, name: d.config.name};
        const spec = d.config.spec || {};
        const workload = deploymentWorkload(d.config) || {};
        const runner = d.status?.runner || {};
        const prep = d.status?.preparer || {};
        const {variant, repo} = deploymentSourceView(d.config);

        const runnerType = spec.systemdSpec ? 'systemd' : 'container';
        const spaceId = identity.spaceId || 0;
        const nodeId = Number(d.config.nodeId || 0);
        const node = nodeDisplayName(nodeId, machines);
        const nodeMissing = Boolean(nodeId) && !machinesByNodeId.has(nodeId);
        const existingStatus = runner.status || 0;
        const uiExistingStatus = nodeMissing && existingStatus === STATUS_RUNNING ? 0 : existingStatus;
        const systemDeployment = isOpenDeployDeployment({spaceId, name: identity.name});
        const scheduledInstances = (d.scheduledInstances || []).map((state) => {
            const instance = state.instance || {};
            const instanceRunner = state.status?.runner || {};
            const instancePrep = state.status?.preparer || {};
            const pinnedVersion = deploymentWorkload(state.config)?.version || '';
            const instanceNodeId = Number(instance.nodeId || nodeId);
            const instanceNodeMissing = Boolean(instanceNodeId) && !machinesByNodeId.has(instanceNodeId);
            const instanceStatus = instanceRunner.status || 0;
            const sourceView = deploymentSourceView(state.config);
            return {
                instanceId: instance.id || 0,
                runnerPresent: Boolean(state.status?.runner),
                existingStatus: instanceNodeMissing && instanceStatus === STATUS_RUNNING ? 0 : instanceStatus,
                existingVersion: instanceRunner.runningVersion || pinnedVersion,
                deployedVersion: workload.version || '',
                preparer: instancePrep,
                // The instance's own pinned config is what its preparer worked
                // on, so a rollover shows each instance against its own version.
                prepareVersion: pinnedVersion || workload.version || '',
                targetState: instance.state || 0,
                nodeMissing: instanceNodeMissing,
                ...sourceView,
            };
        });

        return {
            id,
            instanceId,
            name: identity.name || '',
            node,
            nodeId,
            spaceId,
            spaceName: spaceNames.get(spaceId) || `space ${spaceId}`,
            variant,
            repo,
            runnerType,
            runnerPresent: Boolean(d.status?.runner),
            existingStatus: uiExistingStatus,
            canDelete: systemDeployment
                ? nodeMissing
                : uiExistingStatus === STATUS_STOPPED || (!d.instance && !workload.running) || (nodeMissing && uiExistingStatus === 0),
            nodeMissing,
            existingVersion: runner.runningVersion || '',
            numberOfRestarts: runner.numberOfRestarts || 0,
            lastRestartAt: runner.lastRestartAt,
            deployedBy: d.config.author || 0,
            deployedAt: d.config.updatedAt,
            deployedVersion: workload.version || '',
            desiredRunning: Boolean(workload.running),
            preparer: prep,
            prepareVersion: deploymentWorkload(d.pinnedConfig)?.version || workload.version || '',
            currentVersion: d.config.version || 0,
            spaceVersion: d.config.spaceVersion || 0,
            targetState: d.instance?.state || 0,
            scheduledInstances,
        };
    });
};

// findRawConfig finds the latest desired DeploymentConfig for a deployment ID.
const findRawConfig = (deploymentId) => {
    const all = deploymentsS.rawVal;
    if (!Array.isArray(all)) return null;
    for (const d of all) {
        if (d.config?.id === deploymentId) return d.config;
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
    let activeInfoTipUnpin = null;

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
        if (deployment.isSystemGroup) {
            overlayNode.val = openDeployGroupUpdateOverlay(deployment, closeOverlay);
            return;
        }
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

    const onViewConfig = (deployment) => {
        overlayNode.val = deploymentConfigOverlay(deployment, closeOverlay);
    };

    const onDelete = (deployment) => {
        if (!deployment.canDelete && deployment.existingStatus !== STATUS_STOPPED) return;
        overlayNode.val = deleteDeploymentOverlay(deployment, closeOverlay);
    };

    const onRevertHistoryTargetVersion = (deploymentId, historyConfig) => {
        overlayNode.val = revertDeploymentTargetVersionOverlay(
            deploymentId,
            historyConfig,
            () => findRawConfig(deploymentId),
            closeOverlay,
        );
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

    const openRecentlyDeletedOverlay = () => {
        overlayNode.val = recentlyDeletedOverlay(
            (config) => {
                closeOverlay();
                // The deleted deployment released its identity tuple, so the fork
                // keeps its name, space, and node prefilled. If a new deployment
                // has since claimed the tuple, create rejects it and the form
                // reports the conflict.
                createOverlayNode.val = createOverlay(closeCreateOverlay, undefined, {
                    sourceDeploymentConfig: config,
                    retainIdentity: true,
                });
            },
            closeOverlay,
        );
    };

    const statusRowNode = (deployment, showSpaceColumn) => deployment.isSystemGroup
        ? systemGroupStatusRow(
            deployment,
            {onShowHistory, onShowRunOutput, onShowPrepareOutput, onUpdate, onViewConfig, onDelete},
            {showSpace: showSpaceColumn},
        )
        : statusRow(
            deployment,
            onShowHistory,
            onShowRunOutput,
            onShowPrepareOutput,
            onUpdate,
            onFork,
            {showSpace: showSpaceColumn, onViewConfig, onDelete},
        );

    const rowSearchValues = (row) => [
        row.name,
        row.node,
        row.spaceName,
        row.repo,
        row.runnerType,
        row.existingVersion,
        row.deployedVersion,
        ...(row.scheduledInstances || []).map(instance => instance.existingVersion),
    ];

    const filterDeployments = (rows) => {
        const query = search.val.trim().toLowerCase();
        if (!query) return rows;
        return rows.filter(row => {
            const values = row.isSystemGroup
                ? [row.name, row.spaceName, ...row.members.flatMap(rowSearchValues)]
                : rowSearchValues(row);
            return values.some(value => String(value || '').toLowerCase().includes(query));
        });
    };

    const filterOpendeployDeployments = (rows) => showOpendeploy.val
        ? rows
        : rows.filter(row => row.spaceId !== OPENDEPLOY_SPACE_ID);

    const deploymentTableClass = (showSpaceColumn) => `w-full ${showSpaceColumn ? 'min-w-[80rem]' : 'min-w-[75rem]'} table-fixed text-left text-sm`;

    const deploymentColgroup = (showSpaceColumn) => colgroup(
        col({style: "width:10rem"}),
        showSpaceColumn ? col({style: "width:5.5rem"}) : '',
        col({style: "width:8rem"}),
        col({style: "width:7rem"}),
        // Version carries container image tags, which run far longer than the
        // Prepare stage names now that the version prefix moved to the tooltip.
        col({style: "width:10rem"}),
        // 8.5rem leaves 112px of text room; the widest stage name, "resolving
        // inputs", measures 102px.
        col({style: "width:8.5rem"}),
        col({style: "width:7rem"}),
        col({style: "width:7rem"}),
        col({style: "width:8rem"}),
        col({style: "width:11.5rem"}),
    );

    const deploymentTableHeader = (showSpaceColumn) => table(
        {class: deploymentTableClass(showSpaceColumn)},
        deploymentColgroup(showSpaceColumn),
        thead(
            tr(
                {class: "border-b border-gray-700 text-xs uppercase tracking-wide text-gray-500"},
                tableHeader("Deployment", headerTips.deployment, "py-3 pl-4 pr-3 font-medium"),
                showSpaceColumn ? tableHeader("Space", headerTips.space, "py-3 px-3 font-medium") : '',
                tableHeader("Node", headerTips.node, "py-3 px-3 font-medium"),
                tableHeader("Status", headerTips.status, "py-3 px-3 font-medium"),
                tableHeader("Version", headerTips.version, "py-3 px-3 font-medium"),
                tableHeader("Prepare", headerTips.prepare, "py-3 px-3 font-medium"),
                tableHeader("Restarts", headerTips.restarts, "py-3 px-3 font-medium"),
                tableHeader("Deployed by", headerTips.deployedBy, "py-3 px-3 font-medium"),
                tableHeader("Deployed at", headerTips.deployedAt, "py-3 px-3 font-medium"),
                tableHeader("Actions", headerTips.actions, "py-3 pl-3 pr-1 font-medium text-right", true),
            ),
        ),
    );

    const deploymentTableBody = (rows, showSpaceColumn) => table(
        {class: deploymentTableClass(showSpaceColumn)},
        deploymentColgroup(showSpaceColumn),
        tbody(
            ...rows.map(s => statusRowNode(s, showSpaceColumn)),
        ),
    );

    const spaceDividerRow = (space, isFirst, columnCount) => tr(
        td(
            {colSpan: columnCount, class: `${isFirst ? 'pt-2 pb-3' : 'py-3'} px-0`},
            div(
                {class: "flex items-center gap-3"},
                span({class: "text-xs font-semibold tracking-wide text-blue-300 whitespace-nowrap"}, spaceLabel(space)),
                div({class: "h-px flex-1 bg-gradient-to-r from-gray-600/80 to-transparent"}),
            ),
        ),
    );

    const groupedDeploymentTableBody = (groups) => table(
        {class: deploymentTableClass(false)},
        deploymentColgroup(false),
        tbody(
            ...groups.flatMap((group, index) => {
                const space = (spacesS.val || []).find(s => s.id === group.spaceId) || {id: group.spaceId, name: `space ${group.spaceId}`};
                return [
                    spaceDividerRow(space, index === 0, 9),
                    ...group.rows.map(row => statusRowNode(row, false)),
                ];
            }),
        ),
    );

    const tableHeader = (text, tip, classes, alignRight = false) => th(
        {class: `${classes} bg-surface`},
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
        let attachTimer = null;

        const unpin = () => {
            pinned.val = false;
            if (attachTimer) {
                clearTimeout(attachTimer);
                attachTimer = null;
            }
            if (offHandler) {
                document.removeEventListener('mousedown', offHandler);
                offHandler = null;
            }
            if (activeInfoTipUnpin === unpin) activeInfoTipUnpin = null;
        };

        const toggle = (e) => {
            e.stopPropagation();
            if (pinned.val) {
                unpin();
                return;
            }
            if (activeInfoTipUnpin) activeInfoTipUnpin();
            pinned.val = true;
            activeInfoTipUnpin = unpin;
            offHandler = () => unpin();
            // Attach on the next tick so the opening click doesn't close it.
            attachTimer = setTimeout(() => {
                attachTimer = null;
                if (pinned.val && offHandler) document.addEventListener('mousedown', offHandler);
            }, 0);
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

    // Keep both overflow elements outside the live table-body binding so status
    // updates do not replace the DOM nodes that own scrollTop and scrollLeft.
    const deploymentTableCard = (viewS) => div(
        {class: () => `w-full min-w-0 min-h-0 flex-1 rounded-lg bg-surface border border-gray-700 p-2 flex-col ${viewS.val.message ? 'hidden' : 'flex'}`},
        div(
            {class: "app-scroll-x w-full min-h-0 flex-1 overflow-x-auto overflow-y-hidden"},
            div(
                {class: "h-full min-h-0 flex flex-col"},
                div({class: "flex-none pr-1"}, () => deploymentTableHeader(!groupBySpace.val)),
                div(
                    {class: "deployment-table-scroll min-h-0 flex-1 overflow-y-auto overflow-x-hidden pr-1"},
                    () => {
                        const view = viewS.val;
                        if (view.message) return '';
                        if (!groupBySpace.val) return deploymentTableBody(view.rows, true);
                        return groupedDeploymentTableBody(groupDeploymentsBySpace(view.rows));
                    },
                ),
            ),
        ),
    );

    const deploymentToolbarButtonBase = "inline-flex items-center justify-center whitespace-nowrap rounded-lg px-3 py-1.5 " +
        "text-sm leading-5 transition-colors cursor-pointer";
    const deploymentToolbarPrimaryButtonClass = `${deploymentToolbarButtonBase} bg-brand text-white hover:brightness-110`;
    const deploymentToolbarSecondaryButtonClass = `${deploymentToolbarButtonBase} bg-gray-700 text-gray-200 hover:bg-gray-600`;

    const deploymentViewS = van.derive(() => {
        const allRows = mapDeploymentsToView(deploymentsS.val, spacesS.val, machinesS.val);
        const visibleRows = filterOpendeployDeployments(allRows);
        // The per-node system deployment rows collapse into one merged row per
        // name before search and sort, so a group matches when any member does.
        const {rest, groups} = makeSystemGroups(visibleRows, machinesS.val);
        const filtered = filterDeployments([...rest, ...groups]);

        if (deploymentsStreamS.val.status !== 'connected' && allRows.length === 0) {
            return {message: deploymentsStreamS.val.sentence, messageCard: false, rows: []};
        }
        if (allRows.length === 0) {
            return {message: 'No deployments configured. Create a deployment config first.', messageCard: true, rows: []};
        }
        if (visibleRows.length === 0) {
            return {message: 'Only _system deployments are configured. Enable Show _system to display them.', messageCard: true, rows: []};
        }
        if (filtered.length === 0) {
            return {message: 'No deployments match your search.', messageCard: true, rows: []};
        }

        // Sort: system group rows last (opendeploy-net then opendeploy, as
        // ordered by makeSystemGroups), then by space, name, node, and finally
        // id so the order is fully deterministic across stream snapshots and
        // reconnects.
        const groupRank = (row) => row.isSystemGroup ? (row.name === 'opendeploy' ? 2 : 1) : 0;
        const rows = [...filtered].sort((a, b) => (groupRank(a) - groupRank(b))
            || (a.spaceId - b.spaceId)
            || (a.name || '').localeCompare(b.name || '')
            || (a.node || '').localeCompare(b.node || '')
            || (a.id - b.id));
        return {message: '', messageCard: false, rows};
    });

    const deploymentTable = deploymentTableCard(deploymentViewS);

    const mainContent = div(
        {class: "flex flex-col gap-3 w-full min-w-0 min-h-0 flex-1"},
        div(
            {class: "flex flex-wrap items-center justify-between gap-3"},
            div(
                {class: "flex items-center gap-2"},
                input({
                    class: "text-input search-input",
                    type: "search",
                    placeholder: "Search deployments",
                    value: search,
                    oninput: (e) => search.val = e.target.value,
                }),
                button({
                    type: "button",
                    "data-testid": "recently-deleted-button",
                    class: "inline-flex items-center whitespace-nowrap rounded-md border border-gray-700 bg-gray-800 " +
                        "px-2.5 py-1 text-xs text-gray-400 transition-colors hover:bg-gray-700 hover:text-gray-200 cursor-pointer",
                    title: "Deployments deleted recently",
                    onclick: openRecentlyDeletedOverlay,
                }, "See recently deleted"),
            ),
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
                    title: "Toggle _system internal deployments",
                },
                    span({class: () => `h-4 w-7 rounded-full relative transition-colors ${showOpendeploy.val ? 'bg-brand' : 'bg-gray-600'}`},
                        span({class: () => `absolute top-0.5 h-3 w-3 rounded-full bg-white transition-all ${showOpendeploy.val ? 'left-3.5' : 'left-0.5'}`}),
                    ),
                    span("Show _system"),
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
            const view = deploymentViewS.val;
            if (!view.message) return '';
            const message = p({class: "text-gray-400"}, view.message);
            return view.messageCard ? div({class: "card"}, message) : message;
        },
        deploymentTable,
    );

    let currentWidthPct = loadSidebarWidth();

    // Persistent DOM nodes — widths are updated directly during drag
    // so VanJS doesn't rebuild the sidebar on every mouse move.
    const mainPane = div(
        {class: "min-w-0 min-h-0 overflow-hidden p-3 flex flex-col gap-6", style: "width:100%"},
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
		content = deploymentHistory(depId, label, closeSidebar, onRevertHistoryTargetVersion);

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
