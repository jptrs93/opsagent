// The sidebar tree: four groups ordered by how often an operator visits them,
// so workload and observability pages come first and admin pages last. The
// sidebar renders from this model; the dashboard routes each item key to a
// page. `future` marks pages that do not exist yet, which the sidebar tags
// "soon" and the dashboard renders as a planned-page explanation. `badge`
// names a pending count the sidebar shows beside the item, and beside a
// collapsed group as the sum of its items, so an inbox is never hidden.

export const NAV_GROUPS = [
    {
        key: "workloads",
        label: "Workloads",
        hint: "What the cluster should run: versioned state that changes together when you ship.",
        items: [
            {key: "status", label: "Deployments"},
            {key: "secrets", label: "Secrets / Configs"},
            {key: "assets", label: "Assets"},
            {key: "jobs", label: "Jobs", future: true},
            {key: "policies", label: "Policies"},
            {key: "charts", label: "Charts", future: true},
        ],
    },
    {
        key: "observability",
        label: "Observability",
        hint: "What the cluster is doing.",
        items: [
            {key: "logs", label: "Logs"},
            {key: "metrics", label: "Metrics"},
            {key: "dashboards", label: "Dashboards", future: true},
            {key: "alerts", label: "Alerts", future: true},
        ],
    },
    {
        key: "access",
        label: "IAM",
        hint: "Who, and which agents, may act on the cluster.",
        items: [
            {key: "users", label: "Users & roles"},
            {key: "sessions", label: "Sessions"},
            {key: "approvals", label: "Approvals", future: true, badge: "approvals"},
        ],
    },
    {
        key: "cluster",
        label: "Cluster",
        hint: "Admin-configured infrastructure that changes rarely.",
        items: [
            {key: "spaces", label: "Spaces"},
            {key: "cluster", label: "Nodes"},
            {key: "settings", label: "Settings"},
        ],
    },
];

export const groupOfPage = (pageKey) => NAV_GROUPS.find(group => group.items.some(item => item.key === pageKey));

export const itemOfPage = (pageKey) => {
    for (const group of NAV_GROUPS) {
        const item = group.items.find(candidate => candidate.key === pageKey);
        if (item) return item;
    }
    return null;
};
