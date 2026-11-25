import test from "node:test";
import assert from "node:assert/strict";
import {deploymentChoices} from "./deploymentChoices.js";

const deployment = (id, name, spaceId, nodes = []) => ({
    config: {deployment: {id, name, spaceId, scheduling: {placement: {value: {dedicatedNodes: {nodes}}}}}},
});

const spaces = [{id: 0, name: "_system"}, {id: 1, name: "prod"}, {id: 2, name: "staging"}];
const machines = [{id: 5, name: "worker-a"}, {id: 6, name: "worker-b", evicted: true}];

test("choices are ordered by name, then by space with the system space last", () => {
    const choices = deploymentChoices([
        deployment(4, "web", 1),
        deployment(3, "api", 0),
        deployment(2, "api", 2),
        deployment(1, "API", 1),
        deployment(5, "worker-10", 1),
        deployment(6, "worker-2", 1),
    ], spaces, machines);

    assert.deepEqual(choices.map((c) => [c.name, c.spaceName]), [
        ["API", "prod"],
        ["api", "staging"],
        ["api", "_system"],
        ["web", "prod"],
        ["worker-2", "prod"],
        ["worker-10", "prod"],
    ]);
});

test("a choice carries its space and the names of its nodes", () => {
    const [choice] = deploymentChoices([deployment(9, "api", 1, [5, 6, 7])], spaces, machines);

    assert.deepEqual(choice, {
        id: 9,
        name: "api",
        spaceId: 1,
        spaceName: "prod",
        nodeNames: ["worker-a", "worker-b (evicted)", "node 7"],
        sharesName: false,
    });
});

test("deployments with one name in one space are marked and ordered by node", () => {
    const choices = deploymentChoices([
        deployment(21, "opendeploy", 0, [6]),
        deployment(20, "opendeploy", 0, [5]),
        deployment(22, "opendeploy", 1, [5]),
        deployment(23, "opendeploy-net", 0, [5]),
    ], spaces, [{id: 5, name: "primary"}, {id: 6, name: "worker-a"}]);

    assert.deepEqual(choices.map((c) => [c.id, c.sharesName]), [
        [22, false],
        [20, true],
        [21, true],
        [23, false],
    ]);
});

test("an unnamed deployment and an unknown space get a stand-in name", () => {
    const [choice] = deploymentChoices([deployment(12, "", 8)], spaces, machines);

    assert.equal(choice.name, "#12");
    assert.equal(choice.spaceName, "space 8");
    assert.deepEqual(choice.nodeNames, []);
});

test("entries without a deployment id are left out", () => {
    assert.deepEqual(deploymentChoices([{config: {}}, {}, null, deployment(0, "ghost", 1)], spaces, machines), []);
    assert.deepEqual(deploymentChoices(null, null, null), []);
});
