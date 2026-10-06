import {deploymentDeleted} from "./deployment.js";

export const PEER_KIND_SPACE = 1;
export const PEER_KIND_DEPLOYMENT = 2;
export const POLICY_ACTION_ALLOW = 1;
export const PROTOCOL_TCP = 1;
export const PROTOCOL_UDP = 2;

export function peerRef(peer) {
    const target = peer?.target?.value || {};
    if (target.space) return {kind: PEER_KIND_SPACE, id: Number(target.space.spaceId || 0)};
    if (target.deployment) return {kind: PEER_KIND_DEPLOYMENT, id: Number(target.deployment.deploymentId || 0)};
    return {kind: 0, id: 0};
}

export function makePeer(kind, id) {
    return Number(kind) === PEER_KIND_DEPLOYMENT
        ? {target: {value: {deployment: {deploymentId: Number(id)}}}}
        : {target: {value: {space: {spaceId: Number(id)}}}};
}

export function resolvePolicyPeer(peer, spaces, deployments) {
    const {kind, id} = peerRef(peer);
    if (kind === PEER_KIND_SPACE) {
        const space = (spaces || []).find((s) => s && !s.deleted && Number(s.id) === id);
        if (!space) return {kind, id, label: `space #${id}`, spaceId: null, dangling: true};
        return {kind, id, label: `space ${space.name || id}`, spaceId: id, dangling: false};
    }
    if (kind === PEER_KIND_DEPLOYMENT) {
        const row = (deployments || []).find((d) => d?.config && !deploymentDeleted(d.config) && Number(d.config.deployment?.id) === id);
        if (!row) return {kind, id, label: `deployment #${id}`, spaceId: null, dangling: true};
        return {kind, id, label: row.config.deployment.name || `deployment #${id}`, spaceId: Number(row.config.deployment.spaceId || 0), dangling: false};
    }
    return {kind, id, label: "unknown peer", spaceId: null, dangling: true};
}

export function formatPorts(ports) {
    if (!ports || !ports.length) return "all ports";
    return ports.map((p) => {
        const proto = Number(p.protocol) === PROTOCOL_UDP ? "udp" : "tcp";
        const start = Number(p.range?.start || 0);
        const end = Number(p.range?.end || 0);
        return end && end !== start ? `${proto}/${start}-${end}` : `${proto}/${start}`;
    }).join(", ");
}

export function parsePorts(text) {
    const trimmed = (text || "").trim();
    if (!trimmed) return {ports: []};
    const ports = [];
    for (const part of trimmed.split(",")) {
        const entry = part.trim();
        if (!entry) continue;
        const match = entry.toLowerCase().match(/^(tcp|udp)\/(\d+)(?:-(\d+))?$/);
        if (!match) return {error: `Invalid port entry "${entry}" — use tcp/443 or udp/1000-2000`};
        const start = Number(match[2]);
        const end = match[3] ? Number(match[3]) : start;
        if (start < 1 || start > 65535 || end < start || end > 65535) {
            return {error: `Invalid port range "${entry}"`};
        }
        ports.push({protocol: match[1] === "udp" ? PROTOCOL_UDP : PROTOCOL_TCP, range: {start, end}});
    }
    return {ports};
}

export function policiesForDeployment(policies, deploymentId, spaceId) {
    const matches = [];
    for (const policy of policies || []) {
        if (!policy || policy.deleted) continue;
        const roles = [];
        if (peerMatchesDeployment(policy.destination, deploymentId, spaceId)) roles.push("inbound");
        if (peerMatchesDeployment(policy.source, deploymentId, spaceId)) roles.push("outbound");
        for (const role of roles) matches.push({policy, role});
    }
    return matches;
}

function peerMatchesDeployment(peer, deploymentId, spaceId) {
    const {kind, id} = peerRef(peer);
    if (kind === PEER_KIND_DEPLOYMENT) return id === Number(deploymentId);
    if (kind === PEER_KIND_SPACE) return id === Number(spaceId);
    return false;
}
