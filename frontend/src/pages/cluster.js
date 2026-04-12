import van from "vanjs-core";
import {capi} from "../capi/index.js";

const { div, h1, h2, p, span, table, thead, tbody, tr, th, td } = van.tags;

const formatTime = (t) => {
    if (!t) return '-';
    const d = t instanceof Date ? t : new Date(t);
    if (isNaN(d.getTime())) return '-';
    return d.toLocaleString();
};

export function clusterPage() {
    const machines = van.state(null);
    const error = van.state(null);

    const load = async () => {
        try {
            const res = await capi.getV1ClusterStatus();
            machines.val = res.machines || [];
        } catch (e) {
            error.val = e.message;
        }
    };

    load();

    return div(
        {class: "flex-1 min-h-0 overflow-auto p-6 flex flex-col gap-6"},
        h1({class: "text-xl font-bold"}, "Cluster"),
        () => {
            if (error.val) {
                return p({class: "text-red-400"}, `Error: ${error.val}`);
            }
            if (machines.val === null) {
                return p({class: "text-gray-400"}, "Loading...");
            }
            if (machines.val.length === 0) {
                return p({class: "text-gray-400"}, "No machines found.");
            }

            const sorted = [...machines.val].sort((a, b) => {
                if (a.isPrimary && !b.isPrimary) return -1;
                if (!a.isPrimary && b.isPrimary) return 1;
                return a.name.localeCompare(b.name);
            });

            return div(
                {class: "card"},
                table(
                    {class: "w-full text-sm"},
                    thead(
                        tr({class: "text-left text-gray-400 border-b border-gray-700"},
                            th({class: "pb-2 pr-6"}, "Machine"),
                            th({class: "pb-2 pr-6"}, "Role"),
                            th({class: "pb-2 pr-6"}, "Status"),
                            th({class: "pb-2"}, "Connected since"),
                        )
                    ),
                    tbody(
                        ...sorted.map(m =>
                            tr({class: "border-b border-gray-800 last:border-0"},
                                td({class: "py-3 pr-6 text-white font-medium"}, m.name),
                                td({class: "py-3 pr-6"},
                                    m.isPrimary
                                        ? span({class: "text-blue-400"}, "primary")
                                        : span({class: "text-gray-300"}, "secondary")
                                ),
                                td({class: "py-3 pr-6"},
                                    m.connected
                                        ? span({class: "text-green-400"}, "connected")
                                        : span({class: "text-red-400"}, "disconnected")
                                ),
                                td({class: "py-3 text-gray-400"},
                                    m.isPrimary ? '-' : formatTime(m.connectedAt)
                                ),
                            )
                        )
                    )
                )
            );
        }
    );
}
