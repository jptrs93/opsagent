import van from "vanjs-core";
import {deploymentsS, networkPoliciesS, spacesS} from "../state/deployments.js";
import {formatPorts, policiesForDeployment, resolvePolicyPeer} from "../lib/networkPolicies.js";

const { div, p, span } = van.tags;

const roleBadge = (role) => span({
    class: `shrink-0 rounded px-1.5 py-0.5 text-[10px] uppercase tracking-wide ${
        role === "inbound" ? "bg-sky-500/15 text-sky-400" : "bg-purple-500/15 text-purple-400"}`,
}, role);

export function deploymentNetworkPolicies(deploymentId, spaceId) {
    return div({class: "flex flex-col gap-1 text-[13px]", "data-testid": "deployment-network-policies"}, () => {
        const matches = policiesForDeployment(networkPoliciesS.val, deploymentId, spaceId);
        if (!matches.length) {
            return p({class: "text-xs text-gray-500"},
                "No override policies apply. Same-space and global-space traffic is always allowed; " +
                "other cross-space traffic is denied. Rules are managed on the Network page.");
        }
        return div({class: "flex flex-col gap-1"},
            ...matches.map(({policy, role}) => {
                const source = resolvePolicyPeer(policy.source, spacesS.val, deploymentsS.val);
                const destination = resolvePolicyPeer(policy.destination, spacesS.val, deploymentsS.val);
                const dangling = source.dangling || destination.dangling;
                return div({class: "flex items-center gap-2 min-w-0"},
                    roleBadge(role),
                    span({class: `truncate ${dangling ? "text-amber-400" : "text-gray-300"}`},
                        `${source.label} → ${destination.label}`),
                    span({class: "shrink-0 text-[11px] text-gray-500"}, formatPorts(policy.ports)),
                );
            }),
            p({class: "text-[11px] text-gray-600"}, "Derived view — rules are managed on the Network page."),
        );
    });
}
