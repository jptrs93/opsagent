import van from "vanjs-core";
import {spinnerButton} from "./spinnerbutton.js";
import {StopCircle, PlayCircle} from "vanjs-feather";
import {format} from "date-fns";

const { div, span, select, option, button, a } = van.tags;

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
            return {class: 'text-yellow-300', prefix: '', text: `requested for ${prepareVersion.substring(0, 7)}`};
        case 2:
        case 3:
            return {class: 'text-blue-300', prefix: '', text: `${prepareVersion.substring(0, 7)} in progress`};
        case 4:
            return {class: 'text-green-300', prefix: 'Last ', text: `of ${prepareVersion.substring(0, 7)} succeeded`};
        case 5:
            return {class: 'text-red-300', prefix: 'Last ', text: `of ${prepareVersion.substring(0, 7)} failed`};
        default:
            return null;
    }
};

export function statusCard(deployment, versions, versionError, scopes, selectedScope, onScopeChange, onDeploy, onStop, onShowHistory, onShowRunOutput, onShowPrepareOutput) {
    const hasExisting = deployment.existingStatus !== STATUS_NO_DEPLOYMENT;
    const hasExistingVersion = deployment.existingVersion && deployment.existingVersion !== '';
    const isRunning = deployment.existingStatus === STATUS_RUNNING;
    const isStopped = !hasExisting || deployment.existingStatus === STATUS_STOPPED;
    const isSystemd = deployment.runnerType === 'systemd';
    const existingColors = hasExisting
        ? (existingStatusLabels[deployment.existingStatus] || existingStatusLabels[0])
        : {bg: 'bg-gray-700', text: 'text-gray-400', label: 'No existing deployment'};
    // Default version selection to the currently deployed version if it's in the list.
    const deployedId = deployment.deployedVersion || '';
    const initialVersion = versions.some(v => v.id === deployedId) ? deployedId : '';
    const selectedVersion = van.state(initialVersion);
    const prepareCopy = prepareStatusCopy(deployment.prepareStatus, deployment.prepareVersion);

    const scopeSelect = scopes.length > 0
        ? select(
            {
                class: "w-80 text-xs font-mono bg-gray-700 text-gray-200 border border-gray-600 rounded px-2 py-1 truncate focus:outline-none focus:ring-1 focus:ring-brand",
                onchange: (e) => { onScopeChange(deployment, e.target.value); },
            },
            ...scopes.map(b => option({value: b, selected: b === selectedScope || (!selectedScope && b === 'main')}, b))
        )
        : null;

    const versionLabel = (v) => {
        const date = v.time instanceof Date && v.time.getTime() > 0
            ? v.time.toISOString().substring(0, 10)
            : '';
        const shortId = v.id.length > 7 && /^[0-9a-f]+$/i.test(v.id) ? v.id.substring(0, 7) : v.id;
        const label = (v.label || '').substring(0, 20);
        const ellipsis = (v.label || '').length > 20 ? '...' : '';
        return `${date}\t\t${shortId}\t\t${label}${ellipsis}`;
    };

    const versionSelect = select(
        {
            class: "w-80 text-xs font-mono bg-gray-700 text-gray-200 border border-gray-600 rounded px-2 py-1 truncate focus:outline-none focus:ring-1 focus:ring-brand",
            onchange: (e) => { selectedVersion.val = e.target.value; }
        },
        option({value: '', disabled: true, selected: !initialVersion}, versions.length ? "Select a version..." : "No versions loaded"),
        ...versions.map(v => option({value: v.id, selected: v.id === initialVersion}, versionLabel(v)))
    );

    const deployBtn = spinnerButton("Deploy", async () => {
        await onDeploy(deployment, selectedVersion.val);
    }, "btn-primary text-xs py-1 px-3", 'button', () => !selectedVersion.val);

    return div(
        {class: "bg-gray-800 rounded-lg p-3 border border-gray-700 flex flex-col gap-2 w-fit min-w-56"},
        div(
            {class: "text-xs text-gray-500"},
            deployment.machine,
        ),
        div(
            {class: "flex items-center justify-between gap-3"},
            div(
                {class: "flex items-center gap-2"},
                span({class: "font-medium text-sm"}, deployment.name),
                a({
                    class: "text-xs text-gray-500 hover:text-gray-300 underline cursor-pointer",
                    onclick: () => onShowHistory(deployment.id),
                }, "history"),
            ),
            div(
                {class: "flex gap-1.5 items-center"},
                isRunning && !isSystemd
                    ? button({
                        class: "text-red-400 hover:text-red-300 transition-colors cursor-pointer",
                        onclick: () => onStop(deployment),
                        title: "Stop",
                    }, StopCircle({size: 14}))
                    : span(),
                isStopped && hasExisting && hasExistingVersion && !isSystemd
                    ? button({
                        class: "text-green-400 hover:text-green-300 transition-colors cursor-pointer",
                        onclick: () => onDeploy(deployment, deployment.existingVersion),
                        title: "Start",
                    }, PlayCircle({size: 14}))
                    : span(),
                hasExisting
                    ? span({
                        class: `px-2 py-0.5 rounded text-xs font-medium cursor-pointer hover:brightness-125 ${existingColors.bg} ${existingColors.text}`,
                        onclick: () => onShowRunOutput(deployment.id),
                        title: "View run output",
                    }, existingColors.label)
                    : span({class: `px-2 py-0.5 rounded text-xs font-medium ${existingColors.bg} ${existingColors.text}`}, existingColors.label),
            ),
        ),
        div(
            {class: "flex gap-4 text-xs text-gray-500 w-full py-2"},
            div(
                {class: "flex-1 basis-0 grid grid-cols-[auto_1fr] gap-x-2 gap-y-1.5"},
                span({class: "text-gray-400"}, "Deployed by:"),
                span(deployment.deployedBy || 'unknown'),
                span({class: "text-gray-400"}, "Deployed at:"),
                span(
                    deployment.deployedAt instanceof Date && deployment.deployedAt.getTime() > 0
                        ? format(deployment.deployedAt, "MMM d, HH:mm")
                        : 'unknown'
                ),
                span({class: "text-gray-400"}, "Version:"),
                (() => {
                    const v = deployment.deployedVersion || '';
                    if (!v) return span({class: "text-gray-500"}, 'none');
                    const isNix = deployment.variant === 'nixBuild' && deployment.repo;
                    const isRel = deployment.variant === 'githubRelease' && deployment.repo;
                    const short = v.length > 7 && /^[0-9a-f]+$/i.test(v) ? v.substring(0, 7) : v;
                    if (isNix) {
                        return a({
                            class: "font-mono text-gray-300 underline hover:text-white",
                            href: `https://${deployment.repo}/commit/${v}`,
                            target: "_blank",
                        }, short);
                    }
                    if (isRel) {
                        return a({
                            class: "font-mono text-gray-300 underline hover:text-white",
                            href: `https://${deployment.repo}/releases/tag/${v}`,
                            target: "_blank",
                        }, short);
                    }
                    return span({class: "font-mono text-gray-300"}, short);
                })(),
            ),
            div(
                {class: "flex-1 basis-0 grid grid-cols-[auto_1fr] gap-x-2 gap-y-1.5"},
                span({class: "text-gray-400"}, "Restarts:"),
                span(String(deployment.numberOfRestarts)),
                span({class: "text-gray-400"}, "Last restart:"),
                span(
                    deployment.lastRestartAt instanceof Date && deployment.lastRestartAt.getTime() > 0
                        ? format(deployment.lastRestartAt, "MMM d, HH:mm")
                        : 'n/a'
                ),
                span(), span(),
            ),
        ),
        div(
            {class: "mt-auto flex flex-col gap-2"},
            prepareCopy
                ? div(
                    {class: "text-xs flex items-center gap-1.5"},
                    prepareCopy.prefix ? span({class: prepareCopy.class}, prepareCopy.prefix) : null,
                    a({
                        class: `underline hover:text-white cursor-pointer ${prepareCopy.class}`,
                        onclick: () => onShowPrepareOutput(deployment.id),
                    }, "prepare"),
                    span({class: prepareCopy.class}, prepareCopy.text),
                )
                : null,
            versionError
                ? div({class: "text-xs text-red-400"}, versionError)
                : null,
            scopeSelect
                ? div({class: "flex gap-2 items-center"},
                    div({class: "flex-1"}, scopeSelect),
                  )
                : null,
            div(
                {class: "flex gap-2 items-center"},
                div({class: "flex-1"}, versionSelect),
                deployBtn,
            ),
        ),
    );
}
