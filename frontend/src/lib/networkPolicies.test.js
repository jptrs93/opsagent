import test from "node:test";
import assert from "node:assert/strict";
import {
    formatPorts,
    makePeer,
    parsePorts,
    peerRef,
    policiesForDeployment,
    resolvePolicyPeer,
    PEER_KIND_DEPLOYMENT,
    PEER_KIND_SPACE,
    PROTOCOL_TCP,
    PROTOCOL_UDP,
} from "./networkPolicies.js";

const spaces = [{id: 1, name: "global"}, {id: 2, name: "staging"}];
const deployments = [{config: {deployment: {id: 7, name: "api", spaceId: 2}, meta: {version: 1}}}];
const spacePeer = spaceId => ({target: {value: {space: {spaceId}}}});
const deploymentPeer = deploymentId => ({target: {value: {deployment: {deploymentId}}}});

test("peerRef and makePeer round trip the peer oneof", () => {
    assert.deepEqual(peerRef(spacePeer(2)), {kind: PEER_KIND_SPACE, id: 2});
    assert.deepEqual(peerRef(deploymentPeer(7)), {kind: PEER_KIND_DEPLOYMENT, id: 7});
    assert.deepEqual(peerRef({}), {kind: 0, id: 0});
    assert.deepEqual(makePeer(PEER_KIND_SPACE, "2"), spacePeer(2));
    assert.deepEqual(makePeer(PEER_KIND_DEPLOYMENT, 7), deploymentPeer(7));
});

test("resolvePolicyPeer resolves spaces and deployments", () => {
    const space = resolvePolicyPeer(spacePeer(2), spaces, deployments);
    assert.equal(space.label, "space staging");
    assert.equal(space.spaceId, 2);
    assert.equal(space.dangling, false);

    const dep = resolvePolicyPeer(deploymentPeer(7), spaces, deployments);
    assert.equal(dep.label, "api");
    assert.equal(dep.spaceId, 2);

    const dangling = resolvePolicyPeer(deploymentPeer(99), spaces, deployments);
    assert.equal(dangling.dangling, true);
    assert.equal(dangling.label, "deployment #99");
});

test("parsePorts round trips through formatPorts", () => {
    const {ports, error} = parsePorts("tcp/443, udp/1000-2000");
    assert.equal(error, undefined);
    assert.deepEqual(ports, [
        {protocol: PROTOCOL_TCP, range: {start: 443, end: 443}},
        {protocol: PROTOCOL_UDP, range: {start: 1000, end: 2000}},
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
        {id: 1, source: spacePeer(1), destination: deploymentPeer(7)},
        {id: 2, source: deploymentPeer(7), destination: spacePeer(1)},
        {id: 3, source: spacePeer(1), destination: spacePeer(2)},
        {id: 4, source: spacePeer(5), destination: spacePeer(6)},
    ];
    const matches = policiesForDeployment(policies, 7, 2);
    assert.deepEqual(matches.map((m) => [m.policy.id, m.role]), [
        [1, "inbound"],
        [2, "outbound"],
        [3, "inbound"],
    ]);
});
