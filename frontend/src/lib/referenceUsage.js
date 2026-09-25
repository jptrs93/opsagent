import {nodeDisplayName} from "./machines.js";
import {deploymentDeleted, placementNodeId} from "./deployment.js";

export function deploymentUsages(deployments, spaces, machines, usesDeployment) {
    const spaceNames = new Map((spaces || []).map(space => [Number(space.id || 0), space.name]));

    return (deployments || []).flatMap(deployment => {
        const config = deployment?.config;
        if (!config || deploymentDeleted(config) || !usesDeployment(deployment)) return [];

        const spaceId = Number(config.value?.spaceId || 0);
        return [{
            id: Number(config.deploymentId || 0),
            space: spaceNames.get(spaceId) || `space ${spaceId}`,
            name: config.value?.name || `deployment ${config.deploymentId}`,
            node: nodeDisplayName(placementNodeId(config), machines),
        }];
    }).sort((a, b) => a.space.localeCompare(b.space)
        || a.name.localeCompare(b.name)
        || a.node.localeCompare(b.node)
        || a.id - b.id);
}

export function deploymentUsesEnvReferences(config, type, entityID) {
    const referenceKey = type === "secret" ? "secret" : "config";
    const envVars = config?.value?.spec?.container1Spec?.runtime?.envVars;
    return Boolean(envVars && Object.values(envVars).some(
        value => Number(value?.[referenceKey]?.id || 0) === Number(entityID),
    ));
}
