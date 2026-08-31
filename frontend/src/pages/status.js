import van from "vanjs-core";
import {
    caretRightIcon, checkIcon, chevronDownIcon, closeIcon, copyIcon, editIcon,
    plusIcon, searchIcon,
} from "../lib/icons.js";
import {deploymentsS, deploymentsStreamS, machinesS, spacesS} from "../state/deployments.js";
import {deploymentHistoryPanel} from "../components/deploymentHistory.js";
import {deploymentNetworkPolicies} from "../components/networkPolicySummary.js";
import {deployOverlay} from "../components/deployOverlay.js";
import {openDeployGroupUpdateOverlay} from "../components/openDeployGroupUpdateOverlay.js";
import {createOverlay} from "../components/createOverlay.js";
import {prepareOutputOverlay} from "../components/prepareOutputOverlay.js";
import {runReportOverlay} from "../components/runReportOverlay.js";
import {exportConfigOverlay} from "../components/exportConfigOverlay.js";
import {deploymentOverlay} from "../components/deploymentJsonOverlay.js";
import {recentlyDeletedOverlay} from "../components/recentlyDeletedOverlay.js";
import {capi} from "../capi/index.js";
import {nodeDisplayName} from "../lib/machines.js";
import {containerWorkload, deploymentWorkload} from "../lib/deployment.js";
import {deploymentUsages} from "../lib/referenceUsage.js";
import {resolveUserDisplayName} from "../lib/users.js";
import {preparerPhase} from "../lib/preparerStatus.js";
import {formatDateTime} from "../lib/date.js";
import {spaceHue} from "../lib/valueExplorer.js";

const { div, h2, p, button, input, label: labelTag, a, table, thead, tbody, tr, th, td, span, colgroup, col } = van.tags;

const INSPECTOR_WIDTH_KEY = 'opsagent_status_inspector_width';
const HIDDEN_SPACES_KEY = 'opsagent_status_hidden_spaces';
const SHOW_OPENDEPLOY_KEY = 'opsagent_show_opendeploy';
const OPENDEPLOY_SPACE_ID = 0;
const STATUS_NO_DEPLOYMENT = 1;
const STATUS_RUNNING = 2;
const STATUS_STOPPED = 3;
const STATUS_STARTING = 4;
const openDeployRepo = 'github.com/jptrs93/opsagent';

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

function loadInspectorWidth() {
    try {
        const v = parseFloat(localStorage.getItem(INSPECTOR_WIDTH_KEY));
        if (v >= 340 && v <= 760) return v;
    } catch {}
    return 448;
}

function saveInspectorWidth(px) {
    try { localStorage.setItem(INSPECTOR_WIDTH_KEY, String(px)); } catch {}
}

function loadHiddenSpaces() {
    try {
        const raw = localStorage.getItem(HIDDEN_SPACES_KEY);
        if (raw) return new Set(JSON.parse(raw).map(Number));
        if (localStorage.getItem(SHOW_OPENDEPLOY_KEY) === 'true') return new Set();
    } catch {}
    return new Set([OPENDEPLOY_SPACE_ID]);
}

function saveHiddenSpaces(set) {
    try { localStorage.setItem(HIDDEN_SPACES_KEY, JSON.stringify([...set])); } catch {}
}

const formatDeploymentLabel = (deploymentRow) => {
    if (!deploymentRow) return 'unknown deployment';
    const parts = [deploymentRow.spaceName, deploymentRow.node, deploymentRow.name].filter(Boolean);
    return parts.length > 0 ? parts.join(' / ') : `#${deploymentRow.id}`;
};

// addressReferrers lists deployments whose env vars resolve the address of
// deploymentId — the same references the server refuses deletion over. Env
// address refs are fully visible client-side, so the dialog can warn before
// the delete is even attempted.
const addressReferrers = (deploymentId) =>
    deploymentUsages(deploymentsS.val, spacesS.val, machinesS.val, (candidate) => {
        const envVars = containerWorkload(candidate?.config)?.runtime?.envVars || {};
        return Object.values(envVars).some(
            (value) => Number(value?.addressDeploymentId || 0) === Number(deploymentId),
        );
    });

function deleteDeploymentOverlay(deploymentRow, close) {
    const saving = van.state(false);
    const error = van.state('');
    const label = formatDeploymentLabel(deploymentRow);
    const referrers = addressReferrers(deploymentRow.id);

    const confirmDelete = async () => {
        if (saving.val) return;
        error.val = '';
        saving.val = true;
        try {
            await capi.postV1DeploymentsDelete({
                deploymentId: deploymentRow.id,
                version: (deploymentRow.currentVersion || 0) + 1,
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
            referrers.length === 0 ? '' : div(
                {class: "text-sm text-amber-400 flex flex-col gap-1", "data-testid": "deployment-delete-referrers"},
                p(`Deletion will fail: its address is referenced by ${referrers.length === 1 ? 'this deployment' : 'these deployments'}:`),
                ...referrers.map((ref) => p({class: "pl-4"}, `${ref.space} / ${ref.node} / ${ref.name}`)),
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

const deploymentSourceView = (config) => {
    const spec = config?.spec || {};
    const container = spec.container1Spec || null;
    const source = container?.source || {};
    if (source.nixDockerBuild) {
        return {variant: 'nixDockerBuild', repo: source.nixDockerBuild.repo || ''};
    }
    if (spec.opendeploySpec) {
        return {variant: 'githubRelease', repo: openDeployRepo};
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
        const id = d.config.id;
        const instanceId = d.instance?.id || 0;
        const identity = {spaceId: d.config.spaceId, name: d.config.name};
        const spec = d.config.spec || {};
        const workload = deploymentWorkload(d.config) || {};
        const runner = d.status?.runner || {};
        const prep = d.status?.preparer || {};
        const {variant, repo} = deploymentSourceView(d.config);

        const runnerType = spec.opendeploySpec ? 'opendeploy' : 'container';
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
                deploymentVersion: instance.deploymentVersion || 0,
                node: nodeDisplayName(instanceNodeId, machines),
                runnerPresent: Boolean(state.status?.runner),
                existingStatus: instanceNodeMissing && instanceStatus === STATUS_RUNNING ? 0 : instanceStatus,
                existingVersion: instanceRunner.runningVersion || pinnedVersion,
                deployedVersion: workload.version || '',
                numberOfRestarts: instanceRunner.numberOfRestarts || 0,
                lastRestartAt: instanceRunner.lastRestartAt,
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
            createdAt: d.config.createdAt,
            hasNetworking: Boolean(spec.networking),
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

const findRawConfig = (deploymentId) => {
    const all = deploymentsS.rawVal;
    if (!Array.isArray(all)) return null;
    for (const d of all) {
        if (d.config?.id === deploymentId) return d.config;
    }
    return null;
};

const shortVersion = (v) => (v.length > 7 && /^[0-9a-f]+$/i.test(v) ? v.slice(0, 7) : v);

const dnsLabel = (s) => {
    const cleaned = String(s || '').trim().toLowerCase().replaceAll('_', '-')
        .replace(/[^a-z0-9-]/g, '').replace(/-+/g, '-').replace(/^-+|-+$/g, '');
    return cleaned || 'deployment';
};
const deploymentDnsName = (name, spaceId) => `${dnsLabel(name)}.space-${spaceId}.internal`;

const STATUS_ORDER = ['Running', 'Starting', 'Preparing', 'Stopped', 'Crashed', 'Prepare failed', 'Unknown', 'No Deployment', 'No existing deployment'];
const STATUS_STYLE = {
    'Running': {dot: 'bg-green-500', text: 'text-green-300'},
    'Starting': {dot: 'bg-yellow-500', text: 'text-yellow-300'},
    'Preparing': {dot: 'bg-blue-500', text: 'text-blue-300'},
    'Stopped': {dot: 'bg-gray-500', text: 'text-gray-400'},
    'Crashed': {dot: 'bg-red-500', text: 'text-red-300'},
    'Prepare failed': {dot: 'bg-red-500', text: 'text-red-300'},
    'Unknown': {dot: 'bg-gray-500', text: 'text-gray-400'},
    'No Deployment': {dot: 'bg-gray-600', text: 'text-gray-400'},
    'No existing deployment': {dot: 'bg-gray-600', text: 'text-gray-500'},
};
const existingStatusNames = {0: 'Unknown', 1: 'No Deployment', 2: 'Running', 3: 'Stopped', 4: 'Starting', 5: 'Crashed'};
const preRunnerNames = {progress: 'Preparing', ready: 'Starting', failed: 'Prepare failed'};
const PREPARE_TEXT = {ready: 'text-green-300', progress: 'text-blue-300', failed: 'text-red-300'};

const instanceStatusView = (member, instance) => {
    const hasExisting = instance.existingStatus !== STATUS_NO_DEPLOYMENT;
    const preRunner = !instance.runnerPresent ? preparerPhase(instance.preparer) : null;
    const nodeMissing = instance.nodeMissing ?? member.nodeMissing;
    let label;
    if (preRunner) label = preRunnerNames[preRunner.tone];
    else if (!hasExisting) label = 'No existing deployment';
    else if (nodeMissing && instance.existingStatus === 0) label = 'Unknown';
    else label = existingStatusNames[instance.existingStatus] || 'Unknown';
    return {
        label,
        style: STATUS_STYLE[label] || STATUS_STYLE.Unknown,
        hasRunOutput: hasExisting && instance.runnerPresent,
    };
};

const subInstances = (row) => row.isSystemGroup
    ? row.members.flatMap((member) => {
        const instances = member.scheduledInstances?.length > 0 ? member.scheduledInstances : [member];
        return instances.map((instance, index) => ({
            member,
            instance,
            node: member.node,
            isPrimaryNode: Boolean(member.isPrimaryNode),
            testSuffix: `${row.name}-${member.node}${instances.length > 1 ? `-${instance.instanceId || index + 1}` : ''}`,
        }));
    })
    : (row.scheduledInstances?.length > 0 ? row.scheduledInstances : [row]).map((instance, index) => ({
        member: row,
        instance,
        node: instance.node || row.node,
        isPrimaryNode: false,
        testSuffix: `${row.name}-${instance.instanceId || index + 1}`,
    }));

const groupSubs = (subs, keyFn) => {
    const out = [];
    const index = new Map();
    for (const sub of subs) {
        const key = keyFn(sub);
        if (!index.has(key)) {
            index.set(key, out.length);
            out.push({key, count: 0, first: sub});
        }
        out[index.get(key)].count++;
    }
    return out;
};

const versionHref = (member, instance) => {
    const version = instance.existingVersion || '';
    if (!version) return '';
    const variant = instance.variant || member.variant;
    const repo = instance.repo || member.repo;
    if (variant === 'nixDockerBuild' && repo) return `https://${repo}/commit/${version}`;
    const releaseRepo = variant === 'githubRelease'
        ? repo
        : member.spaceId === 0 && member.name === 'opendeploy-net' ? openDeployRepo : '';
    if (releaseRepo) return `https://${releaseRepo}/releases/tag/${version}`;
    return '';
};

const versionNode = (member, instance, text, extraClass = '') => {
    const version = instance.existingVersion || '';
    if (!version) return span({class: `text-gray-500 ${extraClass}`}, 'none');
    const desired = instance.deployedVersion || member.deployedVersion || '';
    const mismatched = Boolean(desired) && version !== desired;
    const color = mismatched ? 'text-orange-400' : 'text-gray-300';
    const title = mismatched ? `Instance ${version}; desired ${desired}` : version;
    const href = versionHref(member, instance);
    const cls = `font-mono ${color} truncate min-w-0 ${extraClass}`;
    if (href) {
        return a({
            class: `${cls} underline hover:text-white`,
            href,
            target: "_blank",
            rel: "noopener noreferrer",
            title,
            onclick: (e) => e.stopPropagation(),
        }, text);
    }
    return span({class: cls, title}, text);
};

const preparePhaseView = (instance) => preparerPhase(instance.preparer);

const spaceLabelOf = (space) => space?.name || `space ${space?.id ?? 0}`;

const UI_CHAR_PX = 6.7;
const MONO_CHAR_PX = 6.4;
const SEP_PX = 12;
const DOT_PX = 10;
const CELL_PAD_PX = 22;

const COLUMN_DEFS = [
    {key: "deployment", label: "Deployment", width: 160, min: 100},
    {key: "nodes", label: "Nodes", width: 120, min: 70},
    {key: "status", label: "Status", width: 200, min: 90},
    {key: "version", label: "Version", width: 192, min: 90},
    {key: "prepare", label: "Prepare", width: 176, min: 90},
    {key: "restarts", label: "Restarts", width: 128, min: 60},
    {key: "deployedBy", label: "Deployed by", width: 96, min: 60},
    {key: "deployedAt", label: "Deployed at", width: 152, min: 90},
    {key: "edit", label: "", width: 42, min: 42},
];

const INSPECTOR_MIN = 340;
const INSPECTOR_MAX = 760;

const thBase = "sticky top-0 z-[1] bg-surface px-2 py-1.5 text-left text-[10.5px] font-semibold uppercase " +
    "tracking-wider whitespace-nowrap text-gray-500 shadow-[inset_0_-1px_0_#374151]";
const tdBase = "border-b border-gray-800/60 border-r border-r-gray-800/30 last:border-r-0 px-2 py-[3px] " +
    "align-middle overflow-hidden text-[13px]";

const miniTh = (text, extra = "") => th(
    {class: `border-b border-gray-700/70 border-r border-r-gray-800/40 last:border-r-0 bg-gray-950/40 px-2 py-1 text-left text-[10px] font-medium uppercase tracking-wide text-gray-500 ${extra}`},
    text);
const miniTd = (attrs, ...children) => td(
    {...attrs, class: `border-b border-gray-800/50 border-r border-r-gray-800/30 last:border-r-0 px-2 py-[3px] whitespace-nowrap overflow-hidden ${attrs.class || ''}`},
    ...children);

export function statusPage(onOpenLogs = () => {}) {
    const overlayNode = van.state('');
    const createOverlayNode = van.state('');
    const search = van.state('');
    const selectedRowId = van.state(null);
    const inspectorTab = van.state('details');
    const historyMemberId = van.state(0);
    const hiddenSpaces = van.state(loadHiddenSpaces());
    const collapsedSpaces = van.state(new Set());
    const openMenu = van.state(null);
    const inspectorWidth = van.state(loadInspectorWidth());
    const colWidths = van.state(Object.fromEntries(COLUMN_DEFS.map((c) => [c.key, c.width])));

    let historyCache = {key: '', node: null};

    const select = (rowId) => {
        if (selectedRowId.val !== rowId) {
            inspectorTab.val = 'details';
            historyMemberId.val = 0;
            historyCache = {key: '', node: null};
        }
        selectedRowId.val = rowId;
    };

    const closeInspector = () => { selectedRowId.val = null; };

    const closeOverlay = () => { overlayNode.val = ''; };
    const closeCreateOverlay = () => { createOverlayNode.val = ''; };

    const onShowRunOutput = (deploymentRow) => onOpenLogs(deploymentRow.id);
    const onShowPrepareOutput = (deploymentRow) => {
        overlayNode.val = prepareOutputOverlay(deploymentRow.id, formatDeploymentLabel(deploymentRow), closeOverlay);
    };

    const onShowRunReport = (member, instance) => {
        const runCount = (instance.numberOfRestarts || 0) + 1;
        const inProgress = instance.existingStatus === STATUS_RUNNING || instance.existingStatus === STATUS_STARTING;
        const run = inProgress && runCount > 1 ? runCount - 1 : runCount;
        overlayNode.val = runReportOverlay({
            deploymentId: member.id,
            version: instance.deploymentVersion || member.currentVersion || 0,
            preselect: instance.instanceId ? {instanceId: instance.instanceId, run} : null,
        }, closeOverlay);
    };

    const onUpdate = (deploymentRow) => {
        if (deploymentRow.isSystemGroup) {
            overlayNode.val = openDeployGroupUpdateOverlay(deploymentRow, closeOverlay);
            return;
        }
        const rawConfig = findRawConfig(deploymentRow.id);
        overlayNode.val = deployOverlay(deploymentRow, rawConfig, closeOverlay);
    };

    const onFork = (deploymentRow) => {
        const rawConfig = findRawConfig(deploymentRow.id);
        if (!rawConfig) return;
        createOverlayNode.val = createOverlay(closeCreateOverlay, undefined, {
            sourceDeploymentRow: deploymentRow,
            sourceDeployment: rawConfig,
        });
    };

    const onViewConfig = (deploymentRow) => {
        overlayNode.val = deploymentOverlay(deploymentRow, closeOverlay);
    };

    const onDelete = (deploymentRow) => {
        if (!deploymentRow.canDelete && deploymentRow.existingStatus !== STATUS_STOPPED) return;
        overlayNode.val = deleteDeploymentOverlay(deploymentRow, closeOverlay);
    };

    const onRevertHistoryTargetVersion = (deploymentId, historyConfig) => {
        overlayNode.val = revertDeploymentTargetVersionOverlay(
            deploymentId,
            historyConfig,
            () => findRawConfig(deploymentId),
            closeOverlay,
        );
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
                    sourceDeployment: config,
                    retainIdentity: true,
                });
            },
            closeOverlay,
        );
    };

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

    const deploymentViewS = van.derive(() => {
        const allRows = mapDeploymentsToView(deploymentsS.val, spacesS.val, machinesS.val);
        const visibleRows = allRows.filter(row => !hiddenSpaces.val.has(Number(row.spaceId)));
        const {rest, groups} = makeSystemGroups(visibleRows, machinesS.val);
        const filtered = filterDeployments([...rest, ...groups]);

        if (deploymentsStreamS.val.status !== 'connected' && allRows.length === 0) {
            return {message: deploymentsStreamS.val.sentence, rows: []};
        }
        if (allRows.length === 0) {
            return {message: 'No deployments configured. Create a deployment config first.', rows: []};
        }
        if (visibleRows.length === 0) {
            return {message: 'No deployments in the selected spaces. Adjust the spaces filter to display them.', rows: []};
        }
        if (filtered.length === 0) {
            return {message: 'No deployments match your search.', rows: []};
        }

        const groupRank = (row) => row.isSystemGroup ? (row.name === 'opendeploy' ? 2 : 1) : 0;
        const rows = [...filtered].sort((a, b) => (a.spaceId === 0) - (b.spaceId === 0)
            || (a.spaceId - b.spaceId)
            || (groupRank(a) - groupRank(b))
            || (a.name || '').localeCompare(b.name || '')
            || (a.node || '').localeCompare(b.node || '')
            || (a.id - b.id));
        return {message: '', rows};
    });

    const orderedSpaces = () => [...(spacesS.val || [])]
        .sort((a, b) => ((a.id === 0) - (b.id === 0)) || (a.id - b.id));

    const visibleSpaces = () => orderedSpaces().filter((s) => !hiddenSpaces.val.has(Number(s.id)));
    const spacesDirty = () => !(hiddenSpaces.val.size === 1 && hiddenSpaces.val.has(OPENDEPLOY_SPACE_ID));

    const spaceDot = (spaceId) => span({
        class: "inline-block w-[7px] h-[7px] rounded-full flex-none",
        style: `background:${spaceHue(spaceId)}`,
    });

    const toggleCollapsed = (spaceId) => {
        const next = new Set(collapsedSpaces.val);
        next.has(spaceId) ? next.delete(spaceId) : next.add(spaceId);
        collapsedSpaces.val = next;
    };

    const usablePx = (key) => (colWidths.rawVal[key] ?? 0) - CELL_PAD_PX;

    const oneLineFits = (labels, charPx, perGroupPx, usable) =>
        labels.reduce((w, l) => w + l.length * charPx + perGroupPx, 0)
            + (labels.length - 1) * SEP_PX <= usable;

    const countLabel = (group, total) => (total > 1 ? `${group.count} ${group.key}` : group.key);

    const oneLineCell = (parts, title) => div(
        {class: "flex items-center gap-1 overflow-hidden whitespace-nowrap", title},
        ...parts.flatMap((part, i) => [i > 0 ? span({class: "text-gray-600"}, "·") : "", ...part]));

    const stackedCell = (lines) => div({class: "flex flex-col gap-px py-px"}, ...lines);

    const statusCell = (row) => {
        const subs = subInstances(row);
        const total = subs.length;
        const groups = groupSubs(subs, (s) => instanceStatusView(s.member, s.instance).label)
            .sort((a, b) => STATUS_ORDER.indexOf(a.key) - STATUS_ORDER.indexOf(b.key));
        const labels = groups.map((g) => countLabel(g, total));
        const groupSpan = (g, i, stacked) => {
            const view = instanceStatusView(g.first.member, g.first.instance);
            const attrs = {
                class: `flex items-center ${stacked ? 'gap-1.5' : 'gap-1'} min-w-0 ${view.style.text}` +
                    (view.hasRunOutput ? ' cursor-pointer hover:brightness-125' : ''),
            };
            if (!row.isSystemGroup && total === 1) attrs["data-testid"] = `deployment-runner-status-${row.name}`;
            if (view.hasRunOutput) {
                attrs.title = "View run output";
                attrs.onclick = (e) => { e.stopPropagation(); onShowRunOutput(g.first.member); };
            }
            return span(attrs,
                span({class: `inline-block w-1.5 h-1.5 rounded-full flex-none ${view.style.dot}`}),
                span({class: "truncate"}, labels[i]));
        };
        if (oneLineFits(labels, UI_CHAR_PX, DOT_PX, usablePx("status"))) {
            return oneLineCell(groups.map((g, i) => [groupSpan(g, i, false)]), labels.join(" · "));
        }
        return stackedCell(groups.map((g, i) => groupSpan(g, i, true)));
    };

    const versionCell = (row) => {
        const subs = subInstances(row);
        const total = subs.length;
        const groups = groupSubs(subs, (s) => s.instance.existingVersion || '');
        const labels = groups.map((g) => {
            const short = g.key ? shortVersion(g.key) : 'none';
            return total > 1 ? `${short} ×${g.count}` : short;
        });
        const line = (g, i) => versionNode(g.first.member, g.first.instance, labels[i]);
        if (oneLineFits(labels, MONO_CHAR_PX, 0, usablePx("version"))) {
            return oneLineCell(groups.map((g, i) => [line(g, i)]), labels.join(" · "));
        }
        return stackedCell(groups.map(line));
    };

    const prepareCell = (row) => {
        const subs = subInstances(row);
        const total = subs.length;
        const groups = groupSubs(subs, (s) => preparePhaseView(s.instance)?.label || '-');
        const labels = groups.map((g) => countLabel(g, total));
        const line = (g, i) => {
            const phase = preparePhaseView(g.first.instance);
            const testID = !row.isSystemGroup && i === 0 ? `deployment-prepare-status-${row.name}` : undefined;
            if (!phase) {
                return span({class: "text-gray-500", ...(testID ? {"data-testid": testID} : {})}, '-');
            }
            const version = g.first.instance.prepareVersion || '';
            return button({
                type: "button",
                class: `${PREPARE_TEXT[phase.tone]} hover:brightness-125 underline cursor-pointer p-0 truncate text-left min-w-0`,
                title: `${version ? shortVersion(version) + ' ' : ''}${phase.label} — view prepare output`,
                ...(testID ? {"data-testid": testID} : {}),
                onclick: (e) => { e.stopPropagation(); onShowPrepareOutput(g.first.member); },
            }, labels[i]);
        };
        if (oneLineFits(labels, UI_CHAR_PX, 0, usablePx("prepare"))) {
            return oneLineCell(groups.map((g, i) => [line(g, i)]), labels.join(" · "));
        }
        return stackedCell(groups.map(line));
    };

    const nodesCell = (row) => {
        const nodes = [...new Set(subInstances(row).map((s) => s.node).filter(Boolean))];
        if (nodes.length === 0) return span({class: "text-gray-500"}, '-');
        return nodes.length === 1
            ? span({class: "truncate text-gray-300"}, nodes[0])
            : span({class: "truncate text-gray-300", title: nodes.join(", ")}, `${nodes.length} nodes`);
    };

    const restartsCell = (row) => {
        const subs = subInstances(row);
        const total = subs.reduce((n, s) => n + (s.instance.numberOfRestarts || 0), 0);
        const last = subs
            .map((s) => s.instance.lastRestartAt)
            .filter((t) => t instanceof Date && t.getTime() > 0)
            .sort((a, b) => b - a)[0];
        return div({class: "flex items-baseline gap-1.5 overflow-hidden whitespace-nowrap"},
            total > 0
                ? button({
                    type: "button",
                    class: "cursor-pointer p-0 text-gray-300 underline hover:text-white",
                    title: "View run report",
                    "data-testid": `deployment-restarts-open-${row.name}`,
                    onclick: (e) => {
                        e.stopPropagation();
                        const target = subs
                            .filter((s) => (s.instance.numberOfRestarts || 0) > 0)
                            .sort((a, b) => (b.instance.lastRestartAt?.getTime?.() || 0) - (a.instance.lastRestartAt?.getTime?.() || 0))[0];
                        if (target) onShowRunReport(target.member, target.instance);
                    },
                }, String(total))
                : span({class: "text-gray-500"}, String(total)),
            last ? span({class: "truncate text-[11px] text-gray-500"}, formatDateTime(last, "")) : "");
    };

    const latestAudit = (row) => {
        if (!row.isSystemGroup) return {by: row.deployedBy, at: row.deployedAt};
        const newest = [...row.members].sort((a, b) =>
            (b.deployedAt instanceof Date ? b.deployedAt.getTime() : 0) - (a.deployedAt instanceof Date ? a.deployedAt.getTime() : 0))[0];
        return {by: newest?.deployedBy || 0, at: newest?.deployedAt};
    };

    const deploymentRow = (row) => {
        const audit = latestAudit(row);
        return tr(
            {
                class: () => `cursor-default ${selectedRowId.val === row.id ? "bg-brand/15" : "hover:bg-gray-700/35"}`,
                onclick: () => select(row.id),
                "data-testid": `deployment-row-${row.name}`,
            },
            td({class: `${tdBase} whitespace-nowrap`},
                span({class: "truncate font-medium text-white"}, row.name || `#${row.id}`)),
            td({class: `${tdBase} whitespace-nowrap`}, nodesCell(row)),
            td({class: tdBase}, statusCell(row)),
            td({class: tdBase}, versionCell(row)),
            td({class: tdBase}, prepareCell(row)),
            td({class: `${tdBase}`, "data-testid": `deployment-restarts-${row.name}`}, restartsCell(row)),
            td({class: `${tdBase} whitespace-nowrap text-gray-400`}, () => resolveUserDisplayName(audit.by) || 'unknown'),
            td({class: `${tdBase} whitespace-nowrap text-gray-500`, title: formatDateTime(audit.at, "")},
                formatDateTime(audit.at, "-")),
            td({class: `${tdBase} px-1 text-right whitespace-nowrap`},
                button({
                    type: "button",
                    title: `Update ${row.name}`,
                    "aria-label": "Update",
                    class: () => "inline-flex h-6 w-6 items-center justify-center rounded text-gray-500 hover:text-gray-100 " +
                        "hover:bg-white/10 transition-opacity cursor-pointer " +
                        (selectedRowId.val === row.id ? "opacity-100" : "opacity-40 hover:opacity-100"),
                    onclick: (e) => {
                        e.stopPropagation();
                        onUpdate(row);
                    },
                }, editIcon({class: "w-3.5 h-3.5"}))),
        );
    };

    const spaceBandRow = (space, count, collapsed) => tr(
        {class: "cursor-default bg-gray-950/30 hover:bg-gray-700/35", onclick: () => toggleCollapsed(space.id)},
        td({class: "border-b border-gray-800/80 px-2 py-1", colSpan: COLUMN_DEFS.length},
            span({class: "flex items-center gap-1.5 font-mono text-[13px]"},
                button({
                    type: "button",
                    "aria-label": collapsed ? `Expand ${spaceLabelOf(space)}` : `Collapse ${spaceLabelOf(space)}`,
                    class: "flex h-4 w-4 flex-none items-center justify-center rounded-sm text-gray-500 hover:text-gray-100 hover:bg-white/10 cursor-pointer",
                    onclick: (e) => { e.stopPropagation(); toggleCollapsed(space.id); },
                }, caretRightIcon({class: `w-[11px] h-[11px] transition-transform ${collapsed ? "" : "rotate-90"}`})),
                spaceDot(space.id),
                span({class: "font-semibold text-gray-100"}, spaceLabelOf(space)),
                span({class: "text-[10.5px] text-gray-500"}, String(count)))),
    );

    const fitColumns = (avail, keepKey = null) => {
        if (avail < 200) return;
        const widths = {...colWidths.rawVal};
        const total = () => COLUMN_DEFS.reduce((sum, c) => sum + widths[c.key], 0);
        let growable = COLUMN_DEFS.filter((c) => c.key !== "edit" && c.key !== keepKey);
        for (let pass = 0; pass < 3; pass++) {
            const free = avail - total();
            if (!growable.length || Math.abs(free) < 1) break;
            const share = free / growable.length;
            for (const c of growable) widths[c.key] = Math.max(c.min, Math.round(widths[c.key] + share));
            if (free < 0) growable = growable.filter((c) => widths[c.key] > c.min);
        }
        const rem = avail - total();
        const catchAll = growable[0];
        if (catchAll && rem !== 0) widths[catchAll.key] = Math.max(catchAll.min, widths[catchAll.key] + rem);
        if (COLUMN_DEFS.some((c) => widths[c.key] !== colWidths.rawVal[c.key])) colWidths.val = widths;
    };

    const startColResize = (event, colKey, min) => {
        event.preventDefault();
        event.stopPropagation();
        const tableEl = event.target.closest("table");
        const colEl = tableEl?.querySelector(`col[data-col="${colKey}"]`);
        const startX = event.clientX;
        const startW = colWidths.rawVal[colKey];
        const startTotal = COLUMN_DEFS.reduce((sum, c) => sum + colWidths.rawVal[c.key], 0);
        let width = startW;
        const move = (ev) => {
            width = Math.max(min, startW + (ev.clientX - startX));
            if (colEl) colEl.style.width = `${width}px`;
            if (tableEl) tableEl.style.width = `${startTotal + (width - startW)}px`;
        };
        const up = () => {
            document.removeEventListener("mousemove", move);
            document.removeEventListener("mouseup", up);
            document.body.classList.remove("resizing");
            colWidths.val = {...colWidths.rawVal, [colKey]: width};
            fitColumns(tableScroll.clientWidth, colKey);
        };
        document.addEventListener("mousemove", move);
        document.addEventListener("mouseup", up);
        document.body.classList.add("resizing");
    };

    const nudgeColWidth = (event, colKey, min) => {
        if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
        event.preventDefault();
        const step = event.shiftKey ? 48 : 16;
        const current = colWidths.rawVal[colKey];
        colWidths.val = {...colWidths.rawVal, [colKey]: Math.max(min, current + (event.key === "ArrowRight" ? step : -step))};
        fitColumns(tableScroll.clientWidth, colKey);
    };

    const headerCell = (c, isLast) => {
        const grip = isLast ? "" : span({
            class: "colgrip",
            tabindex: "0",
            role: "separator",
            "aria-orientation": "vertical",
            "aria-label": `Resize ${c.label || "actions"} column`,
            onclick: (e) => e.stopPropagation(),
            onmousedown: (e) => startColResize(e, c.key, c.min),
            onkeydown: (e) => nudgeColWidth(e, c.key, c.min),
        });
        return th({class: thBase}, c.label, grip);
    };

    const deploymentTable = () => {
        const view = deploymentViewS.val;
        if (view.message) {
            return div({class: "p-6 text-sm text-gray-400"}, view.message);
        }
        const bySpace = new Map();
        for (const row of view.rows) {
            if (!bySpace.has(row.spaceId)) bySpace.set(row.spaceId, []);
            bySpace.get(row.spaceId).push(row);
        }
        const rows = visibleSpaces().flatMap((space) => {
            const members = bySpace.get(space.id) || [];
            if (!members.length) return [];
            const collapsed = collapsedSpaces.val.has(space.id);
            return [
                spaceBandRow(space, members.length, collapsed),
                ...(collapsed ? [] : members.map(deploymentRow)),
            ];
        });
        const widths = colWidths.val;
        const totalWidth = COLUMN_DEFS.reduce((sum, c) => sum + widths[c.key], 0);
        return table(
            {class: "table-fixed border-separate border-spacing-0 text-sm", style: `width:${totalWidth}px`},
            colgroup(...COLUMN_DEFS.map((c) => col({"data-col": c.key, style: `width:${widths[c.key]}px`}))),
            thead(tr(...COLUMN_DEFS.map((c, i) => headerCell(c, i === COLUMN_DEFS.length - 1)))),
            tbody(...rows),
        );
    };

    const filterButton = ({menu, label, ariaLabel}) => button({
        type: "button",
        "aria-haspopup": "true",
        "aria-expanded": () => String(openMenu.val === menu),
        "aria-label": ariaLabel,
        class: "inline-flex h-[30px] items-center gap-1.5 rounded border border-gray-600 px-2 text-xs " +
            "text-gray-400 hover:bg-surface-hover hover:text-gray-100 transition-colors cursor-pointer",
        onclick: (e) => {
            e.stopPropagation();
            openMenu.val = openMenu.val === menu ? null : menu;
        },
    }, () => span({class: "inline-flex items-center gap-1.5"}, ...label()));

    const menuShell = (...children) => div({
        class: "absolute top-full left-0 z-30 mt-1.5 min-w-52 rounded-md border border-gray-600 bg-surface p-1 shadow-2xl flex flex-col",
        onclick: (e) => e.stopPropagation(),
    }, ...children);

    const menuRow = (attrs, onclick, ...children) => button({
        type: "button",
        ...attrs,
        class: "flex items-center gap-2 rounded px-2 py-1.5 text-left text-xs text-gray-200 hover:bg-surface-hover cursor-pointer",
        onclick,
    }, ...children);

    const menuCheck = (on) => checkIcon({class: `w-3.5 h-3.5 flex-none text-brand ${on ? "" : "invisible"}`});

    const toggleSpace = (id) => {
        const next = new Set(hiddenSpaces.val);
        next.has(id) ? next.delete(id) : next.add(id);
        hiddenSpaces.val = next;
        saveHiddenSpaces(next);
    };

    const spacesMenu = () => menuShell(
        ...orderedSpaces().map((space) => menuRow(
            {
                role: "menuitemcheckbox",
                "aria-checked": String(!hiddenSpaces.val.has(Number(space.id))),
            },
            () => toggleSpace(Number(space.id)),
            menuCheck(!hiddenSpaces.val.has(Number(space.id))),
            spaceDot(space.id),
            span({class: "font-mono"}, spaceLabelOf(space)),
        )),
        ...(spacesDirty() ? [
            div({class: "my-1 border-t border-gray-700"}),
            menuRow({}, () => {
                const next = new Set([OPENDEPLOY_SPACE_ID]);
                hiddenSpaces.val = next;
                saveHiddenSpaces(next);
            }, closeIcon({class: "w-3.5 h-3.5 flex-none text-brand"}), "Reset to default"),
        ] : []),
    );

    const toolbar = () => div(
        {class: "flex flex-none flex-wrap items-center gap-2 border-b border-gray-700 px-2 py-2"},
        div({class: "relative"},
            searchIcon({class: "pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-gray-500"}),
            input({
                class: "text-input search-input search-input-iconed toolbar-input",
                type: "search",
                placeholder: "Search deployments",
                "aria-label": "Search deployments",
                value: search,
                oninput: (e) => { search.val = e.target.value; },
            })),
        span({class: "relative inline-flex"},
            filterButton({
                menu: "spaces",
                ariaLabel: "Filter spaces",
                label: () => [
                    span({class: "inline-flex items-center gap-1"}, ...visibleSpaces().map((s) => spaceDot(s.id))),
                    `${visibleSpaces().length} space${visibleSpaces().length === 1 ? "" : "s"}`,
                    chevronDownIcon({class: "w-3 h-3"}),
                ],
            }),
            () => openMenu.val === "spaces" ? spacesMenu() : ""),
        button({
            type: "button",
            "data-testid": "recently-deleted-button",
            class: "toolbar-button",
            title: "Deployments deleted recently",
            onclick: openRecentlyDeletedOverlay,
        }, "Recently deleted"),
        div({class: "flex-1"}),
        button({
            type: "button",
            class: "toolbar-button",
            onclick: openExportOverlay,
        }, "Export"),
        button({
            type: "button",
            "data-testid": "add-deployment-button",
            class: "toolbar-button",
            onclick: openCreateOverlay,
        }, plusIcon({class: "w-3.5 h-3.5"}), "Add deployment"),
    );

    const copyButton = (text, what) => {
        const done = van.state(false);
        return button({
            type: "button",
            title: `Copy ${what}`,
            "aria-label": `Copy ${what}`,
            class: "inline-flex h-5 w-5 flex-none items-center justify-center rounded text-gray-500 hover:text-gray-100 hover:bg-white/10 transition-colors cursor-pointer",
            onclick: async (e) => {
                e.stopPropagation();
                try { await navigator.clipboard.writeText(text); } catch {}
                done.val = true;
                setTimeout(() => { done.val = false; }, 1200);
            },
        }, () => done.val ? checkIcon({class: "w-3 h-3 text-green-400"}) : copyIcon({class: "w-3 h-3"}));
    };

    const factTh = "w-[92px] border-b border-gray-800/60 px-2 py-1 text-left align-baseline text-[10px] font-semibold uppercase tracking-wide text-gray-500";
    const factTd = "border-b border-gray-800/60 px-2 py-1 align-baseline text-xs text-gray-300 break-all";
    const factRow = (label, ...value) => tr(th({class: factTh, scope: "row"}, label), td({class: factTd}, ...value));

    const factsTable = (row) => {
        const nodes = [...new Set(subInstances(row).map((s) => s.node).filter(Boolean))];
        const dns = deploymentDnsName(row.name, row.spaceId);
        return table({class: "w-full border-collapse"},
            tbody(
                factRow("Deployment", span({class: "font-mono text-gray-100"}, row.name)),
                factRow("Space", span({class: "inline-flex items-center gap-1.5 font-mono"},
                    spaceDot(row.spaceId), row.spaceName)),
                factRow("Nodes", span({class: "font-mono"}, nodes.join(", ") || '-')),
                ...(row.isSystemGroup ? [] : [
                    factRow("Created", formatDateTime(row.createdAt, "-")),
                    factRow("Deployed", div({class: "flex items-center gap-1.5"},
                        span(formatDateTime(row.deployedAt, "-")),
                        span({class: "text-gray-600"}, "·"),
                        span(() => resolveUserDisplayName(row.deployedBy) || 'unknown'))),
                    factRow("Target", row.deployedVersion
                        ? span({class: "font-mono", title: row.deployedVersion}, shortVersion(row.deployedVersion))
                        : span({class: "text-gray-500"}, '-')),
                    ...(row.hasNetworking ? [
                        factRow("DNS name", div({class: "flex items-center gap-1.5 min-w-0"},
                            span({class: "truncate font-mono"}, dns),
                            copyButton(dns, "DNS name"))),
                    ] : []),
                ]),
            ));
    };

    const instancesTable = (row) => {
        const subs = subInstances(row);
        return table(
            {class: "w-full table-fixed border-collapse text-xs"},
            colgroup(
                col({style: "width:2.6rem"}),
                col({style: "width:5.4rem"}),
                col({style: "width:5.2rem"}),
                col(),
                col({style: "width:6.6rem"}),
                col({style: "width:2.2rem"}),
            ),
            thead(tr(miniTh("Id"), miniTh("Node"), miniTh("Status"), miniTh("Version"), miniTh("Prepare"), miniTh("Rst", "text-right"))),
            tbody(...subs.map((sub) => {
                const view = instanceStatusView(sub.member, sub.instance);
                const phase = preparePhaseView(sub.instance);
                return tr(
                    {class: "hover:bg-gray-700/35"},
                    miniTd({class: "font-mono text-gray-500"}, String(sub.instance.instanceId || '-')),
                    miniTd({class: "font-mono text-gray-300"},
                        span({class: "flex items-center gap-1.5 min-w-0"},
                            span({class: "truncate"}, sub.node || '-'),
                            sub.isPrimaryNode ? span({class: "text-[9px] uppercase tracking-wide text-gray-500"}, 'primary') : '')),
                    miniTd({},
                        span({
                            class: `flex items-center gap-1.5 ${view.style.text}` +
                                (view.hasRunOutput ? ' cursor-pointer hover:brightness-125' : ''),
                            "data-testid": `deployment-runner-status-${sub.testSuffix}`,
                            ...(view.hasRunOutput ? {
                                title: "View run output",
                                onclick: () => onShowRunOutput(sub.member),
                            } : {}),
                        },
                            span({class: `inline-block w-1.5 h-1.5 rounded-full flex-none ${view.style.dot}`}),
                            span({class: "truncate"}, view.label))),
                    miniTd({"data-testid": `deployment-version-${sub.testSuffix}`},
                        versionNode(sub.member, sub.instance, sub.instance.existingVersion ? shortVersion(sub.instance.existingVersion) : 'none')),
                    miniTd({},
                        phase
                            ? button({
                                type: "button",
                                class: `${PREPARE_TEXT[phase.tone]} hover:brightness-125 underline cursor-pointer p-0 truncate text-left min-w-0`,
                                "data-testid": `deployment-prepare-status-${sub.testSuffix}`,
                                title: `${sub.instance.prepareVersion ? shortVersion(sub.instance.prepareVersion) + ' ' : ''}${phase.label} — view prepare output`,
                                onclick: () => onShowPrepareOutput(sub.member),
                            }, phase.label)
                            : span({class: "text-gray-500", "data-testid": `deployment-prepare-status-${sub.testSuffix}`}, '-')),
                    miniTd({class: "text-right tabular-nums"},
                        (sub.instance.numberOfRestarts || 0) > 0
                            ? button({
                                type: "button",
                                class: "cursor-pointer p-0 tabular-nums text-gray-300 underline hover:text-white",
                                title: "View run report",
                                "data-testid": `instance-rst-open-${sub.testSuffix}`,
                                onclick: () => onShowRunReport(sub.member, sub.instance),
                            }, String(sub.instance.numberOfRestarts))
                            : span({class: "text-gray-600"}, String(sub.instance.numberOfRestarts || 0))),
                );
            })),
        );
    };

    const sectionLabel = (text) => p({class: "px-2 pt-3 pb-1 text-[10px] font-semibold uppercase tracking-wider text-gray-500"}, text);

    const inspectorActionButton = (text, onclick, cls = "bg-gray-700 text-gray-200 hover:bg-gray-600") => button({
        type: "button",
        class: `text-xs px-2.5 py-1.5 rounded-md font-medium transition-colors cursor-pointer whitespace-nowrap ${cls}`,
        onclick,
    }, text);

    const detailsTab = (row) => [
        div({class: "app-scroll flex-1 min-h-0 overflow-y-auto"},
            sectionLabel("Overview"),
            factsTable(row),
            sectionLabel(`Instances · ${subInstances(row).length}`),
            div({class: "px-2 pb-2"}, instancesTable(row)),
            ...(row.isSystemGroup ? [] : [
                sectionLabel("Network policies"),
                div({class: "px-2 pb-2"}, deploymentNetworkPolicies(row.id, row.spaceId)),
            ])),
        div({class: "flex flex-none flex-wrap gap-1.5 border-t border-gray-800 px-3 py-2.5"},
            inspectorActionButton("Update", () => onUpdate(row), "bg-brand text-white hover:bg-blue-600"),
            ...(row.isSystemGroup ? [] : [
                inspectorActionButton("Fork", () => onFork(row)),
                inspectorActionButton("View config", () => onViewConfig(row)),
                inspectorActionButton("Prepare output", () => onShowPrepareOutput(row)),
                ...(row.canDelete ? [
                    inspectorActionButton("Delete", () => onDelete(row), "bg-gray-700 text-gray-200 hover:bg-red-600 hover:text-white"),
                ] : []),
            ])),
    ];

    const historyPanelFor = (deploymentId) => {
        const key = String(deploymentId);
        if (historyCache.key !== key) {
            historyCache = {key, node: deploymentHistoryPanel(deploymentId, onRevertHistoryTargetVersion)};
        }
        return historyCache.node;
    };

    const historyTab = (row) => {
        if (!row.isSystemGroup) return [historyPanelFor(row.id)];
        const memberId = row.members.some((m) => m.id === historyMemberId.val)
            ? historyMemberId.val
            : row.members[0].id;
        return [
            div({class: "flex flex-none flex-wrap items-center gap-1 border-b border-gray-800 px-2 py-1.5"},
                ...row.members.map((member) => button({
                    type: "button",
                    class: `rounded px-2 py-0.5 text-[11px] cursor-pointer transition-colors ${member.id === memberId
                        ? "bg-gray-700 text-gray-100"
                        : "text-gray-500 hover:text-gray-200 hover:bg-gray-800"}`,
                    onclick: () => { historyMemberId.val = member.id; },
                }, member.node || `#${member.id}`))),
            historyPanelFor(memberId),
        ];
    };

    const inspectorTabButton = (key, text) => button({
        type: "button",
        class: () => "flex-1 border-b-2 px-3 py-2 text-xs font-medium transition-colors cursor-pointer " +
            (inspectorTab.val === key
                ? "border-brand text-gray-100"
                : "border-transparent text-gray-500 hover:text-gray-300"),
        onclick: () => { inspectorTab.val = key; },
    }, text);

    const startInspectorResize = (event) => {
        event.preventDefault();
        event.stopPropagation();
        const pane = event.target.parentElement;
        const startX = event.clientX;
        const startW = inspectorWidth.rawVal;
        let width = startW;
        const move = (ev) => {
            width = Math.min(INSPECTOR_MAX, Math.max(INSPECTOR_MIN, startW - (ev.clientX - startX)));
            if (pane) pane.style.width = `${width}px`;
        };
        const up = () => {
            document.removeEventListener("mousemove", move);
            document.removeEventListener("mouseup", up);
            document.body.classList.remove("resizing");
            inspectorWidth.val = width;
            saveInspectorWidth(width);
        };
        document.addEventListener("mousemove", move);
        document.addEventListener("mouseup", up);
        document.body.classList.add("resizing");
    };

    const inspector = () => {
        const row = deploymentViewS.val.rows.find((r) => r.id === selectedRowId.val);
        if (!row) return '';
        return div(
            {
                class: "relative flex h-full flex-none flex-col border-l border-gray-700 bg-gray-950/35",
                style: `width:${inspectorWidth.rawVal}px`,
                "data-testid": "deployment-inspector",
            },
            button({
                type: "button",
                class: "vgrip",
                "aria-label": "Resize inspector",
                onmousedown: startInspectorResize,
            }),
            div({class: "flex flex-none items-center gap-2 border-b border-gray-800 py-2.5 pl-3 pr-2"},
                span({class: "min-w-0 truncate font-mono text-sm text-white"}, row.name),
                span({class: "inline-flex items-center gap-1.5 font-mono text-[11px] text-gray-400"},
                    spaceDot(row.spaceId), row.spaceName),
                div({class: "flex-1"}),
                button({
                    type: "button",
                    title: "Close inspector",
                    "aria-label": "Close inspector",
                    class: "inline-flex h-6 w-6 flex-none items-center justify-center rounded text-gray-500 hover:text-gray-100 hover:bg-white/10 transition-colors cursor-pointer",
                    onclick: closeInspector,
                }, closeIcon({class: "w-3.5 h-3.5"}))),
            div({class: "flex flex-none border-b border-gray-800"},
                inspectorTabButton("details", "Details"),
                inspectorTabButton("history", "History")),
            div({class: "flex min-h-0 flex-1 flex-col"},
                ...(inspectorTab.val === "history" ? historyTab(row) : detailsTab(row))),
        );
    };

    const tableScroll = div({class: "app-scroll flex-1 min-h-0 overflow-auto"}, deploymentTable);

    const root = div(
        {class: "flex h-full min-h-0 flex-col bg-surface"},
        div({class: "flex min-h-0 flex-1"},
            div({class: "flex min-h-0 min-w-0 flex-1 flex-col"}, toolbar(), tableScroll),
            inspector),
        () => openMenu.val ? div({class: "fixed inset-0 z-20", onclick: () => { openMenu.val = null; }}) : "",
        () => overlayNode.val || '',
        () => createOverlayNode.val || '',
    );

    new ResizeObserver(() => fitColumns(tableScroll.clientWidth)).observe(tableScroll);

    return root;
}
