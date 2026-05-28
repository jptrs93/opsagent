import van from "vanjs-core";
import {StopCircle, PlayCircle} from "vanjs-feather";
import {format} from "date-fns";
import {resolveUserDisplayName} from "../lib/users.js";

const { tr, td, div, span, button, a } = van.tags;

const envColorCache = {};
function envColor(env) {
    if (!env || env === 'OPSAGENT_SYSTEM') return null;
    if (envColorCache[env]) return envColorCache[env];
    let hash = 0;
    for (let i = 0; i < env.length; i++) hash = ((hash << 5) - hash + env.charCodeAt(i)) | 0;
    const hue = ((hash % 360) + 360) % 360;
    envColorCache[env] = `hsl(${hue}, 30%, 14%)`;
    return envColorCache[env];
}

const existingStatusLabels = {
    0: {bg: 'bg-gray-600', text: 'text-gray-300', label: 'Unknown'},
    1: {bg: 'bg-gray-600', text: 'text-gray-300', label: 'No Deployment'},
    2: {bg: 'bg-green-600', text: 'text-green-300', label: 'Running'},
    3: {bg: 'bg-gray-600', text: 'text-gray-300', label: 'Stopped'},
    4: {bg: 'bg-yellow-600', text: 'text-yellow-300', label: 'Starting'},
    5: {bg: 'bg-red-600', text: 'text-red-300', label: 'Crashed'},
};

const STATUS_RUNNING = 2;
const STATUS_STOPPED = 3;
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

export function statusRow(deployment, onDeploy, onStop, onShowHistory, onShowRunOutput, onShowPrepareOutput, onUpdate) {
    const hasExisting = deployment.existingStatus !== STATUS_NO_DEPLOYMENT;
    const isRunning = deployment.existingStatus === STATUS_RUNNING;
    const isStopped = !hasExisting || deployment.existingStatus === STATUS_STOPPED;
    const isSystemd = deployment.runnerType === 'systemd';
    const existingColors = hasExisting
        ? (existingStatusLabels[deployment.existingStatus] || existingStatusLabels[0])
        : {bg: 'bg-gray-700', text: 'text-gray-400', label: 'No existing deployment'};
    const prepareCopy = prepareStatusCopy(deployment.prepareStatus, deployment.prepareVersion);
    const bgColor = envColor(deployment.environment);

    return tr(
        {
            class: "border-b border-gray-800 last:border-0 hover:bg-gray-800/60 transition-colors",
            style: bgColor ? `background-color: ${bgColor}` : '',
        },
        td(
            {class: "py-3 pl-4 pr-4 align-top min-w-48"},
            div({class: "font-medium text-sm text-white"}, deployment.name || `#${deployment.id}`),
            button({
                class: "text-xs text-gray-500 hover:text-gray-300 underline cursor-pointer p-0",
                onclick: () => onShowHistory(deployment),
                type: "button",
            }, "history"),
        ),
        td({class: "py-3 px-4 align-top text-sm text-gray-300 whitespace-nowrap"}, deployment.environment || '-'),
        td({class: "py-3 px-4 align-top text-sm text-gray-300 whitespace-nowrap"}, deployment.machine || '-'),
        td({class: "py-3 px-4 align-top text-sm text-gray-300 whitespace-nowrap"}, runnerLabel(deployment)),
        td(
            {class: "py-3 px-4 align-top whitespace-nowrap"},
            div(
                {class: "flex items-center gap-2"},
                !isSystemd && isRunning
                    ? iconButton("Stop", "text-red-400 hover:text-red-300", () => onStop(deployment), StopCircle({size: 14}))
                    : null,
                !isSystemd && isStopped && hasExisting && deployment.deployedVersion
                    ? iconButton("Start", "text-green-400 hover:text-green-300", () => onDeploy(deployment, deployment.deployedVersion), PlayCircle({size: 14}))
                    : null,
                statusBadge(hasExisting, existingColors, () => onShowRunOutput(deployment)),
            ),
        ),
        td({class: "py-3 px-4 align-top text-sm whitespace-nowrap"}, versionLink(deployment)),
        td(
            {class: "py-3 px-4 align-top text-sm whitespace-nowrap"},
            prepareCopy
                ? a({
                    class: `underline hover:text-white cursor-pointer ${prepareCopy.class}`,
                    onclick: () => onShowPrepareOutput(deployment),
                }, prepareCopy.text)
                : span({class: "text-gray-500"}, '-'),
        ),
        td(
            {class: "py-3 px-4 align-top text-sm text-gray-300 whitespace-nowrap"},
            div(deployment.numberOfRestarts),
            div({class: "text-xs text-gray-500"}, formatMaybeDate(deployment.lastRestartAt, 'n/a')),
        ),
        td(
            {class: "py-3 px-4 align-top text-sm text-gray-300 whitespace-nowrap"},
            div(() => resolveUserDisplayName(deployment.deployedBy) || 'unknown'),
            div({class: "text-xs text-gray-500"}, formatMaybeDate(deployment.deployedAt, 'unknown')),
        ),
        td(
            {class: "py-3 pl-4 pr-4 align-top text-right whitespace-nowrap"},
            button({
                class: "btn-secondary text-xs py-1.5 px-3 cursor-pointer",
                onclick: () => onUpdate(deployment),
                type: "button",
            }, "Update"),
        ),
    );
}

function iconButton(title, classes, onclick, icon) {
    return button({
        class: `${classes} transition-colors cursor-pointer`,
        onclick,
        title,
        type: "button",
    }, icon);
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
    const v = deployment.deployedVersion || '';
    if (!v) return span({class: "text-gray-500"}, 'none');
    const short = shortVersion(v);
    if (deployment.variant === 'nixBuild' && deployment.repo) {
        return a({
            class: "font-mono text-gray-300 underline hover:text-white",
            href: `https://${deployment.repo}/commit/${v}`,
            target: "_blank",
        }, short);
    }
    if (deployment.variant === 'githubRelease' && deployment.repo) {
        return a({
            class: "font-mono text-gray-300 underline hover:text-white",
            href: `https://${deployment.repo}/releases/tag/${v}`,
            target: "_blank",
        }, short);
    }
    return span({class: "font-mono text-gray-300"}, short);
}

function runnerLabel(deployment) {
    return deployment.runnerType === 'systemd' ? 'systemd' : 'OpsAgent';
}

function shortVersion(v) {
    return v.length > 7 && /^[0-9a-f]+$/i.test(v) ? v.substring(0, 7) : v;
}

function formatMaybeDate(value, fallback) {
    return value instanceof Date && value.getTime() > 0 ? format(value, "MMM d, HH:mm") : fallback;
}
