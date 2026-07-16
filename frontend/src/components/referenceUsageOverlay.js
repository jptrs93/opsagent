import van from "vanjs-core";

const { button, div, h2, p, span, table, tbody, td, th, thead, tr } = van.tags;

export function referenceUsageOverlay(resourceType, resourceName, deployments, settings, onClose) {
    const deploymentTable = deployments.length ? div(
        {class: "overflow-x-auto rounded-lg border border-gray-700"},
        table(
            {class: "w-full min-w-[34rem] text-sm"},
            thead(
                tr(
                    {class: "border-b border-gray-700 bg-gray-950 text-left text-xs text-gray-400"},
                    th({class: "px-3 py-2 font-medium"}, "Space"),
                    th({class: "px-3 py-2 font-medium"}, "Deployment"),
                    th({class: "px-3 py-2 font-medium"}, "Node"),
                ),
            ),
            tbody(...deployments.map(usage => tr(
                {
                    class: "border-b border-gray-800 last:border-0",
                    "data-testid": `reference-usage-deployment-${usage.id}`,
                },
                td({class: "px-3 py-2 text-gray-300"}, usage.space),
                td({class: "px-3 py-2 font-medium text-gray-100"}, usage.name),
                td({class: "px-3 py-2 text-gray-300"}, usage.node),
            ))),
        ),
    ) : p({class: "text-sm text-gray-500"}, "No deployments use this item.");

    return div(
        div({class: "fixed inset-0 z-40 bg-black/70", onclick: onClose}),
        div(
            {
                class: "fixed inset-0 z-50 flex items-center justify-center p-3 md:p-6 pointer-events-none",
                "data-testid": "reference-usage-overlay",
            },
            div(
                {
                    class: "w-full max-w-3xl max-h-[85vh] overflow-hidden rounded-xl border border-gray-700 bg-gray-900 shadow-2xl pointer-events-auto flex flex-col",
                    role: "dialog",
                    "aria-modal": "true",
                    "aria-labelledby": "reference-usage-title",
                },
                div(
                    {class: "flex items-start justify-between gap-4 border-b border-gray-700 px-4 py-3"},
                    div(
                        {class: "min-w-0"},
                        h2(
                            {id: "reference-usage-title", class: "truncate text-base font-semibold text-gray-100"},
                            span({class: "capitalize text-purple-300"}, `${resourceType} ${resourceName}`),
                            " in use by",
                        ),
                    ),
                    button({type: "button", class: "shrink-0 cursor-pointer px-3 py-1.5 text-sm text-gray-400 hover:text-gray-100", onclick: onClose}, "Close"),
                ),
                div(
                    {class: "min-h-0 overflow-y-auto p-4 space-y-5"},
                    div(
                        h2({class: "mb-2 text-sm font-semibold text-gray-200"}, `Deployments (${deployments.length})`),
                        deploymentTable,
                    ),
                    settings.length ? div(
                        h2({class: "mb-2 text-sm font-semibold text-gray-200"}, `System settings (${settings.length})`),
                        div(
                            {class: "overflow-hidden rounded-lg border border-gray-700"},
                            ...settings.map(setting => div(
                                {class: "border-b border-gray-800 px-3 py-2 text-sm text-gray-300 last:border-0"},
                                setting.label,
                            )),
                        ),
                    ) : '',
                ),
            ),
        ),
    );
}
