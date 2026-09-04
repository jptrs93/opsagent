// Pages for features that do not exist yet. Each explains what the feature
// will be, in the layout its future empty state can keep: a summary and what
// it will do. The sidebar tags these items "soon".

import van from "vanjs-core";

const {div, h1, p, span, ul, li} = van.tags;

export const PLANNED = {
    jobs: {
        title: "Jobs",
        summary: "A job is a workload that runs to completion instead of staying up: a migration, a backup, a report, " +
            "a batch import. It is defined like a deployment, with the same source, env refs and mounts, but its " +
            "lifecycle is a run with an exit code rather than a service kept alive.",
        will: [
            "Define a job with the deployment editor's source and container settings, plus a schedule or a manual trigger.",
            "Run it on demand or on a cron schedule, with a concurrency policy for overlapping runs.",
            "List runs with start time, duration, exit code and the node they ran on, with logs one click away.",
            "Retry a failed run, or cancel a running one.",
        ],
    },
    charts: {
        title: "Charts",
        summary: "A chart is a packaged, parameterised set of deployments and the secrets, configs and assets they need, " +
            "the way a Helm chart packages Kubernetes resources. Install a chart into a space, set its values, and it " +
            "expands into ordinary deployments you can see and edit on the Deployments page.",
        will: [
            "Browse charts from a repository or upload one, and see the deployments it would create before installing.",
            "Install into a space with a values form or an HCL values block.",
            "Upgrade an installed chart to a new version with a diff of the resulting deployment changes.",
            "Uninstall and remove every deployment the install created.",
        ],
    },
    dashboards: {
        title: "Dashboards",
        summary: "Dashboards are saved arrangements of metric panels you build yourself: pick deployments, a metric, a rollup " +
            "and a time range per panel, lay the panels out, and share the result with a space.",
        will: [
            "Create a dashboard from a blank grid or by pinning a chart from the Metrics explorer.",
            "Arrange panels on a grid with per-panel deployment, metric and rollup settings.",
            "Share a dashboard with a space, or keep it personal.",
            "Set a default dashboard that opens when you land on Observability.",
        ],
    },
    alerts: {
        title: "Alerts",
        summary: "An alert is a rule over metrics or logs that fires when a condition holds for long enough: restart count " +
            "climbing, a health check failing, memory near its limit, or a log query matching too often.",
        will: [
            "Define a rule on a metric threshold or a log query match rate, with a for-duration and a severity.",
            "See firing and resolved alerts in one list, with a link to the deployment and the metric or logs that tripped it.",
            "Route notifications to a webhook, email or a chat channel per space.",
            "Silence an alert for a while during a planned change.",
        ],
    },
    approvals: {
        title: "Approvals",
        summary: "Approvals are a queue of actions an agent has asked to perform but may not run on its own. A new grant " +
            "level, between deny and allow, lets an agent propose an action; a human reviews it here and approves " +
            "or rejects it. The pending count shows in the sidebar wherever you are.",
        will: [
            "List pending requests with the agent session, the requested action, the target space and entity, and a diff where one applies.",
            "Approve or reject with an optional note; the agent is told the outcome and continues.",
            "Absorb the existing approve step for agent session join requests, so Sessions is only lifecycle.",
            "Keep a history of decided requests for audit.",
        ],
    },
    secretPolicies: {
        title: "Secret policies",
        summary: "A secret policy lets workloads in one space read a secret that lives in another. It has the same shape as a " +
            "network policy: a source space, a destination space and the resource the exception covers, written by " +
            "someone with update access on the destination.",
        will: [
            "Grant space X read access to a named secret, or every secret, in space Y.",
            "Show a dangling marker when the source or destination space, or the secret, no longer exists.",
            "Fail a deployment's reference check with the missing policy named, so the fix is one click away.",
            "Extend to configs and assets later as further tabs on this page.",
        ],
    },
};

export const plannedPill = () => span(
    {class: "rounded bg-blue-500/15 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wider text-blue-300"},
    "Planned",
);

const subhead = (text) => p({class: "mt-5 mb-1.5 text-[11px] font-semibold uppercase tracking-wider text-gray-500"}, text);

// The explanation alone, for a planned tab inside an existing page.
export function plannedBody(spec) {
    return div(
        {class: "app-scroll flex-1 min-h-0 overflow-auto px-6 py-5", "data-testid": "planned-page"},
        p({class: "max-w-2xl text-sm leading-relaxed text-gray-300"}, spec.summary),
        subhead("What it will do"),
        ul(
            {class: "max-w-2xl list-disc space-y-1 pl-5 text-sm leading-relaxed text-gray-300"},
            ...spec.will.map(text => li(text)),
        ),
    );
}

export function plannedPage(key) {
    const spec = PLANNED[key];
    return div(
        {class: "flex h-full min-h-0 flex-col bg-surface"},
        div(
            {class: "flex flex-none items-center gap-2 border-b border-gray-700 px-3 py-2"},
            h1({class: "text-sm font-semibold text-gray-100"}, spec.title),
            plannedPill(),
        ),
        plannedBody(spec),
    );
}
