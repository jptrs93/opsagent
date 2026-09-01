import {deploymentDeleted} from "./deployment.js";

export const PEER_KIND_SPACE = 1;
export const PEER_KIND_DEPLOYMENT = 2;
export const POLICY_ACTION_ALLOW = 1;
export const PROTOCOL_TCP = 1;
export const PROTOCOL_UDP = 2;

export function resolvePolicyPeer(peer, spaces, deployments) {
    const kind = Number(peer?.kind || 0);
    const id = Number(peer?.id || 0);
    if (kind === PEER_KIND_SPACE) {
        const space = (spaces || []).find((s) => s && !s.deleted && Number(s.id) === id);
        if (!space) return {kind, id, label: `space #${id}`, spaceId: null, dangling: true};
        return {kind, id, label: `space ${space.name || id}`, spaceId: id, dangling: false};
    }
    if (kind === PEER_KIND_DEPLOYMENT) {
        const row = (deployments || []).find((d) => d?.config && !deploymentDeleted(d.config) && Number(d.config.id) === id);
        if (!row) return {kind, id, label: `deployment #${id}`, spaceId: null, dangling: true};
        return {kind, id, label: row.config.def?.name || `deployment #${id}`, spaceId: Number(row.config.def?.spaceId || 0), dangling: false};
    }
    return {kind, id, label: "unknown peer", spaceId: null, dangling: true};
}

export function formatPorts(ports) {
    if (!ports || !ports.length) return "all ports";
    return ports.map((p) => {
        const proto = Number(p.protocol) === PROTOCOL_UDP ? "udp" : "tcp";
        const end = Number(p.portEnd || 0);
        return end ? `${proto}/${p.port}-${end}` : `${proto}/${p.port}`;
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
        const port = Number(match[2]);
        const portEnd = match[3] ? Number(match[3]) : 0;
        if (port < 1 || port > 65535 || (portEnd !== 0 && (portEnd < port || portEnd > 65535))) {
            return {error: `Invalid port range "${entry}"`};
        }
        ports.push({protocol: match[1] === "udp" ? PROTOCOL_UDP : PROTOCOL_TCP, port, portEnd});
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
    const kind = Number(peer?.kind || 0);
    const id = Number(peer?.id || 0);
    if (kind === PEER_KIND_DEPLOYMENT) return id === Number(deploymentId);
    if (kind === PEER_KIND_SPACE) return id === Number(spaceId);
    return false;
}
