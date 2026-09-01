export const DEPLOYMENT_EVENT_DELETE = 3;

export function deploymentDeleted(config) {
    return Number(config?.eventType || 0) === DEPLOYMENT_EVENT_DELETE;
}

export function containerWorkload(config) {
    return config?.def?.spec?.container1Spec || null;
}

export function deploymentWorkload(config) {
    const container = containerWorkload(config);
    if (container) return container;
    const opendeploy = config?.def?.spec?.opendeploySpec;
    if (opendeploy) return {...opendeploy, running: true};
    return null;
}
