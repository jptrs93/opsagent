import van from "vanjs-core";
import {formatDateTime} from "../lib/date.js";
import {resolveUserDisplayName} from "../lib/users.js";

const { tr, td, div, span, button, a } = van.tags;

const existingStatusLabels = {
    0: {bg: 'bg-gray-600', text: 'text-gray-300', label: 'Unknown'},
    1: {bg: 'bg-gray-600', text: 'text-gray-300', label: 'No Deployment'},
    2: {bg: 'bg-green-600', text: 'text-green-300', label: 'Running'},
    3: {bg: 'bg-gray-600', text: 'text-gray-300', label: 'Stopped'},
    4: {bg: 'bg-yellow-600', text: 'text-yellow-300', label: 'Starting'},
    5: {bg: 'bg-red-600', text: 'text-red-300', label: 'Crashed'},
};

const missingMachineStatusLabel = {bg: 'bg-yellow-600', text: 'text-yellow-300', label: 'Unknown'};

const STATUS_NO_DEPLOYMENT = 1;
const STATUS_STOPPED = 3;

const prepareStatusCopy = (prepareStatus, prepareVersion) => {
    if (!prepareVersion) return null;

    switch (prepareStatus) {
        case 1:
            return {class: 'text-yellow-300', text: `requested ${shortVersion(prepareVersion)}`};
        case 2:
        case 3:
            return {class: 'text-blue-300', text: `${shortVersion(prepareVersion)} in progress`};
        case 4:
            return {class: 'text-green-300', text: `${shortVersion(prepareVersion)} ready`};
        case 5:
            return {class: 'text-red-300', text: `${shortVersion(prepareVersion)} failed`};
        default:
            return null;
    }
};

export function statusRow(deployment, onShowHistory, onShowRunOutput, onShowPrepareOutput, onUpdate, onFork, opts = {}) {
    const showSpace = opts.showSpace !== false;
    const onViewJson = opts.onViewJson || (() => {});
    const onDelete = opts.onDelete || (() => {});
    const hasExisting = deployment.existingStatus !== STATUS_NO_DEPLOYMENT;
    const canDelete = deployment.existingStatus === STATUS_STOPPED;
    const existingColors = hasExisting
        ? (deployment.machineMissing && deployment.existingStatus === 0
            ? missingMachineStatusLabel
            : (existingStatusLabels[deployment.existingStatus] || existingStatusLabels[0]))
        : {bg: 'bg-gray-700', text: 'text-gray-400', label: 'No existing deployment'};
    const prepareCopy = prepareStatusCopy(deployment.prepareStatus, deployment.prepareVersion);
    const menuOpen = van.state(false);
    const actionButtonClass = "rounded-lg bg-gray-700 text-gray-200 hover:bg-gray-600 transition-colors text-xs leading-none p-2 cursor-pointer";
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
            menuAction("View JSON", () => onViewJson(deployment)),
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
            {class: "py-3 pl-4 pr-3 align-middle min-w-32"},
            span({class: "font-medium text-sm text-white break-words"}, deployment.name || `#${deployment.id}`),
        ),
        showSpace ? td({class: "py-3 px-3 align-middle text-sm text-gray-300 whitespace-nowrap"}, deployment.spaceName || '-') : '',
        td({class: "py-3 px-3 align-middle text-sm text-gray-300 break-words"}, deployment.machine || '-'),
        td(
            {class: "py-3 px-3 align-middle whitespace-nowrap"},
            statusBadge(hasExisting, existingColors, () => onShowRunOutput(deployment)),
        ),
        td({class: "py-3 px-3 align-middle text-sm whitespace-nowrap"}, versionLink(deployment)),
        td(
            {class: "py-3 px-3 align-middle text-sm break-words"},
            prepareCopy
                ? button({
                    class: `${prepareCopy.class} hover:brightness-125 underline cursor-pointer p-0`,
                    onclick: () => onShowPrepareOutput(deployment),
                    title: "View prepare output",
                    type: "button",
                }, prepareCopy.text)
                : span({class: "text-gray-500"}, '-'),
        ),
        td(
            {
                class: "py-3 px-3 align-middle text-sm text-gray-300 whitespace-nowrap",
                "data-testid": `deployment-restarts-${deployment.name || deployment.id}`,
            },
            div(
                {class: "inline-flex items-baseline gap-2"},
                span(deployment.numberOfRestarts),
                span({class: "text-xs text-gray-500"}, formatMaybeDate(deployment.lastRestartAt, 'n/a')),
            ),
        ),
        td(
            {class: "py-3 px-3 align-middle text-sm text-gray-300 break-words"},
            () => resolveUserDisplayName(deployment.deployedBy) || 'unknown',
        ),
        td(
            {class: "py-3 px-3 align-middle text-sm text-gray-500 whitespace-nowrap"},
            formatMaybeDate(deployment.deployedAt, 'unknown'),
        ),
        td(
            {class: "py-3 pl-3 pr-1 align-middle text-right whitespace-nowrap"},
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

function statusBadge(hasExisting, colors, onclick) {
    if (!hasExisting) {
        return span({class: `px-2 py-0.5 rounded text-xs font-medium ${colors.bg} ${colors.text}`}, colors.label);
    }
    return span({
        class: `px-2 py-0.5 rounded text-xs font-medium cursor-pointer hover:brightness-125 ${colors.bg} ${colors.text}`,
        onclick,
        title: "View run output",
    }, colors.label);
}

function versionLink(deployment) {
    const running = deployment.existingVersion || '';
    const desired = deployment.deployedVersion || '';
    if (!running) return span({class: "text-gray-500"}, 'none');
    const short = shortVersion(running);
    const mismatched = Boolean(desired) && running !== desired;
    const color = mismatched ? 'text-orange-400' : 'text-gray-300';
    const title = mismatched ? `Running ${short}; desired ${shortVersion(desired)}` : undefined;
    if (deployment.variant === 'nixDockerBuild' && deployment.repo) {
        return a({
            class: `font-mono ${color} underline hover:text-white`,
            href: `https://${deployment.repo}/commit/${running}`,
            target: "_blank",
            title,
        }, short);
    }
    if (deployment.variant === 'githubRelease' && deployment.repo) {
        return a({
            class: `font-mono ${color} underline hover:text-white`,
            href: `https://${deployment.repo}/releases/tag/${running}`,
            target: "_blank",
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
