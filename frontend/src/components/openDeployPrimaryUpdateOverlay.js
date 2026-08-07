import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {refreshIcon} from "../lib/icons.js";
import {spinnerButton} from "./spinnerbutton.js";

const {button, div, input, label, option, p, select, span} = van.tags;

export function openDeployPrimaryUpdateOverlay(deployment, onClose) {
    const releases = van.state([]);
    const selectedRelease = van.state(deployment.deployedVersion || '');
    const upgradeAll = van.state(false);
    const loading = van.state(false);
    const requestDescription = van.state('');
    const versionError = van.state('');
    const updateError = van.state('');

    const loadReleases = async () => {
        loading.val = true;
        requestDescription.val = 'Loading available versions.';
        versionError.val = '';
        updateError.val = '';
        try {
            const response = await capi.postV1DeploymentsVersions({deploymentId: deployment.id});
            if (Number(response?.deploymentId || 0) !== Number(deployment.id || 0)) {
                throw new Error('Version response did not attest the requested deployment.');
            }
            if (!response.githubRelease) {
                throw new Error('Version response did not include GitHub releases.');
            }
            const nextReleases = response.githubRelease.releases || [];
            const previous = selectedRelease.val;
            releases.val = nextReleases;
            selectedRelease.val = nextReleases.some(release => release.id === previous)
                ? previous
                : (nextReleases[0]?.id || '');
        } catch (error) {
            releases.val = [];
            versionError.val = error.message || 'Failed to load available versions.';
        } finally {
            loading.val = false;
            requestDescription.val = '';
        }
    };

    const submitUpdate = async () => {
        updateError.val = '';
        try {
            if (upgradeAll.val) {
                await capi.postV1DeploymentsUpgradeAll({targetVersion: selectedRelease.val});
            } else {
                await capi.postV1DeploymentsUpdate({
                    deploymentId: deployment.id,
                    version: deployment.currentVersion + 1,
                    targetVersion: selectedRelease.val,
                });
            }
            onClose();
        } catch (error) {
            updateError.val = error.message || 'Update failed.';
        }
    };

    const submitButton = spinnerButton(
        'Update deployment',
        submitUpdate,
        'btn-primary text-sm py-1.5 px-4',
        'button',
        () => loading.val || Boolean(versionError.val) || !selectedRelease.val
            || !releases.val.some(release => release.id === selectedRelease.val),
    );

    void loadReleases();

    const editor = div(
        {
            class: 'bg-gray-900 border border-gray-600 rounded-lg shadow-[0_28px_90px_rgba(0,0,0,0.5)] flex flex-col overflow-hidden',
            'data-testid': 'update-deployment-dialog',
            role: 'dialog',
            'aria-modal': 'true',
            'aria-label': 'Update primary OpenDeploy version',
            style: 'width: 1120px; max-width: 100%; max-height: 88vh;',
        },
        div(
            {class: 'app-scroll min-w-0 overflow-auto px-3 py-3.5'},
            div(
                {class: 'flex flex-col gap-2'},
                div(
                    {class: 'flex w-full items-center text-left'},
                    span({class: 'text-xs font-semibold tracking-wide text-blue-300 whitespace-nowrap'}, 'Version'),
                    span({class: 'ml-3 h-px flex-1 bg-gradient-to-r from-gray-600/80 to-transparent'}),
                ),
                div(
                    {class: 'grid grid-cols-1 items-end gap-x-3 gap-y-2 md:grid-cols-[minmax(0,1fr)_auto]'},
                    () => {
                        const selectionLoaded = releases.val.some(release => release.id === selectedRelease.val);
                        return label(
                            {class: 'flex flex-col gap-1 text-xs text-gray-400'},
                            span('Release'),
                            select({
                                class: 'w-full h-9 px-3 rounded-sm bg-gray-800 text-gray-100 border border-gray-700 focus:outline-none focus:ring-1 focus:ring-brand',
                                value: selectionLoaded ? selectedRelease.val : '',
                                disabled: loading.val || Boolean(versionError.val) || releases.val.length === 0,
                                onchange: event => { selectedRelease.val = event.target.value; },
                            },
                                option(
                                    {value: '', disabled: true, selected: !selectionLoaded},
                                    loading.val ? 'Loading releases...' : (releases.val.length ? 'Select a release...' : 'No releases loaded'),
                                ),
                                ...releases.val.map(release => option(
                                    {value: release.id, selected: release.id === selectedRelease.val},
                                    versionLabel(release),
                                )),
                            ),
                        );
                    },
                    div(
                        {class: 'flex items-end'},
                        button({
                            class: 'inline-flex h-9 items-center justify-center gap-1.5 px-3 rounded-lg text-xs text-gray-300 bg-gray-800 border border-gray-600 hover:bg-gray-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors cursor-pointer',
                            disabled: () => loading.val,
                            onclick: loadReleases,
                            type: 'button',
                            title: 'Refresh available versions',
                        }, refreshIcon({size: 12}), 'Refresh'),
                    ),
                ),
                () => versionError.val || updateError.val
                    ? p(
                        {class: 'text-xs text-red-400', 'data-testid': 'deployment-version-error'},
                        versionError.val || updateError.val,
                    )
                    : '',
            ),
        ),
        div(
            {class: 'flex shrink-0 items-center justify-between gap-3 bg-gray-950/90 px-4 py-2.5'},
            div(
                {class: 'flex min-w-0 items-center gap-4'},
                label(
                    {class: 'flex shrink-0 items-center gap-2 text-xs text-gray-300 cursor-pointer'},
                    input({
                        type: 'checkbox',
                        checked: () => upgradeAll.val,
                        onchange: event => { upgradeAll.val = event.target.checked; },
                    }),
                    span('upgrade all to this version'),
                ),
                requestStatus(requestDescription),
            ),
            div(
                {class: 'flex min-w-0 items-center justify-end gap-3'},
                button({
                    class: 'text-sm text-gray-400 hover:text-gray-200 cursor-pointer px-3 py-1.5',
                    onclick: onClose,
                    type: 'button',
                }, 'Cancel'),
                submitButton,
            ),
        ),
    );

    return div(
        div({class: 'fixed inset-0 bg-black/60 z-40', onclick: onClose}),
        div(
            {class: 'fixed inset-0 z-50 flex items-center justify-center p-4 md:p-8 pointer-events-none'},
            div({class: 'pointer-events-auto max-w-full', onclick: event => event.stopPropagation()}, editor),
        ),
    );
}

function requestStatus(requestDescription) {
    return span(
        {class: () => requestDescription.val ? 'inline-flex items-center gap-2 text-xs text-gray-400' : 'invisible text-xs'},
        span({class: 'w-[1.1em] h-[1.1em] border-[0.15em] border-gray-500/30 border-t-gray-300 rounded-full animate-spin'}),
        span(() => requestDescription.val || 'Idle'),
    );
}

function versionLabel(version) {
    const date = version.time instanceof Date && version.time.getTime() > 0
        ? version.time.toISOString().substring(0, 10)
        : '';
    const shortID = version.id.length > 7 && /^[0-9a-f]+$/i.test(version.id) ? version.id.substring(0, 7) : version.id;
    const labelText = (version.label || '').substring(0, 30);
    const ellipsis = (version.label || '').length > 30 ? '...' : '';
    return `${date}\t\t${shortID}\t\t${labelText}${ellipsis}`;
}
