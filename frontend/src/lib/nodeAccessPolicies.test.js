import assert from "node:assert/strict";
import {test} from "node:test";
import {
    BUILTIN_DEFAULT,
    BUILTIN_SYSTEM,
    MODE_EXACT,
    MODE_EXCEPT,
    allNodesExcluding,
    allowEffect,
    alwaysEnabled,
    createPayload,
    decide,
    denyEffect,
    describeNodes,
    describeSpaces,
    draftFromPolicy,
    draftProblems,
    exactNodes,
    isBuiltin,
    newDraft,
    policySentence,
    runPermission,
    togglePayload,
    updatePayload,
    userSpaces,
} from "./nodeAccessPolicies.js";

const exactSpaces = (...values) => ({exactSpaces: {values}});
const allSpacesBut = (...values) => ({allSpacesExcluding: {values}});
const policy = (name, effect, nodes, spaces, extra = {}) =>
    ({id: 10, seq: 7, name, disabled: false, builtin: 0, nodes, rule: {effect, selector: {permissions: runPermission(), spaces}}, ...extra});

const system = policy("system_everywhere", allowEffect(), allNodesExcluding(), exactSpaces(0), {id: 1, builtin: BUILTIN_SYSTEM});
const everything = policy("all_spaces_everywhere", allowEffect(), allNodesExcluding(), allSpacesBut(), {id: 2, builtin: BUILTIN_DEFAULT});

const nodeNames = new Map([[1, "primary"], [2, "edge-1"]]);
const spaceNames = new Map([[0, "_system"], [1, "global"], [2, "prod"]]);

test("the default lets every node run every space", () => {
    assert.deepEqual(decide([system, everything], 1, 2), {allowed: true, policy: ""});
    assert.deepEqual(decide([system, everything], 9, 9), {allowed: true, policy: ""});
});

test("a deny is checked before the allows and is named", () => {
    const deny = policy("edge is prod only", denyEffect(), exactNodes([2]), allSpacesBut(2));
    const policies = [system, everything, deny];
    assert.deepEqual(decide(policies, 2, 1), {allowed: false, policy: "edge is prod only"});
    assert.deepEqual(decide(policies, 2, 2), {allowed: true, policy: ""});
    assert.deepEqual(decide(policies, 1, 1), {allowed: true, policy: ""});
});

test("what no enabled allow covers is refused", () => {
    const off = {...everything, disabled: true};
    const allow = policy("prod on edge", allowEffect(), exactNodes([2]), exactSpaces(2));
    assert.deepEqual(decide([system, off, allow], 2, 2), {allowed: true, policy: ""});
    assert.deepEqual(decide([system, off, allow], 2, 1), {allowed: false, policy: ""});
    assert.deepEqual(decide([system, off, allow], 1, 2), {allowed: false, policy: ""});
    assert.deepEqual(decide([], 1, 1), {allowed: false, policy: ""});
});

test("a disabled deny refuses nothing", () => {
    const deny = policy("no prod", denyEffect(), allNodesExcluding(), exactSpaces(2), {disabled: true});
    assert.equal(decide([everything, deny], 1, 2).allowed, true);
});

test("the system space is allowed whatever the policies say", () => {
    const denyAll = policy("nothing", denyEffect(), allNodesExcluding(), allSpacesBut());
    assert.deepEqual(decide([denyAll], 1, 0), {allowed: true, policy: ""});
    assert.deepEqual(decide([], 1, 0), {allowed: true, policy: ""});
});

test("a rule that does not select the run permission covers nothing", () => {
    const other = policy("other", denyEffect(), allNodesExcluding(), allSpacesBut());
    other.rule.selector.permissions = {allPermissionsExcluding: {values: [1]}};
    assert.equal(decide([everything, other], 1, 1).allowed, true);
});

test("summaries name nodes and spaces and show an unknown id", () => {
    assert.equal(describeNodes(allNodesExcluding(), nodeNames), "all nodes");
    assert.equal(describeNodes(allNodesExcluding([2]), nodeNames), "all nodes except edge-1");
    assert.equal(describeNodes(exactNodes([1, 7]), nodeNames), "primary, node #7");
    assert.equal(describeNodes(undefined, nodeNames), "no nodes");
    assert.equal(describeSpaces(allSpacesBut(2), spaceNames), "all spaces except prod");
    assert.equal(policySentence(everything, nodeNames, spaceNames), "all nodes may run deployments of all spaces");
    assert.equal(
        policySentence(policy("p", denyEffect(), exactNodes([2]), allSpacesBut(2)), nodeNames, spaceNames),
        "edge-1 may not run deployments of all spaces except prod");
});

test("built-ins are recognised, and only the system one is always enabled", () => {
    assert.equal(isBuiltin(system), true);
    assert.equal(isBuiltin(everything), true);
    assert.equal(isBuiltin(policy("p", allowEffect(), allNodesExcluding(), allSpacesBut())), false);
    assert.equal(alwaysEnabled(system), true);
    assert.equal(alwaysEnabled(everything), false);
});

test("userSpaces drops the system space", () => {
    assert.deepEqual(userSpaces([{id: 0}, {id: 1}, {id: "2"}]).map((s) => Number(s.id)), [1, 2]);
    assert.deepEqual(userSpaces(undefined), []);
});

test("a new draft needs a name and a node before it can be saved", () => {
    const draft = newDraft();
    assert.deepEqual(draftProblems(draft), ["Name the policy.", "Pick at least one node."]);
    assert.deepEqual(draftProblems({...draft, name: "p", nodeIds: [2]}), []);
    assert.deepEqual(draftProblems({...draft, name: "p", nodeIds: [2], spacesMode: MODE_EXACT}), ["Pick at least one space."]);
    assert.deepEqual(draftProblems({...draft, name: "p", nodesMode: MODE_EXCEPT}), []);
});

test("a draft round-trips a policy into its update payload", () => {
    const stored = policy("edge is prod only", denyEffect(), exactNodes([2]), allSpacesBut(2));
    const draft = draftFromPolicy(stored);
    assert.equal(draft.nodesMode, MODE_EXACT);
    assert.equal(draft.spacesMode, MODE_EXCEPT);
    assert.deepEqual(updatePayload(stored, draft), {
        id: 10, expectedSeq: 7, name: "edge is prod only", disabled: false, nodes: stored.nodes, rule: stored.rule,
    });
});

test("createPayload trims the name and carries the run permission", () => {
    const payload = createPayload({...newDraft(), name: "  p  ", enabled: false, deny: false, nodesMode: MODE_EXCEPT, nodeIds: [1], spacesMode: MODE_EXACT, spaceIds: [2]});
    assert.deepEqual(payload, {
        name: "p", disabled: true, nodes: allNodesExcluding([1]),
        rule: {effect: allowEffect(), selector: {permissions: runPermission(), spaces: exactSpaces(2)}},
    });
});

test("togglePayload flips only the switch", () => {
    assert.deepEqual(togglePayload(everything), {
        id: 2, expectedSeq: 7, name: "all_spaces_everywhere", disabled: true, nodes: everything.nodes, rule: everything.rule,
    });
});
