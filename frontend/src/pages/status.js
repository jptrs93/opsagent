import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {deploymentsS, deploymentsStreamS, deploymentKey} from "../state/deployments.js";
import {statusCard} from "../components/statusCard.js";
import {prepareOutput} from "../components/prepareOutput.js";
import {runOutput} from "../components/runOutput.js";
import {deploymentHistory} from "../components/deploymentHistory.js";

const { div, h1, h2, p } = van.tags;

// Sidebar modes
const SIDEBAR_NONE = null;
const SIDEBAR_PREPARE = 'prepare';
const SIDEBAR_RUN = 'run';
const SIDEBAR_HISTORY = 'history';

// mapDeploymentsToView flattens DeploymentWithStatus[] into the shape
// the status card component expects.
const mapDeploymentsToView = (deployments) => {
    if (!Array.isArray(deployments)) return [];

    return deployments.filter(d => d.config && d.config.id && !d.config.deleted).map((d) => {
        const id = d.config.id;
        const key = deploymentKey(id);
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

        return {
            key,
            name: id.name,
            machine: id.machine,
            environment: id.environment,
            variant,
            repo,
            existingStatus: runner.status || 0,
            existingVersion: runner.runningArtifact || '',
            numberOfRestarts: runner.numberOfRestarts || 0,
            lastRestartAt: runner.lastRestartAt,
            deployedBy: d.config.updatedBy || 0,
            deployedAt: d.config.updatedAt,
            deployedVersion: desired.version || '',
            prepareStatus: prep.status || 0,
            prepareVersion: desired.version || '',
            currentSeqNo: d.config.seqNo || 0,
        };
    });
};

export function statusPage() {
    const statuses = van.state([]);
    const versionsMap = van.state({});
    const versionErrors = van.state({});
    const scopesMap = van.state({});
    const selectedScope = van.state({});
    const sidebarMode = van.state(SIDEBAR_NONE);
    const sidebarDeployment = van.state(null);
    const sidebarSeqNo = van.state(0);

    const ensurePreparerDataLoaded = (currentStatuses) => {
        for (const s of currentStatuses || []) {
            if (!s.variant) continue;
            loadScopesForDeployment(s);
        }
    };

    const loadScopesForDeployment = async (deployment) => {
        const depKey = deployment.key;
        if (scopesMap.val[depKey] !== undefined) {
            const scope = selectedScope.val[depKey] || '';
            loadVersionsForDeployment(deployment, scope);
            return;
        }
        try {
            const result = await capi.postV1ListScopes({
                environment: deployment.environment,
                deploymentName: deployment.name,
            });
            const scopes = result?.scopes || [];
            scopesMap.val = {...scopesMap.val, [depKey]: scopes};
            let defaultScope = selectedScope.val[depKey] || '';
            if (!defaultScope && scopes.length > 0) {
                defaultScope = scopes.includes('main') ? 'main' : scopes[0];
                selectedScope.val = {...selectedScope.val, [depKey]: defaultScope};
            }
            loadVersionsForDeployment(deployment, defaultScope);
        } catch (e) {
            console.error(`Failed to load scopes for ${depKey}:`, e.message);
            scopesMap.val = {...scopesMap.val, [depKey]: []};
            loadVersionsForDeployment(deployment, '');
        }
    };

    const loadVersionsForDeployment = async (deployment, scope) => {
        const depKey = deployment.key;
        const cacheKey = `${depKey}:${scope || ''}`;
        if (versionsMap.val[cacheKey]) return;
        try {
            const result = await capi.postV1ListVersions({
                environment: deployment.environment,
                deploymentName: deployment.name,
                scope: scope || '',
            });
            versionsMap.val = {...versionsMap.val, [cacheKey]: result?.versions || []};
        } catch (e) {
            console.error(`Failed to load versions for ${depKey}:`, e.message);
            versionsMap.val = {...versionsMap.val, [cacheKey]: []};
            versionErrors.val = {...versionErrors.val, [depKey]: 'Failed to fetch versions'};
        }
    };

    const onScopeChange = (deployment, scope) => {
        const depKey = deployment.key;
        selectedScope.val = {...selectedScope.val, [depKey]: scope};
        loadVersionsForDeployment(deployment, scope);
    };

    van.derive(() => {
        const currentStatuses = mapDeploymentsToView(deploymentsS.val);
        statuses.val = currentStatuses;
        ensurePreparerDataLoaded(currentStatuses);
    });

    const closeSidebar = () => {
        sidebarMode.val = SIDEBAR_NONE;
        sidebarDeployment.val = null;
        sidebarSeqNo.val = 0;
    };

    const onDeploy = async (deployment, version) => {
        try {
            await capi.postV1DeploymentUpdate({
                id: {environment: deployment.environment, machine: deployment.machine, name: deployment.name},
                targetVersion: version,
                seqNo: deployment.currentSeqNo + 1,
            });
            sidebarMode.val = SIDEBAR_PREPARE;
            sidebarDeployment.val = deployment.key;
            sidebarSeqNo.val = deployment.currentSeqNo;
        } catch (e) {
            alert(`Deploy failed: ${e.message}`);
        }
    };

    const onStop = async (deployment) => {
        try {
            await capi.postV1DeploymentUpdate({
                id: {environment: deployment.environment, machine: deployment.machine, name: deployment.name},
                stop: true,
                seqNo: deployment.currentSeqNo + 1,
            });
        } catch (e) {
            alert(`Stop failed: ${e.message}`);
        }
    };

    const onShowRunOutput = (key, seqNo) => {
        sidebarMode.val = SIDEBAR_RUN;
        sidebarDeployment.val = key;
        sidebarSeqNo.val = seqNo;
    };

    const onShowHistory = (key) => {
        sidebarMode.val = SIDEBAR_HISTORY;
        sidebarDeployment.val = key;
    };

    const onShowPrepareOutput = (key, seqNo) => {
        sidebarMode.val = SIDEBAR_PREPARE;
        sidebarDeployment.val = key;
        sidebarSeqNo.val = seqNo;
    };

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
                        h2({class: "text-lg font-semibold mb-4"}, envName),
                        div(
                            {class: "flex flex-wrap gap-3"},
                            ...deployments.map(s => {
                                const scope = selectedScope.val[s.key] || '';
                                const versionKey = `${s.key}:${scope}`;
                                return statusCard(
                                    s,
                                    versionsMap.val[versionKey] || [],
                                    versionErrors.val[s.key] || null,
                                    scopesMap.val[s.key] || [],
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
            if (sidebarMode.val === SIDEBAR_PREPARE && sidebarDeployment.val) {
                return prepareOutput(sidebarDeployment.val, sidebarSeqNo.val, closeSidebar);
            }
            if (sidebarMode.val === SIDEBAR_RUN && sidebarDeployment.val) {
                return runOutput(sidebarDeployment.val, sidebarSeqNo.val, closeSidebar);
            }
            if (sidebarMode.val === SIDEBAR_HISTORY && sidebarDeployment.val) {
                return deploymentHistory(sidebarDeployment.val, closeSidebar);
            }
            return div();
        },
    );
}
