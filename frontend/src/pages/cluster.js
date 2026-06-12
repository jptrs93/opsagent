import van from "vanjs-core";
import {capi} from "../capi/index.js";

const { button, div, h1, h2, input, p, span, table, tbody, td, th, thead, tr } = van.tags;

const formatTime = (t) => {
    if (!t) return '-';
    const d = t instanceof Date ? t : new Date(t);
    if (isNaN(d.getTime())) return '-';
    return d.toLocaleString();
};

export function clusterPage() {
    const machines = van.state(null);
    const enrollments = van.state(null);
    const error = van.state(null);

    const load = async () => {
        try {
            const [clusterRes, enrollmentRes] = await Promise.all([
                capi.getV1ClusterStatus(),
                capi.postV1EnrollmentList(),
            ]);
            machines.val = clusterRes.machines || [];
            enrollments.val = enrollmentRes.items || [];
            error.val = null;
        } catch (e) {
            error.val = e.message;
        }
    };

    load();

    return div(
        {class: "flex-1 min-h-0 overflow-auto p-6 flex flex-col gap-6"},
        h1({class: "text-xl font-bold"}, "Machines"),
        () => {
            if (error.val) {
                return p({class: "text-red-400"}, `Error: ${error.val}`);
            }
            if (machines.val === null || enrollments.val === null) {
                return p({class: "text-gray-400"}, "Loading...");
            }

            const sorted = [...machines.val].sort((a, b) => {
                if (a.isPrimary && !b.isPrimary) return -1;
                if (!a.isPrimary && b.isPrimary) return 1;
                return a.name.localeCompare(b.name);
            });
            const pending = enrollments.val.filter(e => e.status === "waiting");

            return div(
                {class: "flex flex-col gap-6"},
                div(
                    {class: "card"},
                    h2({class: "font-semibold mb-4"}, "Connected machines"),
                    sorted.length === 0
                        ? p({class: "text-gray-400 text-sm"}, "No machines found.")
                        : table(
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
                ),
                div(
                    {class: "card"},
                    h2({class: "font-semibold mb-2"}, "Enrollment requests"),
                    p({class: "text-sm text-gray-400 mb-4"}, "Accept a waiting worker to issue its cluster client certificate."),
                    pending.length === 0
                        ? p({class: "text-gray-400 text-sm"}, "No pending enrollment requests.")
                        : table(
                            {class: "w-full text-sm"},
                            thead(
                                tr({class: "text-left text-gray-400 border-b border-gray-700"},
                                    th({class: "pb-2 pr-6"}, "Request"),
                                    th({class: "pb-2 pr-6"}, "Remote IP"),
                                    th({class: "pb-2 pr-6"}, "Updated"),
                                    th({class: "pb-2"}, "Accept as"),
                                )
                            ),
                            tbody(...pending.map(req => enrollmentRow(req, load)))
                        )
                )
            );
        }
    );
}

function enrollmentRow(req, reload) {
    const workerName = van.state(`worker-${req.id}`);
    const accepting = van.state(false);
    const rowError = van.state(null);

    const accept = async () => {
        const name = workerName.val.trim();
        if (!name) {
            rowError.val = "Worker name is required";
            return;
        }
        accepting.val = true;
        rowError.val = null;
        try {
            await capi.postV1EnrollmentAccept({id: req.id, workerName: name});
            await reload();
        } catch (e) {
            rowError.val = e.message;
        } finally {
            accepting.val = false;
        }
    };

    return tr(
        {class: "border-b border-gray-800 last:border-0 align-top"},
        td({class: "py-3 pr-6"},
            div({class: "text-white font-medium"}, `#${req.id}`),
            div({class: "text-xs text-gray-500 font-mono break-all"}, req.requestingMachineId),
        ),
        td({class: "py-3 pr-6 text-gray-300"}, req.requestingIpAddress || "-"),
        td({class: "py-3 pr-6 text-gray-400"}, formatTime(req.updatedAt)),
        td({class: "py-3"},
            div({class: "flex flex-col gap-2"},
                div({class: "flex gap-2"},
                    input({
                        class: "text-input w-44",
                        value: workerName,
                        oninput: e => workerName.val = e.target.value,
                    }),
                    button({
                        type: "button",
                        class: "btn-primary",
                        disabled: () => accepting.val,
                        onclick: accept,
                    }, () => accepting.val ? "Accepting..." : "Accept"),
                ),
                () => rowError.val ? p({class: "text-xs text-red-400"}, rowError.val) : null,
            )
        ),
    );
}
