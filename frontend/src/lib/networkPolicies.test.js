import test from "node:test";
import assert from "node:assert/strict";
import {
    formatPorts,
    parsePorts,
    policiesForDeployment,
    resolvePolicyPeer,
    PEER_KIND_DEPLOYMENT,
    PEER_KIND_SPACE,
    PROTOCOL_TCP,
    PROTOCOL_UDP,
} from "./networkPolicies.js";

const spaces = [{id: 1, name: "global"}, {id: 2, name: "staging"}];
const deployments = [{config: {id: 7, name: "api", spaceId: 2}}];

test("resolvePolicyPeer resolves spaces and deployments", () => {
    const space = resolvePolicyPeer({kind: PEER_KIND_SPACE, id: 2}, spaces, deployments);
    assert.equal(space.label, "space staging");
    assert.equal(space.spaceId, 2);
    assert.equal(space.dangling, false);

    const dep = resolvePolicyPeer({kind: PEER_KIND_DEPLOYMENT, id: 7}, spaces, deployments);
    assert.equal(dep.label, "api");
    assert.equal(dep.spaceId, 2);

    const dangling = resolvePolicyPeer({kind: PEER_KIND_DEPLOYMENT, id: 99}, spaces, deployments);
    assert.equal(dangling.dangling, true);
    assert.equal(dangling.label, "deployment #99");
});

test("parsePorts round trips through formatPorts", () => {
    const {ports, error} = parsePorts("tcp/443, udp/1000-2000");
    assert.equal(error, undefined);
    assert.deepEqual(ports, [
        {protocol: PROTOCOL_TCP, port: 443, portEnd: 0},
        {protocol: PROTOCOL_UDP, port: 1000, portEnd: 2000},
    ]);
    assert.equal(formatPorts(ports), "tcp/443, udp/1000-2000");
    assert.equal(formatPorts([]), "all ports");
});

test("parsePorts rejects malformed entries", () => {
    assert.ok(parsePorts("sctp/9").error);
    assert.ok(parsePorts("tcp/0").error);
    assert.ok(parsePorts("tcp/70000").error);
    assert.ok(parsePorts("udp/2000-1000").error);
    assert.deepEqual(parsePorts("  ").ports, []);
});

test("policiesForDeployment classifies roles", () => {
    const policies = [
        {id: 1, source: {kind: PEER_KIND_SPACE, id: 1}, destination: {kind: PEER_KIND_DEPLOYMENT, id: 7}},
        {id: 2, source: {kind: PEER_KIND_DEPLOYMENT, id: 7}, destination: {kind: PEER_KIND_SPACE, id: 1}},
        {id: 3, source: {kind: PEER_KIND_SPACE, id: 1}, destination: {kind: PEER_KIND_SPACE, id: 2}},
        {id: 4, source: {kind: PEER_KIND_SPACE, id: 5}, destination: {kind: PEER_KIND_SPACE, id: 6}},
    ];
    const matches = policiesForDeployment(policies, 7, 2);
    assert.deepEqual(matches.map((m) => [m.policy.id, m.role]), [
        [1, "inbound"],
        [2, "outbound"],
        [3, "inbound"],
    ]);
});
