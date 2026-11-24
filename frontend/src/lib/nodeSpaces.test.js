import assert from "node:assert/strict";
import {test} from "node:test";
import {
    allowedSpaceNames,
    nodeAllowsSpace,
    defaultPlacement,
    placeholderDeploymentName,
    nodesForSpace,
    runsEverySpace,
    selectableSpaces,
} from "./nodeSpaces.js";

const node = (name, allowedSpaces) => ({id: name.length, name, identifier: `${name}-id`, allowedSpaces});

test("nodeAllowsSpace compares numerically", () => {
    const n = node("a", [1, 7]);
    assert.equal(nodeAllowsSpace(n, 7), true);
    assert.equal(nodeAllowsSpace(n, "7"), true);
    assert.equal(nodeAllowsSpace(n, 2), false);
});

test("a node with no record allows nothing rather than throwing", () => {
    assert.equal(nodeAllowsSpace(undefined, 0), false);
    assert.equal(nodeAllowsSpace(undefined, 1), false);
    assert.equal(nodeAllowsSpace({}, 1), false);
});

test("every node runs the system space, which its list leaves out", () => {
    assert.equal(nodeAllowsSpace(node("a", []), 0), true);
    assert.equal(nodeAllowsSpace({}, 0), true);
});

test("runsEverySpace compares against the spaces a person can pick", () => {
    const spaces = [{id: 0, name: "_system"}, {id: 1, name: "global"}, {id: 2, name: "prod"}];
    assert.equal(runsEverySpace(node("a", [1, 2]), spaces), true);
    assert.equal(runsEverySpace(node("a", [1]), spaces), false);
    assert.equal(runsEverySpace(node("a", []), [{id: 0, name: "_system"}]), false);
});

test("nodesForSpace keeps the given order", () => {
    const nodes = [node("a", [1]), node("bb", []), node("ccc", [1])];
    assert.deepEqual(nodesForSpace(nodes, 1).map(n => n.name), ["a", "ccc"]);
    assert.deepEqual(nodesForSpace(nodes, 0).map(n => n.name), ["a", "bb", "ccc"]);
    assert.deepEqual(nodesForSpace(nodes, 9), []);
});

test("selectableSpaces drops the system space and tolerates no list", () => {
    const spaces = [{id: 0, name: "_system"}, {id: 1, name: "global"}, {id: "2", name: "staging"}];
    assert.deepEqual(selectableSpaces(spaces).map(s => s.name), ["global", "staging"]);
    assert.deepEqual(selectableSpaces([]), []);
    assert.deepEqual(selectableSpaces(undefined), []);
});

test("allowedSpaceNames shows an unknown id rather than dropping it", () => {
    const spaces = [{id: 0, name: "_system"}, {id: 1, name: "global"}];
    assert.deepEqual(
        allowedSpaceNames(node("a", [1, 7]), spaces),
        ["global", "space 7"],
    );
});

test("defaultPlacement prefers the global space when a node hosts it", () => {
    const spaces = [{id: 0, name: "_system"}, {id: 1, name: "global"}, {id: 2, name: "prod"}];
    const nodes = [{id: 11, allowedSpaces: [2]}, {id: 12, allowedSpaces: [1, 2]}];
    assert.deepEqual(defaultPlacement(spaces, nodes), {spaceId: 1, nodeId: 12});
});

test("defaultPlacement falls back to the first visible space some node hosts", () => {
    const spaces = [{id: 0, name: "_system"}, {id: 1, name: "global"}, {id: 2, name: "prod"}, {id: 3, name: "staging"}];
    const nodes = [{id: 11, allowedSpaces: [3]}, {id: 12, allowedSpaces: [2]}];
    assert.deepEqual(defaultPlacement(spaces, nodes), {spaceId: 2, nodeId: 12});
});

test("defaultPlacement is null when no node hosts a visible space", () => {
    const spaces = [{id: 0, name: "_system"}, {id: 1, name: "global"}];
    assert.equal(defaultPlacement(spaces, [{id: 11, allowedSpaces: []}]), null);
    assert.equal(defaultPlacement(spaces, []), null);
});

test("placeholderDeploymentName is deployment- plus six lowercase alphanumerics", () => {
    assert.match(placeholderDeploymentName(), /^deployment-[a-z0-9]{6}$/);
    assert.equal(placeholderDeploymentName(() => 0), "deployment-aaaaaa");
});
