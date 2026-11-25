import {nodeDisplayName} from "./machines.js";

const byText = new Intl.Collator(undefined, {numeric: true, sensitivity: "base"});

const bySpace = (a, b) => ((a === 0) - (b === 0)) || (a - b);

const twinKey = (choice) => `${choice.spaceId}\u0000${choice.name}`;

export function deploymentChoices(deployments, spaces, machines) {
    const spaceById = new Map((spaces || []).map((space) => [Number(space.id), space]));
    const choices = (deployments || [])
        .map((item) => {
            const deployment = item?.config?.deployment || {};
            const id = Number(deployment.id || 0);
            const spaceId = Number(deployment.spaceId || 0);
            const nodes = deployment.scheduling?.placement?.value?.dedicatedNodes?.nodes || [];
            return {
                id,
                name: deployment.name || `#${id}`,
                spaceId,
                spaceName: spaceById.get(spaceId)?.name || `space ${spaceId}`,
                nodeNames: nodes.map((nodeId) => nodeDisplayName(nodeId, machines)),
            };
        })
        .filter((choice) => choice.id > 0);

    const twins = new Map();
    for (const choice of choices) twins.set(twinKey(choice), (twins.get(twinKey(choice)) || 0) + 1);
    for (const choice of choices) choice.sharesName = twins.get(twinKey(choice)) > 1;

    return choices.sort((a, b) => byText.compare(a.name, b.name)
        || bySpace(a.spaceId, b.spaceId)
        || byText.compare(a.nodeNames.join(" "), b.nodeNames.join(" "))
        || a.id - b.id);
}
