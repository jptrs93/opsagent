import {allExcludingSelector, exactSelector, readSelector} from "./authz.js";

const SYSTEM_SPACE_ID = 0;

export const RUN_DEPLOYMENTS = 1;

export const BUILTIN_SYSTEM = 1;
export const BUILTIN_DEFAULT = 2;

export const allowEffect = () => ({value: {allow: {}}});
export const denyEffect = () => ({value: {deny: {}}});
export const isDeny = (rule) => Boolean(rule?.effect?.value?.deny);

export const exactNodes = (values) => ({exactNodes: {values: [...values]}});
export const allNodesExcluding = (values = []) => ({allNodesExcluding: {values: [...values]}});
export const runPermission = () => ({exactPermissions: {values: [RUN_DEPLOYMENTS]}});

const readPosition = (exact, excluding) => {
    if (excluding) return {mode: excluding.values?.length ? "allExcept" : "all", values: [...(excluding.values || [])]};
    const values = exact?.values || [];
    return {mode: values.length ? "list" : "none", values: [...values]};
};

export const readNodes = (sel) => readPosition(sel?.exactNodes, sel?.allNodesExcluding);
export const readPermissions = (sel) => readPosition(sel?.exactPermissions, sel?.allPermissionsExcluding);
export const readSpaces = (sel) => readSelector(sel, "spaces");

export const userSpaces = (spaces) => (spaces || []).filter((s) => Number(s.id) !== SYSTEM_SPACE_ID);

export const isBuiltin = (policy) => Number(policy?.builtin || 0) !== 0;
export const alwaysEnabled = (policy) => Number(policy?.builtin || 0) === BUILTIN_SYSTEM;

const describe = (view, noun, name) => {
    const names = view.values.map(name).join(", ");
    if (view.mode === "all") return `all ${noun}`;
    if (view.mode === "allExcept") return `all ${noun} except ${names}`;
    return view.mode === "list" ? names : `no ${noun}`;
};

export const describeNodes = (sel, nodeNames) =>
    describe(readNodes(sel), "nodes", (id) => nodeNames?.get?.(Number(id)) ?? `node #${id}`);

export const describeSpaces = (sel, spaceNames) =>
    describe(readSpaces(sel), "spaces", (id) => spaceNames?.get?.(Number(id)) ?? `space #${id}`);

export const policySentence = (policy, nodeNames, spaceNames) =>
    `${describeNodes(policy?.nodes, nodeNames)} ${isDeny(policy?.rule) ? "may not run" : "may run"} deployments of ${describeSpaces(policy?.rule?.selector?.spaces, spaceNames)}`;

const selects = (view, listed) => view.mode === "list" ? listed : view.mode !== "none" && !listed;
const selectsID = (view, id) => selects(view, view.values.map(Number).includes(Number(id)));

export const policyCovers = (policy, nodeId, spaceId) =>
    selectsID(readNodes(policy?.nodes), nodeId)
    && selectsID(readPermissions(policy?.rule?.selector?.permissions), RUN_DEPLOYMENTS)
    && selectsID(readSpaces(policy?.rule?.selector?.spaces), spaceId);

export function decide(policies, nodeId, spaceId) {
    if (Number(spaceId) === SYSTEM_SPACE_ID) return {allowed: true, policy: ""};
    let allowed = false;
    for (const policy of policies || []) {
        if (policy.disabled || !policyCovers(policy, nodeId, spaceId)) continue;
        if (isDeny(policy.rule)) return {allowed: false, policy: policy.name};
        allowed = true;
    }
    return {allowed, policy: ""};
}

export const MODE_EXACT = "exact";
export const MODE_EXCEPT = "except";

export const newDraft = () => ({
    name: "", deny: true, enabled: true,
    nodesMode: MODE_EXACT, nodeIds: [],
    spacesMode: MODE_EXCEPT, spaceIds: [],
    permissions: runPermission(),
});

const draftMode = (view) => view.mode === "all" || view.mode === "allExcept" ? MODE_EXCEPT : MODE_EXACT;

export function draftFromPolicy(policy) {
    const nodes = readNodes(policy.nodes);
    const spaces = readSpaces(policy.rule?.selector?.spaces);
    return {
        name: policy.name, deny: isDeny(policy.rule), enabled: !policy.disabled,
        nodesMode: draftMode(nodes), nodeIds: nodes.values.map(Number),
        spacesMode: draftMode(spaces), spaceIds: spaces.values.map(Number),
        permissions: policy.rule?.selector?.permissions || runPermission(),
    };
}

export function draftProblems(draft) {
    const problems = [];
    if (!draft.name.trim()) problems.push("Name the policy.");
    if (draft.nodesMode === MODE_EXACT && !draft.nodeIds.length) problems.push("Pick at least one node.");
    if (draft.spacesMode === MODE_EXACT && !draft.spaceIds.length) problems.push("Pick at least one space.");
    return problems;
}

export const draftNodes = (draft) =>
    draft.nodesMode === MODE_EXACT ? exactNodes(draft.nodeIds) : allNodesExcluding(draft.nodeIds);

export const draftRule = (draft) => ({
    effect: draft.deny ? denyEffect() : allowEffect(),
    selector: {
        permissions: draft.permissions,
        spaces: draft.spacesMode === MODE_EXACT ? exactSelector("spaces", draft.spaceIds) : allExcludingSelector("spaces", draft.spaceIds),
    },
});

export const createPayload = (draft) =>
    ({name: draft.name.trim(), disabled: !draft.enabled, nodes: draftNodes(draft), rule: draftRule(draft)});

export const updatePayload = (policy, draft) => ({id: policy.id, expectedSeq: policy.seq, ...createPayload(draft)});

export const togglePayload = (policy) =>
    ({id: policy.id, expectedSeq: policy.seq, name: policy.name, disabled: !policy.disabled, nodes: policy.nodes, rule: policy.rule});
