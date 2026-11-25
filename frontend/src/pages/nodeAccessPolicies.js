import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {closeIcon, trashIcon} from "../lib/icons.js";
import {machinesS, nodeAccessPoliciesS, spacesS} from "../state/deployments.js";
import {
    MODE_EXACT,
    MODE_EXCEPT,
    alwaysEnabled,
    createPayload,
    describeNodes,
    describeSpaces,
    draftFromPolicy,
    draftNodes,
    draftProblems,
    draftRule,
    isBuiltin,
    isDeny,
    newDraft,
    policySentence,
    togglePayload,
    updatePayload,
    userSpaces,
} from "../lib/nodeAccessPolicies.js";

const {button, div, h2, input, label, p, span} = van.tags;

const ROW_COLS = "grid-cols-[minmax(0,1fr)_auto] gap-x-3 gap-y-1 " +
    "@3xl:grid-cols-[minmax(0,1.5fr)_minmax(0,1.25fr)_minmax(0,1.25fr)_7.5rem]";

const withTestId = (el, testId) => { el.setAttribute("data-testid", testId); return el; };

const actionButton = (text, onclick, cls, disabledWhen, testId) => withTestId(
    spinnerButton(text, onclick, `text-xs whitespace-nowrap ${cls}`, "button", disabledWhen, {base: "rounded-md px-3 py-1 font-medium"}), testId);
const fieldLabel = (text) => span({class: "text-[10px] uppercase tracking-wider text-gray-500 slate:text-[11px] slate:font-medium slate:normal-case slate:tracking-normal"}, text);

const smallBtn = (text, onclick, disabledWhen, testId) => button({
    type: "button",
    disabled: disabledWhen,
    ...(testId ? {"data-testid": testId} : {}),
    class: () => "text-xs px-3 py-1 rounded-md font-medium transition-colors bg-gray-700 text-gray-200 hover:bg-gray-600 " +
        (disabledWhen() ? "opacity-50 cursor-not-allowed" : "cursor-pointer"),
    onclick,
}, text);

const effectBadge = (deny, muted) => span({
    class: "w-11 flex-none rounded border py-px text-center text-[10px] font-medium uppercase tracking-wide " +
        (muted ? "border-gray-700 text-gray-500"
            : deny ? "border-red-500/40 bg-red-500/10 text-red-300" : "border-green-500/40 bg-green-500/10 text-green-300"),
}, deny ? "Deny" : "Allow");

const tag = (text, testId) => span(
    {class: "flex-none rounded bg-gray-800 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-gray-500", ...(testId ? {"data-testid": testId} : {})}, text);

const choiceButton = (state, value, text, testId, activeClass = "border-brand bg-brand/15 text-gray-100") => button({
    type: "button",
    "aria-pressed": () => String(state.val === value),
    "data-testid": testId,
    class: () => "rounded border px-2.5 py-1 text-xs cursor-pointer transition-colors " +
        (state.val === value ? activeClass : "border-gray-600 text-gray-400 hover:bg-surface-hover"),
    onclick: () => { state.val = value; },
}, text);

export function nodeAccessPoliciesPage() {
    const busyId = van.state(0);
    const rowError = van.state(null);
    const editor = van.state(null);
    const deleteTarget = van.state(null);
    const deleteSaving = van.state(false);
    const deleteError = van.state(null);

    const policies = () => [...(nodeAccessPoliciesS.val || [])].sort((a, b) => a.id - b.id);
    const liveNodes = () => (machinesS.val || []).filter((m) => !m.evicted);
    const nodeLabel = (m) => m.name || m.identifier || `node ${m.id}`;
    const nodeNames = () => new Map((machinesS.val || []).map((m) => [Number(m.id), nodeLabel(m)]));
    const spaceNames = () => new Map((spacesS.val || []).map((s) => [Number(s.id), s.name || `space ${s.id}`]));

    const toggle = async (policy) => {
        if (busyId.val) return;
        busyId.val = policy.id;
        rowError.val = null;
        try {
            await capi.postV1NodeAccessPoliciesUpdate(togglePayload(policy));
        } catch (e) {
            rowError.val = {id: policy.id, message: e.message};
        } finally {
            busyId.val = 0;
        }
    };

    const toolbar = () => div(
        {class: "page-toolbar cluster-toolbar gap-3 classic:py-2"},
        p({class: "min-w-0 flex-1 text-xs text-gray-500"},
            "Denies are checked first, then allows; a node runs a space's deployments only when an enabled allow covers it. ",
            "A change that would leave a deployment on a node that may no longer run it is refused."),
        button({
            type: "button",
            class: "toolbar-button flex-none",
            "data-testid": "node-access-policy-add",
            onclick: () => openEditor(null),
        }, "Add policy"),
    );

    const enableSwitch = (policy) => {
        const on = !policy.disabled;
        const locked = alwaysEnabled(policy);
        return button({
            type: "button",
            role: "switch",
            "aria-checked": String(on),
            "aria-label": `Policy ${policy.name} enabled`,
            title: locked ? "Always enabled" : on ? "Enabled" : "Disabled",
            "data-testid": `node-access-policy-toggle-${policy.id}`,
            disabled: () => locked || Boolean(busyId.val),
            class: () => "inline-flex h-6 items-center rounded px-1 " +
                (locked ? "cursor-not-allowed opacity-50" : busyId.val === policy.id ? "cursor-wait opacity-60" : busyId.val ? "cursor-not-allowed" : "cursor-pointer"),
            onclick: () => toggle(policy),
        }, span(
            {class: `relative inline-flex h-4 w-7 items-center rounded-full transition-colors ${on ? "bg-brand" : "bg-gray-600"}`},
            span({class: `inline-block h-3 w-3 rounded-full bg-white transition-transform ${on ? "translate-x-3.5" : "translate-x-0.5"}`}),
        ));
    };

    const summary = (heading, text, cls) => div(
        {class: "col-span-2 flex min-w-0 items-baseline gap-2 @3xl:col-span-1"},
        span({class: "w-14 flex-none text-[10px] uppercase tracking-wider text-gray-500 @3xl:hidden"}, heading),
        span({class: `min-w-0 break-words @3xl:truncate ${cls}`, title: text}, text),
    );

    const policyRow = (policy, names) => {
        const off = policy.disabled;
        const tone = off ? "text-gray-500" : "text-gray-300";
        const builtin = isBuiltin(policy);
        return div(
            {class: `data-row grid ${ROW_COLS} items-center py-1.5 @3xl:py-px classic:border-gray-800 classic:last:border-0 classic:hover:bg-transparent`, "data-testid": `node-access-policy-row-${policy.id}`},
            div({class: "flex min-w-0 items-center gap-2"},
                effectBadge(isDeny(policy.rule), off),
                span({class: `truncate ${off ? "text-gray-500" : "text-gray-200"}`, title: policy.name}, policy.name),
                builtin ? tag("built-in", `node-access-policy-builtin-${policy.id}`) : "",
                off ? tag("off") : ""),
            div({class: "flex items-center justify-end gap-1 @3xl:order-last @3xl:justify-start"},
                enableSwitch(policy),
                builtin ? "" : button({
                    type: "button",
                    title: `Edit policy ${policy.name}`,
                    "aria-label": `Edit policy ${policy.name}`,
                    "data-testid": `node-access-policy-edit-${policy.id}`,
                    class: "inline-flex h-6 items-center justify-center rounded px-2 text-xs text-gray-400 hover:bg-surface-hover hover:text-gray-100 transition-colors cursor-pointer",
                    onclick: () => openEditor(policy),
                }, "Edit"),
                builtin ? "" : button({
                    type: "button",
                    title: `Delete policy ${policy.name}`,
                    "aria-label": `Delete policy ${policy.name}`,
                    "data-testid": `node-access-policy-delete-${policy.id}`,
                    class: "inline-flex h-6 w-6 items-center justify-center rounded text-gray-400 hover:bg-surface-hover hover:text-red-400 transition-colors cursor-pointer",
                    onclick: () => { deleteError.val = null; deleteTarget.val = policy; },
                }, trashIcon())),
            summary("Nodes", describeNodes(policy.nodes, names.nodes), `font-mono ${tone}`),
            summary("Spaces", describeSpaces(policy.rule?.selector?.spaces, names.spaces), tone),
            () => rowError.val?.id === policy.id ? div(
                {class: "order-last col-span-full flex items-start gap-2 rounded border border-red-500/30 bg-red-500/10 px-2 py-1 text-xs text-red-300"},
                span({class: "min-w-0 flex-1 break-words", "data-testid": "node-access-policy-error"}, rowError.val.message),
                button({
                    type: "button",
                    "aria-label": "Dismiss",
                    class: "flex-none cursor-pointer text-red-300/70 hover:text-red-200",
                    onclick: () => { rowError.val = null; },
                }, closeIcon({class: "h-3.5 w-3.5"}))) : "",
        );
    };

    const headerCell = (text, cls = "") => span({class: `col-head classic:text-[10px] ${cls}`}, text);

    // The container query sits on the scroller, not the page, so the fixed
    // overlays below are never positioned against it.
    const listArea = () => div(
        {class: "app-scroll @container flex-1 min-h-0 overflow-auto"}, () => {
            const list = policies();
            if (!list.length) {
                return p({class: "px-4 py-3 text-sm text-gray-400", "data-testid": "node-access-policy-empty"},
                    "No node access policies visible. Managing them needs the access permission.");
            }
            const names = {nodes: nodeNames(), spaces: spaceNames()};
            return div({class: "pl-4 pr-2 text-[13px] slate:px-3 graphite:px-3"},
                div({class: `hidden @3xl:grid ${ROW_COLS} items-center border-b border-gray-800 py-1.5 slate:h-7 slate:border-line slate:py-0 graphite:h-6 graphite:border-0 graphite:py-0`},
                    headerCell("Policy"), headerCell("Nodes"), headerCell("Spaces"), headerCell("Enabled", "pl-1")),
                ...list.map((policy) => policyRow(policy, names)));
        });

    // The field states are made here, outside the overlay's binding, so a
    // stream update while the dialog is open cannot rebuild it over the draft.
    function openEditor(policy) {
        const draft = policy ? draftFromPolicy(policy) : newDraft();
        editor.val = {
            policy,
            fields: Object.fromEntries(Object.entries(draft).map(([key, value]) => [key, van.state(value)])),
            error: van.state(null),
            saving: van.state(false),
        };
    }

    const editorOverlay = ({policy, fields, error, saving}) => {
        const draft = () => Object.fromEntries(Object.entries(fields).map(([key, state]) => [key, state.val]));
        const close = () => { if (!saving.val) editor.val = null; };

        const save = async () => {
            if (saving.val) return;
            saving.val = true;
            error.val = null;
            try {
                if (policy) await capi.postV1NodeAccessPoliciesUpdate(updatePayload(policy, draft()));
                else await capi.postV1NodeAccessPoliciesCreate(createPayload(draft()));
                editor.val = null;
            } catch (e) {
                error.val = e.message;
            } finally {
                saving.val = false;
            }
        };

        const chips = (state, kind, items) => div({class: "flex flex-wrap items-center gap-1.5"},
            ...items.map(([id, text]) => {
                const on = () => state.val.includes(id);
                return button({
                    type: "button",
                    "aria-pressed": () => String(on()),
                    "data-testid": `node-access-policy-${kind}-${id}`,
                    class: () => "rounded border px-2 py-0.5 font-mono text-xs cursor-pointer transition-colors " +
                        (on() ? "border-brand bg-brand/15 text-gray-100" : "border-gray-600 text-gray-400 hover:bg-surface-hover"),
                    onclick: () => { state.val = on() ? state.val.filter((v) => v !== id) : [...state.val, id]; },
                }, text);
            }));

        const nodeChips = () => {
            const names = nodeNames();
            const listed = liveNodes().map((m) => Number(m.id));
            const ids = [...listed, ...fields.nodeIds.rawVal.filter((id) => !listed.includes(id))];
            return chips(fields.nodeIds, "node", ids.map((id) => [id, names.get(id) ?? `node #${id}`]));
        };

        const spaceChips = () => {
            const names = spaceNames();
            const listed = userSpaces(spacesS.val).map((s) => Number(s.id));
            const ids = [...listed, ...fields.spaceIds.rawVal.filter((id) => !listed.includes(id))];
            return chips(fields.spaceIds, "space", ids.map((id) => [id, names.get(id) ?? `space #${id}`]));
        };

        const field = (heading, ...body) => div({class: "flex flex-col gap-1.5"}, fieldLabel(heading), ...body);
        const choices = (...buttons) => div({class: "flex flex-wrap items-center gap-2"}, ...buttons);

        const nameInput = input({
            class: "text-input text-sm",
            placeholder: "policy_name",
            maxLength: 64,
            spellcheck: false,
            "data-testid": "node-access-policy-name",
            disabled: saving,
            value: fields.name,
            oninput: (e) => { fields.name.val = e.target.value; },
        });
        setTimeout(() => nameInput.focus());

        const title = policy ? `Edit node access policy ${policy.name}` : "Add node access policy";
        return div(
            {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"},
            div({
                class: "dialog flex max-h-[88vh] w-full max-w-xl flex-col overflow-hidden classic:rounded-[0.3rem] classic:border-gray-700 classic:bg-surface",
                role: "dialog",
                "aria-modal": "true",
                "aria-label": title,
                "data-testid": "node-access-policy-editor",
                onkeydown: (e) => { if (e.key === "Escape") close(); },
            },
                div({class: "flex-none border-b border-gray-800 px-4 py-3 slate:border-line graphite:border-line"},
                    h2({class: "truncate text-base font-semibold"}, title)),
                div({class: "app-scroll flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 py-4"},
                    field("Name", nameInput),
                    field("Effect", choices(
                        choiceButton(fields.deny, false, "Allow", "node-access-policy-effect-allow", "border-green-500/50 bg-green-500/10 text-green-300"),
                        choiceButton(fields.deny, true, "Deny", "node-access-policy-effect-deny", "border-red-500/50 bg-red-500/10 text-red-300"))),
                    field("Nodes",
                        choices(
                            choiceButton(fields.nodesMode, MODE_EXACT, "These nodes", "node-access-policy-nodes-mode-exact"),
                            choiceButton(fields.nodesMode, MODE_EXCEPT, "All nodes except", "node-access-policy-nodes-mode-except")),
                        nodeChips),
                    field("Spaces they run",
                        choices(
                            choiceButton(fields.spacesMode, MODE_EXACT, "These spaces", "node-access-policy-spaces-mode-exact"),
                            choiceButton(fields.spacesMode, MODE_EXCEPT, "All spaces except", "node-access-policy-spaces-mode-except")),
                        spaceChips),
                    label({class: "flex cursor-pointer select-none items-center gap-2 self-start text-xs text-gray-300"},
                        input({
                            type: "checkbox",
                            class: "cursor-pointer accent-blue-500",
                            "data-testid": "node-access-policy-enabled",
                            checked: fields.enabled,
                            onchange: (e) => { fields.enabled.val = e.target.checked; },
                        }),
                        "Enabled"),
                    field("Reads as", div(
                        {class: "break-words rounded-md border border-gray-800 bg-gray-950/40 px-3 py-2 text-xs text-gray-300 slate:border-line slate:bg-field graphite:border-line", "data-testid": "node-access-policy-preview"},
                        () => policySentence({nodes: draftNodes(draft()), rule: draftRule(draft())}, nodeNames(), spaceNames())))),
                div({class: "flex flex-none flex-col gap-2 border-t border-gray-800 px-4 py-3 slate:border-line graphite:border-line"},
                    () => error.val ? p({class: "break-words text-sm text-red-400", "data-testid": "node-access-policy-error"}, error.val) : "",
                    div({class: "flex items-center justify-end gap-2"},
                        span({class: "min-w-0 flex-1 break-words text-xs text-gray-500", "data-testid": "node-access-policy-problem"},
                            () => draftProblems(draft())[0] || ""),
                        smallBtn("Cancel", close, () => saving.val, "node-access-policy-cancel"),
                        actionButton(policy ? "Save" : "Create", save, "bg-brand text-white hover:bg-blue-600",
                            () => saving.val || draftProblems(draft()).length > 0, "node-access-policy-save")))),
        );
    };

    const confirmDelete = async () => {
        const target = deleteTarget.val;
        if (!target || deleteSaving.val) return;
        deleteSaving.val = true;
        deleteError.val = null;
        try {
            await capi.postV1NodeAccessPoliciesDelete({id: target.id});
            deleteTarget.val = null;
        } catch (e) {
            deleteError.val = e.message;
        } finally {
            deleteSaving.val = false;
        }
    };

    const deleteOverlay = () => {
        const target = deleteTarget.val;
        if (!target) return "";
        return div(
            {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"},
            div(
                {class: "card w-full max-w-md flex flex-col gap-4 shadow-2xl", "data-testid": "node-access-policy-delete-dialog"},
                h2({class: "text-base font-semibold"}, "Confirm delete"),
                p({class: "break-words text-sm text-gray-300"},
                    `Delete node access policy ${target.name}? It says: ${policySentence(target, nodeNames(), spaceNames())}.`),
                () => deleteError.val ? p({class: "break-words text-sm text-red-400", "data-testid": "node-access-policy-error"}, deleteError.val) : "",
                div({class: "flex items-center justify-end gap-2"},
                    smallBtn("Cancel", () => { if (!deleteSaving.val) deleteTarget.val = null; }, () => deleteSaving.val),
                    actionButton("Confirm", confirmDelete, "bg-red-600 text-white hover:bg-red-500",
                        () => deleteSaving.val, "node-access-policy-delete-confirm"),
                ),
            ),
        );
    };

    return div(
        {class: "h-full min-h-0 flex flex-col overflow-hidden"},
        toolbar(),
        listArea(),
        () => editor.val ? editorOverlay(editor.val) : "",
        deleteOverlay,
    );
}
