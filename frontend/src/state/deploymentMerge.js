import {deploymentDeleted} from "../lib/deployment.js";
// FINALIZED: the primary has accepted the placement is gone. It owns nothing,
// but it is still the last thing an ordinal ran, which is all a stopped
// deployment has to show.
const FINALIZED = 2;

const deploymentIdOf = (state) => Number(state?.instance?.deploymentId || state?.config?.id || 0);
const ordinalOf = (state) => Number(state?.instance?.instanceOrdinal || 0);

export const mergeDeploymentState = (configs, instances) => {
    // Group by ordinal first. Live instances are the ordinal's schedule — during
    // rollover there are several — and a finalized one counts only when the
    // ordinal has no live instance left.
    const ordinalsByDeployment = new Map();
    for (const state of instances.values()) {
        if (!state?.instance?.id) continue;
        const deploymentId = deploymentIdOf(state);
        if (!deploymentId) continue;
        let ordinals = ordinalsByDeployment.get(deploymentId);
        if (!ordinals) {
            ordinals = new Map();
            ordinalsByDeployment.set(deploymentId, ordinals);
        }
        const ordinal = ordinalOf(state);
        const group = ordinals.get(ordinal) || {live: [], final: null};
        if (state.instance.state === FINALIZED) {
            if (!group.final || Number(state.instance.id) > Number(group.final.instance.id)) {
                group.final = state;
            }
        } else {
            group.live.push(state);
        }
        ordinals.set(ordinal, group);
    }

    const instancesByDeployment = new Map();
    for (const [deploymentId, ordinals] of ordinalsByDeployment) {
        const scheduled = [];
        for (const group of ordinals.values()) {
            if (group.live.length) scheduled.push(...group.live);
            else if (group.final) scheduled.push(group.final);
        }
        scheduled.sort((a, b) => Number(a.instance.id) - Number(b.instance.id));
        instancesByDeployment.set(deploymentId, scheduled);
    }

    const rows = [];
    for (const config of configs.values()) {
        if (!config?.id || deploymentDeleted(config)) continue;
        const scheduledInstances = instancesByDeployment.get(Number(config.id)) || [];
        const runtime = scheduledInstances[scheduledInstances.length - 1];
        rows.push({
            scheduledInstances,
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
    next.set(id, update);

    // Whatever this instance replaced is no longer the ordinal's last run, so
    // drop the finalized entries it supersedes rather than accumulating one per
    // incarnation for the life of the session.
    const deploymentId = deploymentIdOf(update);
    const ordinal = ordinalOf(update);
    for (const [otherId, other] of next) {
        if (otherId === id || other?.instance?.state !== FINALIZED) continue;
        if (deploymentIdOf(other) !== deploymentId || ordinalOf(other) !== ordinal) continue;
        if (Number(other.instance.id) < id) next.delete(otherId);
    }
    return next;
};
