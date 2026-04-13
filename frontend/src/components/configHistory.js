import van from "vanjs-core";
import {format} from "date-fns";
import {resolveUserDisplayName} from "../lib/users.js";
const { div, h2, span } = van.tags;

export function configHistory(versionsState, onRestore) {
    return div(
        {class: "w-72 border-l border-gray-700 bg-sidebar flex flex-col overflow-auto"},
        div(
            {class: "p-4 border-b border-gray-800"},
            h2({class: "text-sm font-semibold text-gray-300"}, "Version History")
        ),
        div(
            {class: "flex-1 overflow-auto p-2"},
            () => {
                const versions = versionsState.val;
                if (!versions || versions.length === 0) {
                    return div({class: "p-4 text-sm text-gray-500"}, "No versions yet.");
                }
                return div(
                    {class: "flex flex-col gap-1"},
                    ...versions.map((v, i) => {
                        const author = resolveUserDisplayName(v.updatedBy);
                        return div({
                            class: "px-3 py-2 rounded text-sm cursor-pointer hover:bg-surface-hover transition-colors",
                            onclick: () => onRestore(v)
                        },
                            div(
                                {class: `${i === 0 ? "text-white" : "text-gray-500"} text-xs font-mono whitespace-nowrap overflow-hidden text-ellipsis`},
                                v.timestamp ? format(new Date(v.timestamp), "MMM d, yyyy HH:mm") : "",
                                author ? span(` [${author}]`) : "",
                                span(` v${v.version}`)
                            )
                        );
                    })
                );
            }
        )
    );
}
