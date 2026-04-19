import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {deploymentsS, deploymentsStreamS} from "../state/deployments.js";
import {statusCard} from "../components/statusCard.js";
import {deploymentLogs} from "../components/deploymentLogs.js";
import {deploymentHistory} from "../components/deploymentHistory.js";
import {deployOverlay} from "../components/deployOverlay.js";
import {createOverlay} from "../components/createOverlay.js";

const { div, h1, p, button } = van.tags;

const SIDEBAR_WIDTH_KEY = 'opsagent_sidebar_width';
const DEFAULT_SIDEBAR_PCT = 50;
const MIN_SIDEBAR_PCT = 20;
const MAX_SIDEBAR_PCT = 80;

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

// Sidebar modes
const SIDEBAR_NONE = null;
const SIDEBAR_PREPARE = 'prepare';
const SIDEBAR_RUN = 'run';
const SIDEBAR_HISTORY = 'history';

const formatDeploymentLabel = (deployment) => {
    if (!deployment) return 'unknown deployment';
    const parts = [deployment.environment, deployment.machine, deployment.name].filter(Boolean);
    return parts.length > 0 ? parts.join(' / ') : `#${deployment.id}`;
};

// mapDeploymentsToView flattens DeploymentWithStatus[] into the shape
// the status card component expects.
const mapDeploymentsToView = (deployments) => {
    if (!Array.isArray(deployments)) return [];

    return deployments.filter(d => d.config && d.config.id && !d.config.deleted).map((d) => {
        const id = d.config.id; // integer
        const cid = d.config.configId || {};
        const spec = d.config.spec || {};
        const desired = d.config.desiredState || {};
        const runner = d.status?.runner || {};
        const prep = d.status?.preparer || {};

        let variant = '';
        let repo = '';
        if (spec.prepare?.nixBuild) {
            variant = 'nixBuild';
            repo = spec.prepare.nixBuild.repo || '';
        } else if (spec.prepare?.githubRelease) {
            variant = 'githubRelease';
            repo = spec.prepare.githubRelease.repo || '';
        }

        const runnerType = spec.runner?.systemd ? 'systemd' : 'osProcess';

        return {
            id,
            name: cid.name || '',
            machine: cid.machine || '',
            environment: cid.environment || '',
            variant,
            repo,
            runnerType,
            existingStatus: runner.status || 0,
            existingVersion: runner.runningArtifact || '',
            numberOfRestarts: runner.numberOfRestarts || 0,
            lastRestartAt: runner.lastRestartAt,
            deployedBy: d.config.updatedBy || 0,
            deployedAt: d.config.updatedAt,
            deployedVersion: desired.version || '',
            prepareStatus: prep.status || 0,
            prepareVersion: desired.version || '',
            currentVersion: d.config.version || 0,
        };
    });
};

// findRawConfig finds the raw DeploymentWithStatus from deploymentsS for a given deployment ID.
const findRawConfig = (deploymentId) => {
    const all = deploymentsS.val;
    if (!Array.isArray(all)) return null;
    for (const d of all) {
        if (d.config && d.config.id === deploymentId) return d.config;
    }
    return null;
};

export function statusPage() {
    const statuses = van.state([]);
    const sidebarMode = van.state(SIDEBAR_NONE);
    const sidebarDeploymentId = van.state(null);
    const sidebarLabel = van.state('');
    const sidebarRevision = van.state(0);
    let activeSidebarAbort = null;

    // Overlay state
    const overlayDeployment = van.state(null);
    const overlayRevision = van.state(0);
    const showCreateOverlay = van.state(false);

    // Track deployments
    van.derive(() => {
        statuses.val = mapDeploymentsToView(deploymentsS.val);
    });

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

    const onDeploy = async (deployment, version) => {
        try {
            await capi.postV1DeploymentUpdate({
                deploymentId: deployment.id,
                targetVersion: version,
                version: deployment.currentVersion + 1,
            });
        } catch (e) {
            alert(`Deploy failed: ${e.message}`);
        }
    };

    const onStop = async (deployment) => {
        try {
            await capi.postV1DeploymentUpdate({
                deploymentId: deployment.id,
                stop: true,
                version: deployment.currentVersion + 1,
            });
        } catch (e) {
            alert(`Stop failed: ${e.message}`);
        }
    };

    const openSidebar = (deployment, mode) => {
        sidebarMode.val = mode;
        sidebarDeploymentId.val = deployment.id;
        sidebarLabel.val = formatDeploymentLabel(deployment);
        sidebarRevision.val++;
    };

    const onShowRunOutput = (deployment) => openSidebar(deployment, SIDEBAR_RUN);
    const onShowHistory = (deployment) => openSidebar(deployment, SIDEBAR_HISTORY);
    const onShowPrepareOutput = (deployment) => openSidebar(deployment, SIDEBAR_PREPARE);

    const onUpdate = (deployment) => {
        overlayDeployment.val = deployment;
        overlayRevision.val++;
    };

    const closeOverlay = () => {
        overlayDeployment.val = null;
    };

    const mainContent = div(
        {class: "flex flex-col gap-6"},
        div(
            {class: "flex items-center justify-between"},
            h1({class: "text-xl font-bold"}, "Deployments"),
            button({
                class: "btn-primary text-sm py-1.5 px-4 cursor-pointer",
                onclick: () => { showCreateOverlay.val = true; },
            }, "Add deployment"),
        ),
        () => {
            if (deploymentsStreamS.val.status !== 'connected' && statuses.val.length === 0) {
                return p({class: "text-gray-400"}, deploymentsStreamS.val.sentence);
            }

            const filtered = statuses.val;

            if (filtered.length === 0) {
                return div(
                    {class: "card"},
                    p(
                        {class: "text-gray-400"},
                        "No deployments configured. Create a deployment config first."
                    )
                );
            }

            // Sort: OPSAGENT_SYSTEM last, then by environment, name, machine,
            // and finally id so the order is fully deterministic across
            // stream snapshots and reconnects.
            const sorted = [...filtered].sort((a, b) => {
                const aSystem = a.environment === 'OPSAGENT_SYSTEM' ? 1 : 0;
                const bSystem = b.environment === 'OPSAGENT_SYSTEM' ? 1 : 0;
                return aSystem - bSystem
                    || (a.environment || '').localeCompare(b.environment || '')
                    || (a.name || '').localeCompare(b.name || '')
                    || (a.machine || '').localeCompare(b.machine || '')
                    || (a.id - b.id);
            });

            return div(
                {class: "flex flex-wrap gap-3"},
                ...sorted.map(s => {
                    return statusCard(
                        s,
                        onDeploy,
                        onStop,
                        onShowHistory,
                        onShowRunOutput,
                        onShowPrepareOutput,
                        onUpdate,
                    );
                })
            );
        }
    );

    let currentWidthPct = loadSidebarWidth();

    // Persistent DOM nodes — widths are updated directly during drag
    // so VanJS doesn't rebuild the sidebar on every mouse move.
    const mainPane = div(
        {class: "min-h-0 overflow-auto p-6 flex flex-col gap-6", style: "width:100%"},
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
        if (mode === SIDEBAR_HISTORY) {
            content = deploymentHistory(depId, label, closeSidebar);
        } else {
            abortActiveSidebar();
            const ac = new AbortController();
            activeSidebarAbort = ac;
            content = deploymentLogs(depId, label, mode, ac, closeSidebar);
        }

        sidebarPane.appendChild(content);
        applySidebarLayout(true);
    });

    // Overlay container — appended to body-level so it floats above everything.
    const overlayContainer = div();

    van.derive(() => {
        const dep = overlayDeployment.val;
        const _rev = overlayRevision.val;
        overlayContainer.innerHTML = '';

        if (!dep) return;

        const rawConfig = findRawConfig(dep.id);
        overlayContainer.appendChild(
            deployOverlay(dep, rawConfig, closeOverlay)
        );
    });

    // Create overlay container
    const createOverlayContainer = div();

    van.derive(() => {
        const show = showCreateOverlay.val;
        createOverlayContainer.innerHTML = '';

        if (!show) return;

        createOverlayContainer.appendChild(
            createOverlay(
                () => { showCreateOverlay.val = false; },
            )
        );
    });

    return div(
        {class: "flex h-full min-h-0 overflow-hidden"},
        mainPane,
        dividerEl,
        sidebarPane,
        overlayContainer,
        createOverlayContainer,
    );
}
