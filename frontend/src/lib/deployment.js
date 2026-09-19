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
    return config?.value?.spec?.opendeploySpec || null;
}

export function placementNodeId(config) {
    return Number(config?.value?.scheduling?.dedicatedNodes?.nodes?.[0] || 0);
}

export function desiredRunning(config) {
    return Boolean(config?.value?.scheduling?.running);
}

export function dedicatedScheduling(running, nodeId) {
    const id = Number(nodeId || 0);
    return {running: Boolean(running), dedicatedNodes: {nodes: id ? [id] : []}};
}

export function deploymentRestartEvent(config, prevConfig) {
    if (!config || !prevConfig) return false;
    if (deploymentDeleted(config) || deploymentDeleted(prevConfig)) return false;
    return Number(config.specVersion || 0) === Number(prevConfig.specVersion || 0)
        && Number(config.spaceVersion || 0) === Number(prevConfig.spaceVersion || 0)
        && Number(config.nameVersion || 0) === Number(prevConfig.nameVersion || 0)
        && Number(config.schedulingVersion || 0) === Number(prevConfig.schedulingVersion || 0);
}
