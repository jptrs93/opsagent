import van from "vanjs-core";

const { div, h1, p } = van.tags;

export function settingsPage() {
    return div(
        {class: "flex-1 min-h-0 overflow-auto p-6 flex flex-col gap-6"},
        h1({class: "text-xl font-bold"}, "Settings"),
        p({class: "text-gray-400"}, "Nothing to configure yet."),
    );
}
