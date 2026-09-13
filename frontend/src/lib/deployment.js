export const DEPLOYMENT_EVENT_DELETE = 3;

export function deploymentDeleted(config) {
    return Number(config?.eventType || 0) === DEPLOYMENT_EVENT_DELETE;
}

export function containerWorkload(config) {
    return config?.value?.spec?.container1Spec || null;
}

export function deploymentWorkload(config) {
    const container = containerWorkload(config);
    if (container) return container;
    const opendeploy = config?.value?.spec?.opendeploySpec;
    if (opendeploy) return {...opendeploy, running: true};
    return null;
}

export function deploymentRestartEvent(config, prevConfig) {
    if (!config || !prevConfig) return false;
    if (deploymentDeleted(config) || deploymentDeleted(prevConfig)) return false;
    return Number(config.specVersion || 0) === Number(prevConfig.specVersion || 0)
        && Number(config.spaceVersion || 0) === Number(prevConfig.spaceVersion || 0)
        && Number(config.nameVersion || 0) === Number(prevConfig.nameVersion || 0);
}
