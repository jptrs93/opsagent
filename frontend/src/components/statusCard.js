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

const STATUS_NO_DEPLOYMENT = 1;

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

export function statusRow(deployment, onShowHistory, onShowRunOutput, onShowPrepareOutput, onUpdate, opts = {}) {
    const showEnvironment = opts.showEnvironment !== false;
    const hasExisting = deployment.existingStatus !== STATUS_NO_DEPLOYMENT;
    const existingColors = hasExisting
        ? (existingStatusLabels[deployment.existingStatus] || existingStatusLabels[0])
        : {bg: 'bg-gray-700', text: 'text-gray-400', label: 'No existing deployment'};
    const prepareCopy = prepareStatusCopy(deployment.prepareStatus, deployment.prepareVersion);

    return tr(
        {class: "border-b border-gray-800 last:border-0 hover:bg-gray-800/60 transition-colors", "data-testid": `deployment-row-${deployment.name || deployment.id}`},
        td(
            {class: "py-3 pl-4 pr-3 align-middle min-w-32"},
            div(
                {class: "inline-flex items-baseline gap-2 max-w-full"},
                span({class: "font-medium text-sm text-white break-words"}, deployment.name || `#${deployment.id}`),
                button({
                    class: "shrink-0 text-xs text-gray-500 hover:text-gray-300 underline cursor-pointer p-0",
                    onclick: () => onShowHistory(deployment),
                    type: "button",
                }, "history"),
            ),
        ),
        showEnvironment ? td({class: "py-3 px-3 align-middle text-sm text-gray-300 whitespace-nowrap"}, deployment.environment || '-') : '',
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
            {class: "py-3 px-3 align-middle text-sm text-gray-300 whitespace-nowrap"},
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
            {class: "py-3 pl-3 pr-4 align-middle text-right whitespace-nowrap"},
            button({
                class: "btn-secondary text-xs leading-none py-0.5 px-2.5 cursor-pointer",
                onclick: () => onUpdate(deployment),
                type: "button",
            }, "Update"),
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
