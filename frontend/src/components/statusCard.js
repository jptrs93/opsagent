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

const actionButtonClass = "rounded-lg bg-gray-700 text-gray-200 hover:bg-gray-600 transition-colors text-xs leading-none px-2 py-1.5 cursor-pointer";

// rowActionsMenu owns one floating ".." menu: appended to document.body so
// overflow containers cannot clip it, positioned against the toggle anchor, and
// closed on outside click, scroll, or resize. buildItems receives a menuAction
// factory and returns the menu's child nodes. Returns the toggle handler.
function rowActionsMenu(buildItems) {
    let menuEl = null;
    let offMenuClick = null;
    let offViewportHandlers = null;

    const closeMenu = () => {
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
        menuEl = div(
            {
                class: "fixed z-50 min-w-28 overflow-hidden rounded-lg border border-gray-700 bg-gray-900 py-1 text-left shadow-xl",
                onmousedown: (e) => e.stopPropagation(),
                style: "visibility:hidden",
            },
            ...buildItems(menuAction),
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

    return (e) => {
        e.stopPropagation();
        if (menuEl) {
            closeMenu();
            return;
        }
        openMenu(e.currentTarget);
    };
}

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
    const toggleMenu = rowActionsMenu((menuAction) => [
        menuAction("View config", () => onViewConfig(deployment)),
        menuAction("Fork", () => onFork(deployment)),
        canDelete ? menuAction("Delete", () => onDelete(deployment)) : '',
    ]);

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

// systemGroupStatusRow renders the single merged row for one internal system
// deployment (opendeploy or opendeploy-net). Every member is the same logical
// deployment on one node, so Node, Status, Version, Prepare, Restarts, and the
// audit cells split into one subline per member scheduled instance, aligned
// across columns. Subline clicks (run output, prepare output) target that
// member's deployment; the row-level Update action receives the whole group.
export function systemGroupStatusRow(group, handlers, opts = {}) {
    const showSpace = opts.showSpace !== false;
    const {onShowHistory, onShowRunOutput, onShowPrepareOutput, onUpdate, onViewConfig, onDelete} = handlers;
    const members = group.members || [];
    const sublines = members.flatMap(member => {
        const instances = member.scheduledInstances?.length > 0 ? member.scheduledInstances : [member];
        return instances.map((instance, index) => ({
            member,
            instance,
            // Node-keyed testids; the ordinal disambiguates a member's transient
            // second instance during rollover.
            testSuffix: `${group.name}-${member.node}${instances.length > 1 ? `-${instance.instanceId || index + 1}` : ''}`,
        }));
    });
    const cellClass = sublines.length > 1 ? 'h-7' : 'h-10';
    const toggleMenu = rowActionsMenu((menuAction) => members.flatMap(member => [
        menuAction(`History — ${member.node}`, () => onShowHistory(member)),
        menuAction(`View config — ${member.node}`, () => onViewConfig(member)),
        member.canDelete ? menuAction(`Delete — ${member.node}`, () => onDelete(member)) : '',
    ]));

    return tr(
        {class: "border-b border-gray-800 last:border-0 hover:bg-gray-800/60 transition-colors", "data-testid": `deployment-row-${group.name}`},
        td(
            {class: "py-2 pl-4 pr-3 align-middle min-w-32"},
            span({class: "font-medium text-sm text-white break-words"}, group.name),
        ),
        showSpace ? td({class: "py-2 px-3 align-middle text-sm text-gray-300 whitespace-nowrap"}, group.spaceName || '-') : '',
        td(
            {class: "align-middle text-sm text-gray-300 whitespace-nowrap"},
            ...sublines.map(({member}) => div(
                {class: `${cellClass} flex items-center gap-1.5 px-3`},
                span({class: "truncate min-w-0"}, member.node || '-'),
                member.isPrimaryNode ? span({class: "text-[10px] uppercase tracking-wide text-gray-500"}, 'primary') : '',
            )),
        ),
        td(
            {class: "align-middle whitespace-nowrap"},
            ...sublines.map(({member, instance, testSuffix}) => {
                const {hasRunOutput, colors} = instanceStatusDisplay(member, instance);
                return div(
                    {class: `${cellClass} flex items-center px-3`},
                    statusBadge(hasRunOutput, colors, () => onShowRunOutput(member), `deployment-runner-status-${testSuffix}`),
                );
            }),
        ),
        td(
            {class: "align-middle text-sm whitespace-nowrap"},
            ...sublines.map(({member, instance, testSuffix}) => div(
                {class: `${cellClass} flex items-center px-3`, "data-testid": `deployment-version-${testSuffix}`},
                versionLink(member, instance),
            )),
        ),
        td(
            {class: "align-middle text-sm"},
            ...sublines.map(({member, instance, testSuffix}) => {
                const copy = prepareStatusCopy(instance.preparer, instance.prepareVersion);
                const testID = `deployment-prepare-status-${testSuffix}`;
                return div(
                    {class: `${cellClass} flex items-center px-3`},
                    copy
                        ? button({
                            class: `${copy.class} hover:brightness-125 underline cursor-pointer p-0 truncate text-left min-w-0`,
                            "data-testid": testID,
                            onclick: () => onShowPrepareOutput(member),
                            title: copy.title,
                            type: "button",
                        }, copy.text)
                        : span({class: "text-gray-500", "data-testid": testID}, '-'),
                );
            }),
        ),
        td(
            {class: "align-middle text-sm text-gray-300 whitespace-nowrap"},
            ...sublines.map(({member, testSuffix}) => div(
                {
                    class: `${cellClass} flex items-center gap-2 px-3`,
                    "data-testid": `deployment-restarts-${testSuffix}`,
                },
                span(member.numberOfRestarts),
                span({class: "text-xs text-gray-500"}, formatMaybeDate(member.lastRestartAt, 'n/a')),
            )),
        ),
        td(
            {class: "align-middle text-sm text-gray-300 break-words"},
            ...sublines.map(({member}) => div(
                {class: `${cellClass} flex items-center px-3`},
                () => resolveUserDisplayName(member.deployedBy) || 'unknown',
            )),
        ),
        td(
            {class: "align-middle text-sm text-gray-500 whitespace-nowrap"},
            ...sublines.map(({member}) => div(
                {class: `${cellClass} flex items-center px-3`},
                formatMaybeDate(member.deployedAt, 'unknown'),
            )),
        ),
        td(
            {class: "py-2 pl-3 pr-1 align-middle text-right whitespace-nowrap"},
            div(
                {class: "inline-flex items-center justify-end gap-1"},
                button({
                    class: actionButtonClass,
                    onclick: () => onUpdate(group),
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
    // Container image tags are unbounded, so the cell truncates and the full
    // version always lives in the tooltip — the ellipsis eats the tail, which
    // for a tag like 2025.7.23-debian-12-r5 is the part that matters least.
    const title = mismatched ? `Instance ${version}; desired ${desired}` : version;
    // min-w-0 is what lets the flex item shrink below its text width so
    // truncate can take effect.
    const clip = `font-mono ${color} truncate min-w-0`;
    const variant = instance.variant || deployment.variant;
    const repo = instance.repo || deployment.repo;
    if (variant === 'nixDockerBuild' && repo) {
        return a({
            class: `${clip} underline hover:text-white`,
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
            class: `${clip} underline hover:text-white`,
            href: `https://${releaseRepo}/releases/tag/${version}`,
            target: "_blank",
            rel: "noopener noreferrer",
            title,
        }, short);
    }
    return span({class: clip, title}, short);
}

function shortVersion(v) {
    return v.length > 7 && /^[0-9a-f]+$/i.test(v) ? v.substring(0, 7) : v;
}

function formatMaybeDate(value, fallback) {
    return formatDateTime(value, fallback);
}
