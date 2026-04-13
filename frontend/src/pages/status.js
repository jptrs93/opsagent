import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {deploymentsS, deploymentsStreamS, versionsS} from "../state/deployments.js";
import {statusCard} from "../components/statusCard.js";
import {deploymentLogs} from "../components/deploymentLogs.js";
import {deploymentHistory} from "../components/deploymentHistory.js";

const { div, h1, h2, p } = van.tags;

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

export function statusPage() {
    const statuses = van.state([]);
    const selectedScope = van.state({});
    const sidebarMode = van.state(SIDEBAR_NONE);
    const sidebarDeploymentId = van.state(null);
    const sidebarLabel = van.state('');
    let activeSidebarAbort = null;

    // Derive scopes and versions from the pushed versionsS state.
    const getScopesForDeployment = (depId) => {
        const entry = versionsS.val.get(depId);
        return entry?.scopes || [];
    };

    const getVersionsForDeployment = (depId, scope) => {
        const entry = versionsS.val.get(depId);
        if (!entry?.versionsByScope) return [];
        const scoped = entry.versionsByScope[scope || ''];
        return scoped?.versions || [];
    };

    const onScopeChange = (deployment, scope) => {
        const depKey = deployment.id;
        selectedScope.val = {...selectedScope.val, [depKey]: scope};

        // If we don't have versions for this scope yet, nudge the backend to poll it.
        const existing = getVersionsForDeployment(depKey, scope);
        if (existing.length === 0) {
            capi.postV1VersionNudge({deploymentId: depKey, scope: scope || ''}).catch(() => {});
        }
    };

    // Auto-select scope based on currently deployed version when version data arrives.
    van.derive(() => {
        const versions = versionsS.val;
        const currentStatuses = mapDeploymentsToView(deploymentsS.val);
        statuses.val = currentStatuses;

        for (const s of currentStatuses) {
            if (!s.variant) continue;
            const depId = s.id;
            // Only auto-select if not already set by the user.
            if (selectedScope.val[depId] !== undefined) continue;

            const entry = versions.get(depId);
            if (!entry) continue;
            const scopes = entry.scopes || [];

            // Try to find which scope contains the currently deployed version.
            let bestScope = '';
            if (s.deployedVersion && entry.versionsByScope) {
                for (const [scope, sv] of Object.entries(entry.versionsByScope)) {
                    if (sv?.versions?.some(v => v.id === s.deployedVersion)) {
                        bestScope = scope;
                        break;
                    }
                }
            }
            // Fall back to 'main' or first scope.
            if (!bestScope && scopes.length > 0) {
                bestScope = scopes.includes('main') ? 'main' : scopes[0];
            }

            if (bestScope || scopes.length === 0) {
                selectedScope.val = {...selectedScope.val, [depId]: bestScope};
            }
        }
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
    };

    const onShowRunOutput = (deployment) => openSidebar(deployment, SIDEBAR_RUN);
    const onShowHistory = (deployment) => openSidebar(deployment, SIDEBAR_HISTORY);
    const onShowPrepareOutput = (deployment) => openSidebar(deployment, SIDEBAR_PREPARE);

    const mainContent = div(
        {class: "flex-1 min-h-0 overflow-auto p-6 flex flex-col gap-6"},
        h1({class: "text-xl font-bold"}, "Deployments"),
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

            // Re-read versionsS inside the closure so VanJS tracks the dependency.
            const versions = versionsS.val;

            const envMap = {};
            for (const s of filtered) {
                const env = s.environment || 'Unknown';
                if (!envMap[env]) envMap[env] = [];
                envMap[env].push(s);
            }

            const envEntries = Object.entries(envMap).sort(([a], [b]) => {
                const aSystem = a === 'OPSAGENT_SYSTEM' ? 1 : 0;
                const bSystem = b === 'OPSAGENT_SYSTEM' ? 1 : 0;
                return aSystem - bSystem || a.localeCompare(b);
            });

            return div(
                {class: "flex flex-col gap-6"},
                ...envEntries.map(([envName, deployments]) =>
                    div(
                        {class: "card"},
                        h2({class: envName === 'OPSAGENT_SYSTEM'
                            ? "text-xs text-gray-500 mb-4"
                            : "text-lg font-semibold mb-4"}, envName),
                        div(
                            {class: "flex flex-wrap gap-3"},
                            ...deployments.map(s => {
                                const scope = selectedScope.val[s.id] || '';
                                const depVersions = getVersionsForDeployment(s.id, scope);
                                const depScopes = getScopesForDeployment(s.id);
                                return statusCard(
                                    s,
                                    depVersions,
                                    null,
                                    depScopes,
                                    scope,
                                    onScopeChange,
                                    onDeploy,
                                    onStop,
                                    onShowHistory,
                                    onShowRunOutput,
                                    onShowPrepareOutput,
                                );
                            })
                        )
                    )
                )
            );
        }
    );

    return div(
        {class: "flex h-full min-h-0 overflow-hidden"},
        mainContent,
        () => {
            const mode = sidebarMode.val;
            const depId = sidebarDeploymentId.val;
            if (!mode || !depId) return div();

            const label = sidebarLabel.val;
            if (mode === SIDEBAR_HISTORY) {
                return deploymentHistory(depId, closeSidebar);
            }
            abortActiveSidebar();
            const ac = new AbortController();
            activeSidebarAbort = ac;
            return deploymentLogs(depId, label, mode, ac, closeSidebar);
        },
    );
}
