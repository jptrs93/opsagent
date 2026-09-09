import {deploymentDeleted} from '../lib/deployment.js';

const latest = versions => [...versions.values()].reduce((a, b) => !a || b.version > a.version ? b : a, undefined);

// One read rule for both browser rows and their retained version pins. An
// ordinal uses its live placements; without any, it uses its newest final run.
export function selectInstanceEvents(tree) {
    const ordinals = new Map();
    for (const event of tree.scheduledInstances.values()) {
        const value = event.value;
        const key = `${value.deploymentId}:${value.instanceOrdinal || 0}`;
        const group = ordinals.get(key) || {live: [], final: undefined};
        if (value.state === 2) {
            if (!group.final || event.scheduledInstanceId > group.final.scheduledInstanceId) group.final = event;
        } else group.live.push(event);
        ordinals.set(key, group);
    }
    const result = [];
    for (const group of ordinals.values()) {
        if (group.live.length) result.push(...group.live);
        else if (group.final) {
            const versions = tree.deployments.get(group.final.value.deploymentId);
            if (!versions || !deploymentDeleted(latest(versions))) result.push(group.final);
        }
    }
    return result.sort((a, b) => a.scheduledInstanceId - b.scheduledInstanceId);
}

export function deriveDeploymentRows(tree) {
    const instances = new Map();
    for (const event of selectInstanceEvents(tree)) {
        const value = event.value;
        const config = tree.deployments.get(value.deploymentId)?.get(value.deploymentVersion);
        const state = {
            instance: {...value, id: event.scheduledInstanceId},
            config,
            status: tree.instanceStatuses.get(event.scheduledInstanceId),
        };
        const group = instances.get(value.deploymentId) || [];
        group.push(state);
        instances.set(value.deploymentId, group);
    }
    const rows = [];
    for (const [id, versions] of tree.deployments) {
        const config = latest(versions);
        if (deploymentDeleted(config)) continue;
        const scheduledInstances = instances.get(id) || [];
        const runtime = scheduledInstances.at(-1);
        rows.push({config, scheduledInstances, instance: runtime?.instance, status: runtime?.status, pinnedConfig: runtime?.config});
    }
    return rows.sort((a, b) => a.config.deploymentId - b.config.deploymentId);
}
