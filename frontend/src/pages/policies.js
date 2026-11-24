// Policies: one page for every cross-space exception, a tab per resource
// kind. Network is the network policies page; Secrets is planned.

import van from "vanjs-core";
import {networkPoliciesPage} from "./networkPolicies.js";
import {plannedBody} from "./planned.js";
import {tabButton, tabStrip} from "../components/tabs.js";

const {div, span} = van.tags;

export function policiesPage() {
    const tab = van.state("network");

    return div(
        {class: "page"},
        tabStrip({label: "Policies"},
            tabButton({id: "network", activeTab: tab, title: "Network", testid: "policies-tab-network"}),
            tabButton({
                id: "secrets",
                activeTab: tab,
                title: "Secrets",
                testid: "policies-tab-secrets",
                trailing: span({class: "cluster-planned-tag"}, "planned"),
            }),
        ),
        div(
            {class: "page-body flex flex-col overflow-hidden"},
            () => tab.val === "network" ? networkPoliciesPage() : plannedBody(),
        ),
    );
}
