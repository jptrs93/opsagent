import {nodeDisplayName} from "./machines.js";

export function deploymentUsages(deployments, spaces, machines, usesDeployment) {
    const spaceNames = new Map((spaces || []).map(space => [Number(space.id || 0), space.name]));

    return (deployments || []).flatMap(deployment => {
        const config = deployment?.config;
        if (!config || config.deleted || !usesDeployment(deployment)) return [];

        const identity = config.configId || {};
        const spaceId = Number(identity.spaceId || 0);
        return [{
            id: Number(config.id || 0),
            space: spaceNames.get(spaceId) || `space ${spaceId}`,
            name: identity.name || `deployment ${config.id}`,
            node: nodeDisplayName(config.nodeId, machines),
        }];
    }).sort((a, b) => a.space.localeCompare(b.space)
        || a.name.localeCompare(b.name)
        || a.node.localeCompare(b.node)
        || a.id - b.id);
}
