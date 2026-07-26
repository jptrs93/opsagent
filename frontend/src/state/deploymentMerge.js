export const mergeDeploymentState = (configs, instances) => {
    const newestByDeployment = new Map();
    for (const state of instances.values()) {
        const instance = state?.instance;
        if (!instance?.id || instance.state === 2) continue;
        const deploymentId = Number(instance.deploymentId || state?.config?.id || 0);
        if (!deploymentId) continue;
        const current = newestByDeployment.get(deploymentId);
        if (!current || instance.id > current.instance.id) {
            newestByDeployment.set(deploymentId, state);
        }
    }

    const rows = [];
    for (const config of configs.values()) {
        if (!config?.id || config.deleted) continue;
        const runtime = newestByDeployment.get(Number(config.id));
        rows.push({
            instance: runtime?.instance,
            config,
            pinnedConfig: runtime?.config,
            status: runtime?.status,
        });
    }
    return rows.sort((a, b) => Number(a.config.id) - Number(b.config.id));
};

export const applyScheduledInstanceUpdate = (instances, update) => {
    const next = new Map(instances);
    const id = Number(update?.instance?.id || 0);
    if (!id) return next;
    if (update.instance.state === 2) {
        next.delete(id);
    } else {
        next.set(id, update);
    }
    return next;
};
