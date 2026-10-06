import {nodeDisplayName} from "./machines.js";
import {containerWorkload, deploymentDeleted, deploymentId, deploymentOf, placementNodeId} from "./deployment.js";

export function deploymentUsages(deployments, spaces, machines, usesDeployment) {
    const spaceNames = new Map((spaces || []).map(space => [Number(space.id || 0), space.name]));

    return (deployments || []).flatMap(item => {
        const record = item?.config || item;
        const deployment = deploymentOf(record);
        if (!deployment || deploymentDeleted(record) || !usesDeployment(item)) return [];

        const spaceId = Number(deployment.spaceId || 0);
        const id = deploymentId(record);
        return [{
            id,
            space: spaceNames.get(spaceId) || `space ${spaceId}`,
            name: deployment.name || `deployment ${id}`,
            node: nodeDisplayName(placementNodeId(record), machines),
        }];
    }).sort((a, b) => a.space.localeCompare(b.space)
        || a.name.localeCompare(b.name)
        || a.node.localeCompare(b.node)
        || a.id - b.id);
}

const ENV_REFERENCE_ID = {
    secret: envVar => envVar?.value?.secret?.secret?.secretId,
    config: envVar => envVar?.value?.config?.config?.configId,
    asset: envVar => envVar?.value?.asset?.asset?.assetId,
    address: envVar => envVar?.value?.address?.deploymentId,
};

export function deploymentUsesEnvReferences(record, type, entityID) {
    const referenceId = ENV_REFERENCE_ID[type];
    const envVars = containerWorkload(record?.config || record)?.runtime?.envVars;
    return Boolean(referenceId && envVars && Object.values(envVars).some(
        envVar => Number(referenceId(envVar) || 0) === Number(entityID),
    ));
}
