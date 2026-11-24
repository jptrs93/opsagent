import {allNodesExcluding, allowEffect, alwaysEnabled, decide, isBuiltin, isDeny, readNodes, readSpaces, runPermission} from "/src/lib/nodeAccessPolicies.js";

const builtin = (id, name, kind, spaces) => ({
    id, name, disabled: false, builtin: kind, nodes: allNodesExcluding(),
    rule: {effect: allowEffect(), selector: {permissions: runPermission(), spaces}},
});

export const builtinPolicies = () => [
    builtin(1, "system_everywhere", 1, {exactSpaces: {values: [0]}}),
    builtin(2, "all_spaces_everywhere", 2, {allSpacesExcluding: {values: []}}),
];

const definition = (p) => JSON.stringify([p.name, isDeny(p.rule), readNodes(p.nodes), readSpaces(p.rule?.selector?.spaces)]);

export function writePolicies(policies, {op, id, policy}, placements, refuse) {
    const current = policies.find((p) => Number(p.id) === Number(id));
    if (op !== "create" && !current) throw refuse(404, "Node access policy not found");
    if (op === "delete" && isBuiltin(current)) throw refuse(409, "A built-in node access policy can only be enabled or disabled");
    if (op === "update" && isBuiltin(current)) {
        if (definition(current) !== definition(policy)) throw refuse(409, "A built-in node access policy can only be enabled or disabled");
        if (alwaysEnabled(current) && policy.disabled) throw refuse(409, "This built-in node access policy cannot be disabled");
    }
    if (op !== "delete" && policies.some((p) => p !== current && p.name === policy.name)) {
        throw refuse(409, "A node access policy with this name already exists");
    }
    const next = op === "delete" ? policies.filter((p) => p !== current)
        : op === "create" ? [...policies, policy]
            : policies.map((p) => p === current ? {...policy, builtin: current.builtin} : p);
    const stranded = placements.filter((d) => !decide(next, d.nodeId, d.spaceId).allowed)
        .map((d) => `deployment "${d.deployment}" of space "${d.space}" on node "${d.node}"`);
    if (stranded.length) {
        const shown = stranded.length > 5 ? [...stranded.slice(0, 5), `${stranded.length - 5} more`] : stranded;
        throw refuse(409, `This change would leave deployments on nodes that may no longer run them: ${shown.join("; ")}`);
    }
    return next;
}

export const runSpaces = (policies, nodeId, spaceIds) =>
    spaceIds.filter((spaceId) => Number(spaceId) !== 0 && decide(policies, nodeId, spaceId).allowed);
