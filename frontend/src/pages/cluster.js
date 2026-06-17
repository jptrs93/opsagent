import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {deploymentsStreamS, enrollmentsS, machinesS} from "../state/deployments.js";

const { button, div, h2, input, p, span, table, tbody, td, th, thead, tr } = van.tags;

const formatTime = (t) => {
    if (!t) return '-';
    const d = t instanceof Date ? t : new Date(t);
    if (isNaN(d.getTime())) return '-';
    return d.toLocaleString();
};

export function clusterPage() {
    return div(
        {class: "flex-1 min-h-0 overflow-auto p-3 flex flex-col gap-3"},
        () => {
            if (deploymentsStreamS.val.status !== "connected" && machinesS.val.length === 0) {
                return p({class: "text-gray-400"}, "Loading...");
            }

            const sorted = [...machinesS.val].sort((a, b) => {
                if (a.isPrimary && !b.isPrimary) return -1;
                if (!a.isPrimary && b.isPrimary) return 1;
                return a.name.localeCompare(b.name);
            });
            const pending = enrollmentsS.val.filter(e => e.status === "waiting");

            return div(
                {class: "flex flex-col gap-3"},
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
                                    tr({class: "border-b border-gray-800 last:border-0", "data-testid": `machine-row-${m.name}`},
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
                            tbody(...pending.map(req => enrollmentRow(req)))
                        )
                )
            );
        }
    );
}

function enrollmentRow(req) {
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
        } catch (e) {
            rowError.val = e.message;
        } finally {
            accepting.val = false;
        }
    };

    return tr(
        {class: "border-b border-gray-800 last:border-0 align-top", "data-testid": `enrollment-request-${req.id}`},
		td({class: "py-3 pr-6"},
			div({class: "text-white font-medium"}, `#${req.id}`),
			div({class: "text-xs text-gray-500 font-mono break-all"}, req.requestingMachineId),
			req.opendeployVersion ? div({class: "text-xs text-gray-400 font-mono"}, req.opendeployVersion) : '',
		),
        td({class: "py-3 pr-6 text-gray-300"}, req.requestingIpAddress || "-"),
        td({class: "py-3 pr-6 text-gray-400"}, formatTime(req.updatedAt)),
        td({class: "py-3"},
            div({class: "flex flex-col gap-2"},
                div({class: "flex gap-2"},
                    input({
                        "data-testid": "enrollment-worker-name-input",
                        class: "text-input w-44",
                        value: workerName,
                        oninput: e => workerName.val = e.target.value,
                    }),
                    button({
                        type: "button",
                        "data-testid": "enrollment-accept-button",
                        class: "btn-primary",
                        disabled: () => accepting.val,
                        onclick: accept,
                    }, () => accepting.val ? "Accepting..." : "Accept"),
                ),
				() => rowError.val ? p({class: "text-xs text-red-400"}, rowError.val) : '',
			)
		),
    );
}
