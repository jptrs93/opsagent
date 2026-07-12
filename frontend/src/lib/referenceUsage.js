export function deploymentUsages(deployments, spaces, usesDeployment) {
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
            machine: identity.machine || '-',
        }];
    }).sort((a, b) => a.space.localeCompare(b.space)
        || a.name.localeCompare(b.name)
        || a.machine.localeCompare(b.machine)
        || a.id - b.id);
}
