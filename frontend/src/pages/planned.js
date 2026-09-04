// Pages for features that do not exist yet: a single centred message. The
// sidebar tags these items "soon".

import van from "vanjs-core";

const {div, p} = van.tags;

export const PLANNED_MESSAGE = "This is a planned future feature not yet implemented";

// Page keys the dashboard routes to a planned page.
export const PLANNED = new Set(["jobs", "charts", "dashboards", "alerts", "approvals"]);

// The message alone, filling its host; also used for a planned tab inside an
// existing page.
export function plannedBody() {
    return div(
        {class: "flex flex-1 min-h-0 items-center justify-center p-6", "data-testid": "planned-page"},
        p({class: "text-center text-sm text-gray-400"}, PLANNED_MESSAGE),
    );
}

export function plannedPage() {
    return div({class: "flex h-full min-h-0 flex-col bg-surface"}, plannedBody());
}
