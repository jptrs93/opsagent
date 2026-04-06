import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {clusterConfigS, deploymentsS, deploymentsStreamS, desiredStatesS, usersS} from "../state/deployments.js";
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

const splitDeploymentKey = (key) => {
    const idx = key.indexOf(':');
    if (idx === -1) {
        return { machine: 'unknown', name: key || 'unknown' };
    }
    return {
        machine: key.slice(0, idx) || 'unknown',
        name: key.slice(idx + 1) || 'unknown',
    };
};

// mergedEnvironments returns environments from both user and system
// sections of the ClusterConfig. System entries are always appended so the
// OPSAGENT env shows up alongside user-defined envs.
const mergedEnvironments = (cc) => {
    const out = [];
    for (const env of (cc?.userConfig?.environments || [])) {
        if (env) out.push(env);
    }
    for (const env of (cc?.systemConfig?.environments || [])) {
        if (env) out.push(env);
    }
    return out;
};

const mapDeploymentsToView = (deployments, cc, desiredStates, users) => {
    if (!Array.isArray(deployments)) return [];

    // Key is <machine>:<name>. Config lookup needs environment info per
    // deployment (for display grouping) so we index by that same key.
    const configByKey = new Map();
    const userById = new Map((users || []).map((user) => [user.id, user.name]));
    for (const env of mergedEnvironments(cc)) {
        for (const dep of (env.deployments || [])) {
            if (!dep || !dep.machine || !dep.name) continue;
            const depKey = `${dep.machine}:${dep.name}`;
            let variant = '';
            let repo = '';
            if (dep.prepare?.nixBuild) {
                variant = 'nixBuild';
                repo = dep.prepare.nixBuild.repo || '';
            } else if (dep.prepare?.githubRelease) {
                variant = 'githubRelease';
                repo = dep.prepare.githubRelease.repo || '';
            }
            configByKey.set(depKey, {variant, repo, environment: env.name, machine: dep.machine});
        }
    }

    return deployments.map((d) => {
        const { machine, name } = splitDeploymentKey(d.key || '');
        const key = `${machine}:${name}`;
        const running = d.runningStatus || {};
        const desired = desiredStates[key] || {};
        const prep = d.preparation || {};
        const updatedBy = desired.updatedBy || 0;
        const info = configByKey.get(key) || {variant: '', repo: '', environment: 'Unknown', machine};
        return {
            key,
            name,
            machine,
            environment: info.environment,
            variant: info.variant,
            repo: info.repo,
            existingStatus: running.status || 0,
            existingVersion: running.version || '',
            numberOfRestarts: running.numberOfRestarts || 0,
            lastRestartAt: running.lastRestartAt,
            deployedBy: userById.get(updatedBy) || (updatedBy ? `User ${updatedBy}` : ''),
            deployedAt: desired.updatedAt,
            deployedVersion: desired.version || '',
            prepareStatus: prep.status || 0,
            prepareVersion: desired.version || '',
            currentSeqNo: desired.seqNo || 0,
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
    const sidebarVersion = van.state(null);

    const ensurePreparerDataLoaded = (currentStatuses) => {
        for (const s of currentStatuses || []) {
            if (!s.variant) continue;
            loadScopesForDeployment(s);
        }
    };

    const loadScopesForDeployment = async (deployment) => {
        const depKey = deployment.key;
        if (scopesMap.val[depKey] !== undefined) {
            // Already loaded; still make sure versions are fetched for the current scope.
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
            // Still attempt to fetch versions without a scope (e.g. github releases).
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
        const currentStatuses = mapDeploymentsToView(deploymentsS.val, clusterConfigS.val, desiredStatesS.val, usersS.val);
        statuses.val = currentStatuses;
        ensurePreparerDataLoaded(currentStatuses);
    });

    const closeSidebar = () => {
        sidebarMode.val = SIDEBAR_NONE;
        sidebarDeployment.val = null;
        sidebarVersion.val = null;
    };

    const onDeploy = async (deploymentName, environment, version, seqNo, machine) => {
        try {
            await capi.postV1DeploymentUpdate({deploymentName, environment, targetVersion: version, seqNo: seqNo + 1});
            sidebarMode.val = SIDEBAR_PREPARE;
            sidebarDeployment.val = `${machine}:${deploymentName}`;
            sidebarVersion.val = version;
        } catch (e) {
            alert(`Deploy failed: ${e.message}`);
        }
    };

    const onStop = async (deploymentName, environment, seqNo) => {
        try {
            await capi.postV1DeploymentUpdate({deploymentName, environment, stop: true, seqNo: seqNo + 1});
        } catch (e) {
            alert(`Stop failed: ${e.message}`);
        }
    };

    const onShowRunOutput = (key, version) => {
        sidebarMode.val = SIDEBAR_RUN;
        sidebarDeployment.val = key;
        sidebarVersion.val = version;
    };

    const onShowHistory = (key) => {
        sidebarMode.val = SIDEBAR_HISTORY;
        sidebarDeployment.val = key;
    };

    const onShowPrepareOutput = (key, version) => {
        sidebarMode.val = SIDEBAR_PREPARE;
        sidebarDeployment.val = key;
        sidebarVersion.val = version;
    };

    const configDeploymentKeys = () => {
        const keys = new Set();
        for (const env of mergedEnvironments(clusterConfigS.val)) {
            for (const dep of (env.deployments || [])) {
                if (dep?.machine && dep?.name) {
                    keys.add(`${dep.machine}:${dep.name}`);
                }
            }
        }
        return keys;
    };

    const mainContent = div(
        {class: "flex-1 min-h-0 overflow-auto p-6 flex flex-col gap-6"},
        h1({class: "text-xl font-bold"}, "Deployments"),
        () => {
            if (clusterConfigS.val === null && deploymentsStreamS.val.status !== 'connected') {
                return p({class: "text-gray-400"}, deploymentsStreamS.val.sentence);
            }

            const validKeys = configDeploymentKeys();
            const filtered = statuses.val.filter(s => validKeys.has(s.key));

            if (validKeys.size > 0 && filtered.length === 0 && deploymentsStreamS.val.status !== 'connected') {
                return div(
                    {class: "card"},
                    p({class: "text-gray-400"}, deploymentsStreamS.val.sentence)
                );
            }

            if (filtered.length === 0) {
                return div(
                    {class: "card"},
                    p(
                        {class: "text-gray-400"},
                        validKeys.size === 0
                            ? "No deployments configured. Create a deployment config first."
                            : "No deployment status available yet."
                    )
                );
            }

            const envMap = {};
            for (const s of filtered) {
                const env = s.environment || 'Unknown';
                if (!envMap[env]) envMap[env] = [];
                envMap[env].push(s);
            }

            return div(
                {class: "flex flex-col gap-6"},
                ...Object.entries(envMap).map(([envName, deployments]) =>
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
                return prepareOutput(sidebarDeployment.val, sidebarVersion.val, closeSidebar);
            }
            if (sidebarMode.val === SIDEBAR_RUN && sidebarDeployment.val) {
                return runOutput(sidebarDeployment.val, sidebarVersion.val, closeSidebar);
            }
            if (sidebarMode.val === SIDEBAR_HISTORY && sidebarDeployment.val) {
                return deploymentHistory(sidebarDeployment.val, closeSidebar);
            }
            return div();
        },
    );
}
