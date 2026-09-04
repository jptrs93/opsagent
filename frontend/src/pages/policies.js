// Policies: one page for every cross-space exception, a tab per resource
// kind. Network is the network policies page; Secrets is planned.

import van from "vanjs-core";
import {networkPoliciesPage} from "./networkPolicies.js";
import {PLANNED, plannedBody} from "./planned.js";

const {button, div, span} = van.tags;

export function policiesPage() {
    const tab = van.state("network");

    const tabButton = (key, label, planned) => button(
        {
            type: "button",
            role: "tab",
            "data-testid": `policies-tab-${key}`,
            "aria-selected": () => String(tab.val === key),
            class: () => `-mb-px inline-flex items-center gap-1.5 border-b-2 px-3 py-1.5 text-xs font-medium cursor-pointer transition-colors ${tab.val === key
                ? "border-brand text-gray-100"
                : "border-transparent text-gray-400 hover:text-gray-200"}`,
            onclick: () => tab.val = key,
        },
        label,
        planned ? span({class: "text-[9px] uppercase tracking-wider text-blue-300/70"}, "planned") : "",
    );

    return div(
        {class: "h-full min-h-0 flex flex-col overflow-hidden bg-surface"},
        div(
            {class: "flex flex-none items-end gap-1 border-b border-gray-800 bg-gray-950/40 px-2 pt-1", role: "tablist"},
            tabButton("network", "Network", false),
            tabButton("secrets", "Secrets", true),
        ),
        div(
            {class: "flex-1 min-h-0 flex flex-col overflow-hidden"},
            () => tab.val === "network" ? networkPoliciesPage() : plannedBody(PLANNED.secretPolicies),
        ),
    );
}
