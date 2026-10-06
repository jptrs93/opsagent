// Readers over a DeploymentRecord {deployment, meta}: the shape the state
// stream, the get, history, and recently-deleted responses all hand out.

export function deploymentOf(record) {
    return record?.deployment || null;
}

export function deploymentMeta(record) {
    return record?.meta || {};
}

export function deploymentId(record) {
    return Number(record?.deployment?.id || 0);
}

export function deploymentSeq(record) {
    return Number(record?.meta?.updatedSeq || 0);
}

export function deploymentDeleted(record) {
    return Boolean(record?.meta?.deleted);
}

export const SYSTEM_SPACE_ID = 0;
export const SELF_DEPLOYMENT_NAME = 'opendeploy';

export function isSelfIdentity(spaceId, name) {
    return Number(spaceId ?? -1) === SYSTEM_SPACE_ID && name === SELF_DEPLOYMENT_NAME;
}

export function isSelfDeployment(record) {
    return isSelfIdentity(record?.deployment?.spaceId, record?.deployment?.name);
}

export function containerWorkload(record) {
    return record?.deployment?.spec?.workload?.value?.container || null;
}

export function deploymentWorkload(record) {
    return containerWorkload(record);
}

export function placementNodeId(record) {
    return Number(record?.deployment?.scheduling?.placement?.value?.dedicatedNodes?.nodes?.[0] || 0);
}

export function desiredRunning(record) {
    return Boolean(record?.deployment?.scheduling?.running);
}

export function dedicatedScheduling(running, nodeId) {
    const id = Number(nodeId || 0);
    return {running: Boolean(running), placement: {value: {dedicatedNodes: {nodes: id ? [id] : []}}}};
}

export function restartGeneration(record) {
    return Number(record?.deployment?.scheduling?.restartGeneration || 0);
}

export function deploymentRestartEvent(record, prevRecord) {
    if (!record || !prevRecord) return false;
    if (deploymentDeleted(record) || deploymentDeleted(prevRecord)) return false;
    return restartGeneration(record) !== restartGeneration(prevRecord)
        && Number(record.meta?.specVersion || 0) === Number(prevRecord.meta?.specVersion || 0)
        && (record.deployment?.name || '') === (prevRecord.deployment?.name || '')
        && Number(record.deployment?.spaceId || 0) === Number(prevRecord.deployment?.spaceId || 0)
        && desiredRunning(record) === desiredRunning(prevRecord)
        && placementNodeId(record) === placementNodeId(prevRecord);
}
