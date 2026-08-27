import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {trashIcon} from "../lib/icons.js";
import {deploymentsS, networkPoliciesS, spacesS} from "../state/deployments.js";
import {
    formatPorts,
    parsePorts,
    resolvePolicyPeer,
    PEER_KIND_DEPLOYMENT,
    PEER_KIND_SPACE,
    POLICY_ACTION_ALLOW,
} from "../lib/networkPolicies.js";

const { div, h2, p, span, input, button, select, option, table, thead, tbody, tr, th, td, colgroup, col } = van.tags;

const smallBtn = (text, onclick, cls, disabledWhen) => button({
    type: "button",
    disabled: disabledWhen,
    class: () => `text-xs px-3 py-1 rounded-md font-medium transition-colors ${cls} ${
        disabledWhen && disabledWhen() ? "opacity-50 cursor-not-allowed" : "cursor-pointer"}`,
    onclick: async (e) => { if (disabledWhen && disabledWhen()) return; await onclick(e); },
}, text);

export function networkPoliciesPage() {
    const error = van.state(null);
    const search = van.state("");
    const formOpen = van.state(false);
    const saving = van.state(false);
    const editing = van.state(null);
    const deleteTarget = van.state(null);
    const deleteSaving = van.state(false);

    const sourceKind = van.state(PEER_KIND_SPACE);
    const sourceId = van.state(0);
    const destinationKind = van.state(PEER_KIND_SPACE);
    const destinationId = van.state(0);
    const portsText = van.state("");

    const activeSpaces = () => (spacesS.val || []).filter((s) => s && !s.deleted);
    const activeDeployments = () => (deploymentsS.val || [])
        .filter((d) => d?.config && !d.config.deleted)
        .sort((a, b) => (a.config.name || "").localeCompare(b.config.name || ""));

    const sortedPolicies = () => [...(networkPoliciesS.val || [])]
        .filter((policy) => policy && !policy.deleted)
        .sort((a, b) => Number(a.id || 0) - Number(b.id || 0));

    const filteredPolicies = () => {
        const query = search.val.trim().toLowerCase();
        const policies = sortedPolicies();
        if (!query) return policies;
        return policies.filter((policy) => {
            const source = resolvePolicyPeer(policy.source, spacesS.val, deploymentsS.val);
            const destination = resolvePolicyPeer(policy.destination, spacesS.val, deploymentsS.val);
            return `${source.label} ${destination.label} ${formatPorts(policy.ports)}`.toLowerCase().includes(query);
        });
    };

    const openCreateForm = () => {
        editing.val = null;
        sourceKind.val = PEER_KIND_SPACE;
        sourceId.val = -1;
        destinationKind.val = PEER_KIND_SPACE;
        destinationId.val = -1;
        portsText.val = "";
        formOpen.val = true;
    };

    const openEditForm = (policy) => {
        editing.val = {id: Number(policy.id), version: Number(policy.version)};
        sourceKind.val = Number(policy.source?.kind || PEER_KIND_SPACE);
        sourceId.val = Number(policy.source?.id || 0);
        destinationKind.val = Number(policy.destination?.kind || PEER_KIND_SPACE);
        destinationId.val = Number(policy.destination?.id || 0);
        portsText.val = policy.ports?.length ? formatPorts(policy.ports) : "";
        formOpen.val = true;
    };

    const closeForm = () => {
        if (saving.val) return;
        formOpen.val = false;
        editing.val = null;
    };

    const peerValid = (kind, id) => Number(kind) === PEER_KIND_SPACE
        ? activeSpaces().some((s) => Number(s.id) === Number(id))
        : Number(id) > 0;

    const formValid = () => peerValid(sourceKind.val, sourceId.val) && peerValid(destinationKind.val, destinationId.val);

    const savePolicy = async () => {
        if (saving.val) return;
        const {ports, error: portsError} = parsePorts(portsText.val);
        if (portsError) {
            error.val = portsError;
            return;
        }
        const body = {
            action: POLICY_ACTION_ALLOW,
            source: {kind: Number(sourceKind.val), id: Number(sourceId.val)},
            destination: {kind: Number(destinationKind.val), id: Number(destinationId.val)},
            ports,
        };
        try {
            saving.val = true;
            error.val = null;
            if (editing.val) {
                await capi.postV1NetworkPoliciesUpdate({...body, id: editing.val.id, version: editing.val.version});
            } else {
                await capi.postV1NetworkPoliciesCreate(body);
            }
            formOpen.val = false;
            editing.val = null;
        } catch (e) {
            error.val = e.message;
        } finally {
            saving.val = false;
        }
    };

    const confirmDelete = async () => {
        const target = deleteTarget.val;
        if (!target || deleteSaving.val) return;
        try {
            deleteSaving.val = true;
            error.val = null;
            await capi.postV1NetworkPoliciesDelete({id: target.id});
            deleteTarget.val = null;
        } catch (e) {
            error.val = e.message;
        } finally {
            deleteSaving.val = false;
        }
    };

    const peerSelects = (kindState, idState, side) => {
        const selectClass = "text-input text-sm py-1";
        return div({class: "flex items-center gap-1.5 min-w-0"},
            select({
                class: selectClass,
                "aria-label": `${side} kind`,
                disabled: saving,
                onchange: (e) => {
                    kindState.val = Number(e.target.value);
                    idState.val = -1;
                },
            },
                option({value: PEER_KIND_SPACE, selected: () => kindState.val === PEER_KIND_SPACE}, "space"),
                option({value: PEER_KIND_DEPLOYMENT, selected: () => kindState.val === PEER_KIND_DEPLOYMENT}, "deployment"),
            ),
            () => Number(kindState.val) === PEER_KIND_SPACE
                ? select({
                    class: selectClass,
                    "aria-label": `${side} space`,
                    disabled: saving,
                    onchange: (e) => { idState.val = Number(e.target.value); },
                },
                    option({value: -1, selected: () => !activeSpaces().some((s) => Number(s.id) === Number(idState.val))}, "select space"),
                    ...activeSpaces().map((s) => option(
                        {value: s.id, selected: () => Number(idState.val) === Number(s.id)},
                        s.name || `space ${s.id}`)),
                )
                : select({
                    class: selectClass,
                    "aria-label": `${side} deployment`,
                    disabled: saving,
                    onchange: (e) => { idState.val = Number(e.target.value); },
                },
                    option({value: -1, selected: () => !activeDeployments().some((d) => Number(d.config.id) === Number(idState.val))}, "select deployment"),
                    ...activeDeployments().map((d) => option(
                        {value: d.config.id, selected: () => Number(idState.val) === Number(d.config.id)},
                        `${d.config.name || d.config.id} (space ${d.config.spaceId ?? 0})`)),
                ),
        );
    };

    const formRow = () => formOpen.val ? div(
        {class: "flex flex-none flex-wrap items-center gap-2 border-b border-gray-700 px-3 py-2", "data-testid": "network-policy-form"},
        span({class: "text-xs uppercase tracking-wide text-gray-500"}, "allow"),
        peerSelects(sourceKind, sourceId, "Source"),
        span({class: "text-gray-500"}, "→"),
        peerSelects(destinationKind, destinationId, "Destination"),
        input({
            class: "text-input text-sm py-1 w-56",
            placeholder: "ports, e.g. tcp/443, udp/1000-2000",
            "aria-label": "Ports",
            disabled: saving,
            value: portsText,
            oninput: (e) => { portsText.val = e.target.value; },
            onkeydown: (e) => {
                if (e.key === "Enter") void savePolicy();
                if (e.key === "Escape") closeForm();
            },
        }),
        div({class: "flex items-center gap-2"},
            spinnerButton(editing.val ? "Save" : "Create", savePolicy,
                "text-xs px-3 py-1 rounded-md font-medium bg-brand text-white hover:bg-blue-600 whitespace-nowrap",
                "button", () => saving.val || !formValid()),
            smallBtn("Discard", closeForm, "bg-gray-700 text-gray-200 hover:bg-gray-600", () => saving.val),
        ),
    ) : "";

    const peerCell = (peer) => {
        const resolved = resolvePolicyPeer(peer, spacesS.val, deploymentsS.val);
        return td({class: "py-1 pr-3 min-w-0"},
            div({class: "flex items-center gap-1.5 min-w-0"},
                span({class: `truncate ${resolved.dangling ? "text-amber-400" : "text-gray-200"}`}, resolved.label),
                resolved.dangling
                    ? span({class: "shrink-0 rounded bg-amber-500/15 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-amber-400",
                        title: "This rule references a deleted space or deployment. It allows nothing until removed."}, "dangling")
                    : "",
            ));
    };

    const policyRow = (policy) => tr(
        {class: "border-b border-gray-800 last:border-0 align-middle", "data-testid": `network-policy-row-${policy.id}`},
        peerCell(policy.source),
        peerCell(policy.destination),
        td({class: "py-1 pr-3 text-gray-400 whitespace-nowrap"}, formatPorts(policy.ports)),
        td({class: "py-1 pl-2 text-right whitespace-nowrap w-px"},
            div({class: "flex items-center justify-end gap-1"},
                button({
                    type: "button",
                    title: `Edit policy ${policy.id}`,
                    "aria-label": `Edit policy ${policy.id}`,
                    class: "inline-flex h-7 items-center justify-center rounded px-2 text-xs text-gray-400 hover:bg-surface hover:text-gray-100 transition-colors cursor-pointer",
                    onclick: () => openEditForm(policy),
                }, "Edit"),
                button({
                    type: "button",
                    title: `Delete policy ${policy.id}`,
                    "aria-label": `Delete policy ${policy.id}`,
                    class: "inline-flex h-7 w-7 items-center justify-center rounded text-gray-400 hover:bg-surface hover:text-red-400 transition-colors cursor-pointer",
                    onclick: () => { deleteTarget.val = policy; },
                }, trashIcon()),
            )),
    );

    const headerCell = (text, cls = "pr-3") => th(
        {class: `py-1.5 ${cls} text-[10px] font-semibold uppercase tracking-wider`}, text);

    const toolbar = () => div(
        {class: "flex flex-none flex-wrap items-center gap-2 border-b border-gray-700 px-2 py-2"},
        input({
            class: "text-input search-input",
            type: "search",
            placeholder: "Search policies",
            "aria-label": "Search policies",
            value: search,
            oninput: (e) => search.val = e.target.value,
        }),
        div({class: "flex-1"}),
        button({
            type: "button",
            disabled: () => formOpen.val,
            class: () => "flex items-center gap-1 whitespace-nowrap text-sm px-3 py-1.5 rounded-lg bg-gray-700 " +
                `text-gray-200 hover:bg-gray-600 transition-colors cursor-pointer ${formOpen.val ? "opacity-50 cursor-not-allowed" : ""}`,
            onclick: () => { if (!formOpen.val) openCreateForm(); },
        }, "Add policy"),
    );

    const explainer = () => p(
        {class: "flex-none border-b border-gray-800 px-3 py-1.5 text-xs text-gray-500"},
        "Traffic within a space, to the global space, and to DNS is always allowed. ",
        "Policies grant additional cross-space access; writing one requires update access on the destination's space.",
    );

    const tableArea = () => div(
        {class: "app-scroll flex-1 min-h-0 overflow-auto"}, () => {
            const visible = filteredPolicies();
            if (!visible.length) {
                return p({class: "px-4 py-3 text-gray-400 text-sm"},
                    search.val.trim() ? "No policies match your search." : "No override policies. Cross-space traffic is denied by default.");
            }
            return div({class: "pl-4 pr-2"}, table(
                {class: "w-full table-fixed text-[13px]"},
                colgroup(
                    col({style: "width:33%"}),
                    col({style: "width:33%"}),
                    col({style: "width:24%"}),
                    col({style: "width:10%"}),
                ),
                thead(tr({class: "text-left text-gray-500 border-b border-gray-800"},
                    headerCell("Source"),
                    headerCell("Destination"),
                    headerCell("Ports"),
                    headerCell("", "w-px"))),
                tbody(...visible.map(policyRow)),
            ));
        });

    const deleteOverlay = () => {
        const target = deleteTarget.val;
        if (!target) return "";
        const source = resolvePolicyPeer(target.source, spacesS.val, deploymentsS.val);
        const destination = resolvePolicyPeer(target.destination, spacesS.val, deploymentsS.val);
        return div(
            {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"},
            div(
                {class: "card w-full max-w-md flex flex-col gap-4 shadow-2xl"},
                h2({class: "text-base font-semibold"}, "Confirm delete"),
                p({class: "text-sm text-gray-300"},
                    `Delete the rule allowing ${source.label} → ${destination.label}? Existing connections keep flowing until they close.`),
                div({class: "flex items-center justify-end gap-2"},
                    smallBtn("Cancel", () => { if (!deleteSaving.val) deleteTarget.val = null; },
                        "bg-gray-700 text-gray-200 hover:bg-gray-600", () => deleteSaving.val),
                    spinnerButton("Confirm", confirmDelete,
                        "text-xs px-3 py-1 rounded-md font-medium bg-red-600 text-white hover:bg-red-500",
                        "button", () => deleteSaving.val),
                ),
            ),
        );
    };

    return div(
        {class: "h-full min-h-0 flex flex-col overflow-hidden bg-surface"},
        toolbar(),
        explainer(),
        formRow,
        () => error.val ? p(
            {class: "flex-none border-b border-red-500/30 bg-red-500/10 px-3 py-1.5 text-xs text-red-300"},
            `Error: ${error.val}`) : "",
        tableArea,
        deleteOverlay,
    );
}
