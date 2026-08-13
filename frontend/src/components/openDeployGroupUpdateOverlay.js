import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {refreshIcon} from "../lib/icons.js";
import {spinnerButton} from "./spinnerbutton.js";
import {deploymentsS} from "../state/deployments.js";
import {deploymentWorkload} from "../lib/deploymentConfig.js";
import {preparerPhase} from "../lib/preparerStatus.js";

const {button, div, input, label, option, p, select, span, table, thead, tbody, tr, th, td} = van.tags;

const RUNNER_RUNNING = 2;
const RUNNER_CRASHED = 5;
const POLL_INTERVAL_MS = 1000;
// Generous per-node budget: an agent upgrade downloads a release binary and
// restarts the process; a netproxy upgrade downloads and imports an image.
const CONVERGENCE_TIMEOUT_MS = 10 * 60 * 1000;

const sleep = (ms) => new Promise(resolve => setTimeout(resolve, ms));

// openDeployGroupUpdateOverlay upgrades one internal system deployment group
// (opendeploy or opendeploy-net) across the cluster. The browser orchestrates
// the rollout itself: it updates one member at a time in group.members order —
// secondaries first, primary last — waits for each node's runner to report the
// new version before moving on, and halts on the first failure so a broken
// release never reaches the remaining nodes (in particular the primary).
export function openDeployGroupUpdateOverlay(group, onClose) {
    const members = group.members || [];
    const primaryMember = members.find(member => member.isPrimaryNode) || members[members.length - 1];

    const releases = van.state([]);
    const loading = van.state(false);
    const requestDescription = van.state('');
    const versionError = van.state('');
    const updateError = van.state('');
    const alignVersions = van.state(true);
    const running = van.state(false);
    const finished = van.state(false);
    // Set when the overlay is dismissed mid-rollout: the loop stops before the
    // next member; the member already posted keeps converging server-side.
    const aborted = {val: false};

    const targetByMember = new Map(members.map(member => [member.id, van.state(member.deployedVersion || '')]));
    const phaseByMember = new Map(members.map(member => [member.id, van.state({state: 'idle', detail: ''})]));
    // Members whose target the user explicitly picked. With "Align versions"
    // off, the rollout only touches these: untouched members keep whatever
    // version they are on, so per-node canary upgrades opened one node at a
    // time never revert a node another rollout just upgraded.
    const touchedMembers = new Set();

    const applyAlignedVersions = () => {
        const primaryTarget = targetByMember.get(primaryMember.id).val;
        for (const member of members) {
            targetByMember.get(member.id).val = primaryTarget;
        }
    };

    const loadReleases = async () => {
        loading.val = true;
        requestDescription.val = 'Loading available versions.';
        versionError.val = '';
        updateError.val = '';
        try {
            const response = await capi.postV1DeploymentsVersions({deploymentId: primaryMember.id});
            if (Number(response?.deploymentId || 0) !== Number(primaryMember.id || 0)) {
                throw new Error('Version response did not attest the requested deployment.');
            }
            if (!response.githubRelease) {
                throw new Error('Version response did not include GitHub releases.');
            }
            const nextReleases = response.githubRelease.releases || [];
            releases.val = nextReleases;
            for (const member of members) {
                const current = targetByMember.get(member.id);
                if (!nextReleases.some(release => release.id === current.val)) {
                    current.val = nextReleases[0]?.id || '';
                }
            }
            if (alignVersions.val) applyAlignedVersions();
        } catch (error) {
            releases.val = [];
            versionError.val = error.message || 'Failed to load available versions.';
        } finally {
            loading.val = false;
            requestDescription.val = '';
        }
    };

    const findLive = (deploymentId) => (deploymentsS.rawVal || [])
        .find(d => Number(d.config?.id || 0) === Number(deploymentId)) || null;

    // waitForConvergence watches the state stream until the member's runner
    // reports RUNNING at the target version for the applied config version, or
    // the prepare/run of that config fails. The stream survives the primary's
    // own restart via its reconnect loop, so the primary-last step resolves
    // once the restarted agent reattaches and reports in.
    const waitForConvergence = async (deploymentId, targetVersion, appliedConfigVersion) => {
        const deadline = Date.now() + CONVERGENCE_TIMEOUT_MS;
        while (Date.now() < deadline) {
            if (aborted.val) return {ok: false, detail: 'cancelled'};
            const live = findLive(deploymentId);
            if (live) {
                const runner = live.status?.runner || {};
                const preparer = live.status?.preparer || {};
                const runnerCurrent = Number(runner.deploymentConfigVersion || 0) >= appliedConfigVersion;
                if (runnerCurrent && runner.status === RUNNER_RUNNING && (runner.runningVersion || '') === targetVersion) {
                    return {ok: true};
                }
                if (runnerCurrent && runner.status === RUNNER_CRASHED) {
                    return {ok: false, detail: 'runner crashed on the new version'};
                }
                if (Number(preparer.deploymentConfigVersion || 0) >= appliedConfigVersion
                    && preparerPhase(preparer)?.tone === 'failed') {
                    return {ok: false, detail: 'prepare failed for the new version'};
                }
            }
            await sleep(POLL_INTERVAL_MS);
        }
        return {ok: false, detail: 'timed out waiting for the node to report the new version'};
    };

    const runUpgrade = async () => {
        if (running.val) return;
        updateError.val = '';
        running.val = true;
        try {
            for (const member of members) {
                if (aborted.val) break;
                const phase = phaseByMember.get(member.id);
                if (!alignVersions.val && !touchedMembers.has(member.id)) {
                    phase.val = {state: 'skipped', detail: 'not selected'};
                    continue;
                }
                const target = targetByMember.get(member.id).val;
                if (!target) {
                    phase.val = {state: 'failed', detail: 'no version selected'};
                    updateError.val = `Upgrade halted: no version selected for ${member.node}.`;
                    return;
                }
                const live = findLive(member.id);
                if (!live?.config) {
                    phase.val = {state: 'failed', detail: 'deployment not found'};
                    updateError.val = `Upgrade halted: deployment for ${member.node} no longer exists.`;
                    return;
                }
                const workload = deploymentWorkload(live.config) || {};
                if (workload.version === target && workload.running) {
                    phase.val = {state: 'skipped', detail: 'already at this version'};
                    continue;
                }
                phase.val = {state: 'updating', detail: ''};
                let applied;
                try {
                    applied = await capi.postV1DeploymentsUpdate({
                        deploymentId: member.id,
                        version: (live.config.version || 0) + 1,
                        targetVersion: target,
                    });
                } catch (error) {
                    phase.val = {state: 'failed', detail: error?.message || 'update request failed'};
                    updateError.val = `Upgrade halted: updating ${member.node} failed.`;
                    return;
                }
                phase.val = {state: 'waiting', detail: ''};
                const result = await waitForConvergence(member.id, target, Number(applied?.version || 0));
                if (!result.ok) {
                    phase.val = {state: 'failed', detail: result.detail};
                    updateError.val = `Upgrade halted: ${member.node} did not reach ${target} (${result.detail}).`;
                    return;
                }
                phase.val = {state: 'done', detail: ''};
            }
        } finally {
            running.val = false;
            finished.val = true;
        }
    };

    const close = () => {
        aborted.val = true;
        onClose();
    };

    const phaseCell = (member) => {
        const phase = phaseByMember.get(member.id);
        return () => {
            const {state, detail} = phase.val;
            switch (state) {
                case 'idle':
                    return span({class: 'text-gray-500'}, running.val ? 'pending' : '-');
                case 'skipped':
                    return span({class: 'text-gray-400'}, detail || 'skipped');
                case 'updating':
                case 'waiting':
                    return span(
                        {class: 'inline-flex items-center gap-2 text-blue-300'},
                        span({class: 'w-[1em] h-[1em] border-[0.15em] border-gray-500/30 border-t-blue-300 rounded-full animate-spin'}),
                        state === 'updating' ? 'updating' : 'waiting for node',
                    );
                case 'done':
                    return span({class: 'text-green-300'}, 'done');
                case 'failed':
                    return span({class: 'text-red-400'}, detail || 'failed');
                default:
                    return span({class: 'text-gray-500'}, '-');
            }
        };
    };

    const versionSelect = (member) => {
        const target = targetByMember.get(member.id);
        const isPrimaryRow = member.id === primaryMember.id;
        return () => {
            const selectionLoaded = releases.val.some(release => release.id === target.val);
            const greyedOut = alignVersions.val && !isPrimaryRow;
            return select({
                class: `w-full h-8 px-2 rounded-sm bg-gray-800 text-gray-100 border border-gray-700 focus:outline-none focus:ring-1 focus:ring-brand ${greyedOut ? 'opacity-50' : ''}`,
                'data-testid': `deployment-target-version-${group.name}-${member.node}`,
                value: selectionLoaded ? target.val : '',
                disabled: greyedOut || running.val || finished.val || loading.val || Boolean(versionError.val) || releases.val.length === 0,
                onchange: event => {
                    target.val = event.target.value;
                    touchedMembers.add(member.id);
                    if (isPrimaryRow && alignVersions.val) applyAlignedVersions();
                },
            },
                option(
                    {value: '', disabled: true, selected: !selectionLoaded},
                    loading.val ? 'Loading releases...' : (releases.val.length ? 'Select a release...' : 'No releases loaded'),
                ),
                ...releases.val.map(release => option(
                    {value: release.id, selected: release.id === target.val},
                    versionLabel(release),
                )),
            );
        };
    };

    const memberRow = (member) => tr(
        {class: 'border-b border-gray-800 last:border-0', 'data-testid': `deployment-upgrade-row-${group.name}-${member.node}`},
        td(
            {class: 'py-2 pl-3 pr-2 text-sm text-gray-200 whitespace-nowrap'},
            span({class: 'inline-flex items-center gap-1.5'},
                member.node || '-',
                member.isPrimaryNode ? span({class: 'text-[10px] uppercase tracking-wide text-gray-500'}, 'primary') : '',
            ),
        ),
        td(
            {class: 'py-2 px-2 text-sm font-mono text-gray-300 whitespace-nowrap'},
            member.existingVersion || member.deployedVersion || 'unknown',
        ),
        td({class: 'py-2 px-2'}, versionSelect(member)),
        td(
            {class: 'py-2 pl-2 pr-3 text-xs whitespace-nowrap', 'data-testid': `deployment-upgrade-status-${group.name}-${member.node}`},
            phaseCell(member),
        ),
    );

    const submitButton = spinnerButton(
        'Upgrade',
        runUpgrade,
        'btn-primary text-sm py-1.5 px-4',
        'button',
        () => loading.val || finished.val || Boolean(versionError.val) || releases.val.length === 0
            || members.some(member => (alignVersions.val || touchedMembers.has(member.id)) && !targetByMember.get(member.id).val),
    );

    void loadReleases();

    const editor = div(
        {
            class: 'bg-gray-900 border border-gray-600 rounded-lg shadow-[0_28px_90px_rgba(0,0,0,0.5)] flex flex-col overflow-hidden',
            'data-testid': 'update-deployment-dialog',
            role: 'dialog',
            'aria-modal': 'true',
            'aria-label': `Upgrade ${group.name}`,
            style: 'width: 760px; max-width: 100%; max-height: 88vh;',
        },
        div(
            {class: 'app-scroll min-w-0 overflow-auto px-3 py-3.5'},
            div(
                {class: 'flex flex-col gap-3'},
                div(
                    {class: 'flex w-full items-center text-left'},
                    span({class: 'text-xs font-semibold tracking-wide text-blue-300 whitespace-nowrap'}, `Upgrade ${group.name}`),
                    span({class: 'ml-3 h-px flex-1 bg-gradient-to-r from-gray-600/80 to-transparent'}),
                ),
                p(
                    {class: 'text-xs text-gray-400'},
                    'Nodes are upgraded one at a time in the order below, primary last. ' +
                    'The rollout waits for each node to report the new version and stops if a node fails.',
                ),
                div(
                    {class: 'flex items-center justify-between gap-3'},
                    label(
                        {class: 'flex shrink-0 items-center gap-2 text-xs text-gray-300 cursor-pointer'},
                        input({
                            type: 'checkbox',
                            'data-testid': 'align-versions-toggle',
                            checked: () => alignVersions.val,
                            disabled: () => running.val || finished.val,
                            onchange: event => {
                                alignVersions.val = event.target.checked;
                                if (alignVersions.val) applyAlignedVersions();
                            },
                        }),
                        span('Align versions'),
                    ),
                    button({
                        class: 'inline-flex h-8 items-center justify-center gap-1.5 px-3 rounded-lg text-xs text-gray-300 bg-gray-800 border border-gray-600 hover:bg-gray-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors cursor-pointer',
                        disabled: () => loading.val || running.val || finished.val,
                        onclick: loadReleases,
                        type: 'button',
                        title: 'Refresh available versions',
                    }, refreshIcon({size: 12}), 'Refresh'),
                ),
                table(
                    {class: 'w-full table-fixed text-left text-sm'},
                    thead(
                        tr(
                            {class: 'border-b border-gray-700 text-xs uppercase tracking-wide text-gray-500'},
                            th({class: 'py-2 pl-3 pr-2 font-medium', style: 'width:11rem'}, 'Node'),
                            th({class: 'py-2 px-2 font-medium', style: 'width:8rem'}, 'Current'),
                            th({class: 'py-2 px-2 font-medium'}, 'Target version'),
                            th({class: 'py-2 pl-2 pr-3 font-medium', style: 'width:13rem'}, 'Status'),
                        ),
                    ),
                    tbody(...members.map(memberRow)),
                ),
                () => versionError.val || updateError.val
                    ? p(
                        {class: 'text-xs text-red-400', 'data-testid': 'deployment-version-error'},
                        versionError.val || updateError.val,
                    )
                    : '',
                () => finished.val && !updateError.val
                    ? p({class: 'text-xs text-green-300', 'data-testid': 'deployment-upgrade-complete'}, 'Upgrade complete.')
                    : '',
            ),
        ),
        div(
            {class: 'flex shrink-0 items-center justify-between gap-3 bg-gray-950/90 px-4 py-2.5'},
            requestStatus(requestDescription),
            div(
                {class: 'flex min-w-0 items-center justify-end gap-3'},
                button({
                    class: 'text-sm text-gray-400 hover:text-gray-200 cursor-pointer px-3 py-1.5',
                    onclick: close,
                    type: 'button',
                }, () => finished.val ? 'Close' : (running.val ? 'Stop and close' : 'Cancel')),
                () => finished.val ? '' : submitButton,
            ),
        ),
    );

    // No backdrop-click dismissal: a stray click must not silently stop a
    // rollout that is mid-flight.
    return div(
        div({class: 'fixed inset-0 bg-black/60 z-40'}),
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
