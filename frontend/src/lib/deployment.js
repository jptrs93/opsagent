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

export function schedulingGeneration(config) {
    return Number(config?.value?.scheduling?.generation || 0);
}

export function deploymentRestartEvent(config, prevConfig) {
    if (!config || !prevConfig) return false;
    if (deploymentDeleted(config) || deploymentDeleted(prevConfig)) return false;
    return schedulingGeneration(config) !== schedulingGeneration(prevConfig)
        && Number(config.specVersion || 0) === Number(prevConfig.specVersion || 0)
        && (config.value?.name || '') === (prevConfig.value?.name || '')
        && Number(config.value?.spaceId || 0) === Number(prevConfig.value?.spaceId || 0)
        && desiredRunning(config) === desiredRunning(prevConfig)
        && placementNodeId(config) === placementNodeId(prevConfig);
}
