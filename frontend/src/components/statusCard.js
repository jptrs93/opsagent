import van from "vanjs-core";
import {formatDateTime} from "../lib/date.js";
import {resolveUserDisplayName} from "../lib/users.js";
import {preparerPhase} from "../lib/preparerStatus.js";

const { tr, td, div, span, button, a } = van.tags;

const existingStatusLabels = {
    0: {bg: 'bg-gray-600', text: 'text-gray-300', label: 'Unknown'},
    1: {bg: 'bg-gray-600', text: 'text-gray-300', label: 'No Deployment'},
    2: {bg: 'bg-green-600', text: 'text-green-300', label: 'Running'},
    3: {bg: 'bg-gray-600', text: 'text-gray-300', label: 'Stopped'},
    4: {bg: 'bg-yellow-600', text: 'text-yellow-300', label: 'Starting'},
    5: {bg: 'bg-red-600', text: 'text-red-300', label: 'Crashed'},
};

const missingNodeStatusLabel = {bg: 'bg-yellow-600', text: 'text-yellow-300', label: 'Unknown'};

// Until the runner reports in, the preparer drives the Status badge. A ready
// preparer with no runner yet is the deployment starting.
const preRunnerBadges = {
    progress: {bg: 'bg-blue-600', text: 'text-blue-200', label: 'Preparing'},
    ready: {bg: 'bg-yellow-600', text: 'text-yellow-300', label: 'Starting'},
    failed: {bg: 'bg-red-600', text: 'text-red-300', label: 'Prepare failed'},
};

const prepareToneClass = {
    progress: 'text-blue-300',
    ready: 'text-green-300',
    failed: 'text-red-300',
};

const STATUS_NO_DEPLOYMENT = 1;
const STATUS_STOPPED = 3;
const openDeployRepo = 'github.com/jptrs93/opsagent';

const preRunnerStatusLabel = (preparer) => {
    const phase = preparerPhase(preparer);
    return phase ? preRunnerBadges[phase.tone] : null;
};

// The Prepare column names the stage preparation is in, so a deployment stuck
// resolving a secret reads differently from one stuck in a long build. The
// version being prepared is left to the tooltip: the Version cell on the same
// line already carries it, and only differs when it is already flagged orange.
const prepareStatusCopy = (preparer, prepareVersion) => {
    if (!prepareVersion) return null;
    const phase = preparerPhase(preparer);
    if (!phase) return null;
    return {
        class: prepareToneClass[phase.tone],
        text: phase.label,
        title: `${shortVersion(prepareVersion)} ${phase.label} — view prepare output`,
    };
};

// Status, Version, and Prepare each split into one line per scheduled instance,
// so a rollover reads across the row. The suffix is dropped in the single
// instance case, which is what the e2e helpers address.
const instanceTestID = (prefix, statusKey, instance, index, count) =>
    `${prefix}-${statusKey}${count > 1 ? `-${instance.instanceId || index + 1}` : ''}`;

export function statusRow(deployment, onShowHistory, onShowRunOutput, onShowPrepareOutput, onUpdate, onFork, opts = {}) {
    const showSpace = opts.showSpace !== false;
    const onViewConfig = opts.onViewConfig || (() => {});
    const onDelete = opts.onDelete || (() => {});
    const canDelete = deployment.canDelete ?? deployment.existingStatus === STATUS_STOPPED;
    const statusKey = deployment.name || deployment.id;
    const scheduledInstances = deployment.scheduledInstances?.length > 0
        ? deployment.scheduledInstances
        : [deployment];
    const scheduledInstanceCellClass = scheduledInstances.length > 1 ? 'h-7' : 'h-10';
    const menuOpen = van.state(false);
    const actionButtonClass = "rounded-lg bg-gray-700 text-gray-200 hover:bg-gray-600 transition-colors text-xs leading-none px-2 py-1.5 cursor-pointer";
    let menuEl = null;
    let offMenuClick = null;
    let offViewportHandlers = null;

    const closeMenu = () => {
        menuOpen.val = false;
        if (offMenuClick) {
            document.removeEventListener('mousedown', offMenuClick);
            offMenuClick = null;
        }
        if (offViewportHandlers) {
            offViewportHandlers();
            offViewportHandlers = null;
        }
        if (menuEl) {
            menuEl.remove();
            menuEl = null;
        }
    };

    const positionMenu = (anchor) => {
        if (!menuEl) return;

        const rect = anchor.getBoundingClientRect();
        const gap = 4;
        const edgePadding = 8;
        const menuWidth = menuEl.offsetWidth;
        const menuHeight = menuEl.offsetHeight;
        const left = Math.max(edgePadding, Math.min(window.innerWidth - menuWidth - edgePadding, rect.right - menuWidth));
        let top = rect.bottom + gap;

        if (top + menuHeight > window.innerHeight - edgePadding) {
            top = Math.max(edgePadding, rect.top - menuHeight - gap);
        }

        menuEl.style.left = `${left}px`;
        menuEl.style.top = `${top}px`;
        menuEl.style.visibility = 'visible';
    };

    const menuAction = (label, onClick) => button({
        class: "block w-full px-3 py-1.5 text-left text-xs text-gray-200 hover:bg-gray-800 cursor-pointer",
        onclick: () => {
            closeMenu();
            onClick();
        },
        type: "button",
    }, label);

    const openMenu = (anchor) => {
        menuOpen.val = true;
        menuEl = div(
            {
                class: "fixed z-50 min-w-28 overflow-hidden rounded-lg border border-gray-700 bg-gray-900 py-1 text-left shadow-xl",
                onmousedown: (e) => e.stopPropagation(),
                style: "visibility:hidden",
            },
            menuAction("View config", () => onViewConfig(deployment)),
            menuAction("Fork", () => onFork(deployment)),
            canDelete ? menuAction("Delete", () => onDelete(deployment)) : '',
        );
        document.body.appendChild(menuEl);
        positionMenu(anchor);

        offMenuClick = () => closeMenu();
        setTimeout(() => document.addEventListener('mousedown', offMenuClick), 0);

        const closeOnViewportChange = () => closeMenu();
        window.addEventListener('resize', closeOnViewportChange);
        window.addEventListener('scroll', closeOnViewportChange, true);
        offViewportHandlers = () => {
            window.removeEventListener('resize', closeOnViewportChange);
            window.removeEventListener('scroll', closeOnViewportChange, true);
        };
    };

    const toggleMenu = (e) => {
        e.stopPropagation();
        if (menuOpen.val) {
            closeMenu();
            return;
        }
        openMenu(e.currentTarget);
    };

    return tr(
        {class: "border-b border-gray-800 last:border-0 hover:bg-gray-800/60 transition-colors", "data-testid": `deployment-row-${deployment.name || deployment.id}`},
        td(
            {class: "py-2 pl-4 pr-3 align-middle min-w-32"},
            span({class: "font-medium text-sm text-white break-words"}, deployment.name || `#${deployment.id}`),
        ),
        showSpace ? td({class: "py-2 px-3 align-middle text-sm text-gray-300 whitespace-nowrap"}, deployment.spaceName || '-') : '',
        td({class: "py-2 px-3 align-middle text-sm text-gray-300 break-words"}, deployment.node || '-'),
        td(
            {class: "align-middle whitespace-nowrap"},
            ...scheduledInstances.map((instance, index) => {
                const {hasRunOutput, colors} = instanceStatusDisplay(deployment, instance);
                const testID = instanceTestID('deployment-runner-status', statusKey, instance, index, scheduledInstances.length);
                return div(
                    {class: `${scheduledInstanceCellClass} flex items-center px-3`},
                    statusBadge(hasRunOutput, colors, () => onShowRunOutput(deployment), testID),
                );
            }),
        ),
        td(
            {class: "align-middle text-sm whitespace-nowrap"},
            ...scheduledInstances.map(instance => div(
                {class: `${scheduledInstanceCellClass} flex items-center px-3`},
                versionLink(deployment, instance),
            )),
        ),
        td(
            {class: "align-middle text-sm"},
            ...scheduledInstances.map((instance, index) => {
                const copy = prepareStatusCopy(instance.preparer, instance.prepareVersion);
                const testID = instanceTestID('deployment-prepare-status', statusKey, instance, index, scheduledInstances.length);
                return div(
                    // Every stage name fits the column, but truncate rather than
                    // wrap as a backstop: wrapping would break the per-instance
                    // alignment with Status and Version.
                    {class: `${scheduledInstanceCellClass} flex items-center px-3`},
                    copy
                        // min-w-0 is what lets the flex item shrink below its
                        // text width so truncate can actually take effect.
                        ? button({
                            class: `${copy.class} hover:brightness-125 underline cursor-pointer p-0 truncate text-left min-w-0`,
                            "data-testid": testID,
                            onclick: () => onShowPrepareOutput(deployment),
                            title: copy.title,
                            type: "button",
                        }, copy.text)
                        : span({class: "text-gray-500", "data-testid": testID}, '-'),
                );
            }),
        ),
        td(
            {
                class: "py-2 px-3 align-middle text-sm text-gray-300 whitespace-nowrap",
                "data-testid": `deployment-restarts-${deployment.name || deployment.id}`,
            },
            div(
                {class: "inline-flex items-baseline gap-2"},
                span(deployment.numberOfRestarts),
                span({class: "text-xs text-gray-500"}, formatMaybeDate(deployment.lastRestartAt, 'n/a')),
            ),
        ),
        td(
            {class: "py-2 px-3 align-middle text-sm text-gray-300 break-words"},
            () => resolveUserDisplayName(deployment.deployedBy) || 'unknown',
        ),
        td(
            {class: "py-2 px-3 align-middle text-sm text-gray-500 whitespace-nowrap"},
            formatMaybeDate(deployment.deployedAt, 'unknown'),
        ),
        td(
            {class: "py-2 pl-3 pr-1 align-middle text-right whitespace-nowrap"},
            div(
                {class: "inline-flex items-center justify-end gap-1"},
                button({
                    class: actionButtonClass,
                    onclick: () => onShowHistory(deployment),
                    type: "button",
                }, "History"),
                button({
                    class: actionButtonClass,
                    onclick: () => onUpdate(deployment),
                    type: "button",
                }, "Update"),
                button({
                    class: actionButtonClass,
                    onmousedown: (e) => e.stopPropagation(),
                    onclick: toggleMenu,
                    type: "button",
                    title: "More actions",
                }, ".."),
            ),
        ),
    );
}

function instanceStatusDisplay(deployment, instance) {
    const hasExisting = instance.existingStatus !== STATUS_NO_DEPLOYMENT;
    const preRunnerColors = !instance.runnerPresent ? preRunnerStatusLabel(instance.preparer) : null;
    const nodeMissing = instance.nodeMissing ?? deployment.nodeMissing;
    const colors = preRunnerColors || (hasExisting
        ? (nodeMissing && instance.existingStatus === 0
            ? missingNodeStatusLabel
            : (existingStatusLabels[instance.existingStatus] || existingStatusLabels[0]))
        : {bg: 'bg-gray-700', text: 'text-gray-400', label: 'No existing deployment'});
    return {hasRunOutput: hasExisting && instance.runnerPresent, colors};
}

function statusBadge(hasRunOutput, colors, onclick, testID) {
    if (!hasRunOutput) {
        return span({class: `px-2 py-0.5 rounded text-xs font-medium ${colors.bg} ${colors.text}`, "data-testid": testID}, colors.label);
    }
    return span({
        class: `px-2 py-0.5 rounded text-xs font-medium cursor-pointer hover:brightness-125 ${colors.bg} ${colors.text}`,
        "data-testid": testID,
        onclick,
        title: "View run output",
    }, colors.label);
}

function versionLink(deployment, instance) {
    const version = instance.existingVersion || '';
    const desired = instance.deployedVersion || deployment.deployedVersion || '';
    if (!version) return span({class: "text-gray-500"}, 'none');
    const short = shortVersion(version);
    const mismatched = Boolean(desired) && version !== desired;
    const color = mismatched ? 'text-orange-400' : 'text-gray-300';
    const title = mismatched ? `Instance ${short}; desired ${shortVersion(desired)}` : undefined;
    const variant = instance.variant || deployment.variant;
    const repo = instance.repo || deployment.repo;
    if (variant === 'nixDockerBuild' && repo) {
        return a({
            class: `font-mono ${color} underline hover:text-white`,
            href: `https://${repo}/commit/${version}`,
            target: "_blank",
            rel: "noopener noreferrer",
            title,
        }, short);
    }
    const releaseRepo = variant === 'githubRelease'
        ? repo
        : deployment.spaceId === 0 && deployment.name === 'opendeploy-net'
            ? openDeployRepo
            : '';
    if (releaseRepo) {
        return a({
            class: `font-mono ${color} underline hover:text-white`,
            href: `https://${releaseRepo}/releases/tag/${version}`,
            target: "_blank",
            rel: "noopener noreferrer",
            title,
        }, short);
    }
    return span({class: `font-mono ${color}`, title}, short);
}

function shortVersion(v) {
    return v.length > 7 && /^[0-9a-f]+$/i.test(v) ? v.substring(0, 7) : v;
}

function formatMaybeDate(value, fallback) {
    return formatDateTime(value, fallback);
}
