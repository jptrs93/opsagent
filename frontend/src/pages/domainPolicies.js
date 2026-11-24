// The Domain policies tab of the Spaces page: which spaces may publish which
// ingress hostnames.

import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {spinnerButton} from "../components/spinnerbutton.js";
import {closeIcon, infoIcon, trashIcon} from "../lib/icons.js";
import {spaceDomainPoliciesS, spacesS} from "../state/deployments.js";
import {
    MODE_EXACT,
    MODE_EXCEPT,
    canonicalDomain,
    createPayload,
    decide,
    describeDomains,
    describeSpaces,
    draftFromPolicy,
    draftProblems,
    draftRule,
    isDeny,
    newDraft,
    openToEverySpace,
    ruleSentence,
    togglePayload,
    updatePayload,
    userSpaces,
} from "../lib/domainPolicies.js";

const {button, div, h2, input, label, option, p, select, span, textarea} = van.tags;

// One row is a grid: stacked while the pane is narrow, a table line once it
// is wide. The controls sit second in the markup so they share the first
// line with the name when stacked.
const ROW_COLS = "grid-cols-[minmax(0,1fr)_auto] gap-x-3 gap-y-1 " +
    "@3xl:grid-cols-[minmax(0,1.5fr)_minmax(0,1fr)_minmax(0,1.5fr)_7.5rem]";

const mono = (text) => span({class: "font-mono text-gray-400"}, text);
const withTestId = (el, testId) => { el.setAttribute("data-testid", testId); return el; };

// spinnerButton at the size of the small button beside it.
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

const choiceButton = (state, value, text, testId, activeClass = "border-brand bg-brand/15 text-gray-100") => button({
    type: "button",
    "aria-pressed": () => String(state.val === value),
    "data-testid": testId,
    class: () => "rounded border px-2.5 py-1 text-xs cursor-pointer transition-colors " +
        (state.val === value ? activeClass : "border-gray-600 text-gray-400 hover:bg-surface-hover"),
    onclick: () => { state.val = value; },
}, text);

export function domainPoliciesPage() {
    const busyId = van.state(0);
    // {id, message}: the server's refusal of the last toggle, shown under its row.
    const rowError = van.state(null);
    const editor = van.state(null);
    const deleteTarget = van.state(null);
    const deleteSaving = van.state(false);
    const deleteError = van.state(null);
    const checkSpace = van.state(0);
    const checkHost = van.state("");

    const policies = () => [...(spaceDomainPoliciesS.val || [])].sort((a, b) => a.id - b.id);
    const spaceNames = () => new Map((spacesS.val || []).map((s) => [Number(s.id), s.name || `space ${s.id}`]));

    // The switch always shows the stream's state: a refused toggle leaves it
    // where it was, an accepted one moves when the update arrives.
    const toggle = async (policy) => {
        if (busyId.val) return;
        busyId.val = policy.id;
        rowError.val = null;
        try {
            await capi.postV1SpaceDomainPoliciesUpdate(togglePayload(policy));
        } catch (e) {
            rowError.val = {id: policy.id, message: e.message};
        } finally {
            busyId.val = 0;
        }
    };

    // --- hostname check ---------------------------------------------------------

    const checkSpaceId = () => {
        const ids = userSpaces(spacesS.val).map((s) => Number(s.id));
        return ids.includes(checkSpace.val) ? checkSpace.val : ids[0];
    };

    const checkResult = () => {
        if (!checkHost.val.trim() || !policies().length) return "";
        const hostname = canonicalDomain(checkHost.val);
        const result = (cls, text) => span({class: `text-xs ${cls}`, "data-testid": "domain-policy-check-result"}, text);
        if (!hostname || hostname.startsWith("*.")) return result("text-gray-500", "Not a hostname");
        const decision = decide(policies(), checkSpaceId(), hostname);
        if (decision.allowed) return result("text-green-300", "Allowed");
        return result("text-red-300", decision.policy ? `Refused by ${decision.policy}` : "Refused: no enabled allow covers it");
    };

    const toolbar = () => div(
        {class: "page-toolbar cluster-toolbar gap-2 classic:py-2"},
        span({class: "text-xs text-gray-400"}, "Check a hostname"),
        () => select({
            class: "input h-[30px] max-w-40 text-xs",
            "aria-label": "Space to check",
            "data-testid": "domain-policy-check-space",
            onchange: (e) => { checkSpace.val = Number(e.target.value); },
        }, ...userSpaces(spacesS.val).map((s) => option({value: s.id, selected: Number(s.id) === checkSpaceId()}, s.name || `space ${s.id}`))),
        input({
            class: "text-input search-input toolbar-input",
            placeholder: "shop.example.com",
            "aria-label": "Hostname to check",
            "data-testid": "domain-policy-check-hostname",
            spellcheck: false,
            value: checkHost,
            oninput: (e) => { checkHost.val = e.target.value; },
        }),
        checkResult,
        div({class: "flex-1"}),
        button({
            type: "button",
            class: "toolbar-button",
            "data-testid": "domain-policy-add",
            onclick: () => openEditor(null),
        }, "Add policy"),
    );

    const explainer = () => p(
        {class: "flex-none border-b border-gray-800 px-3 py-1.5 text-xs text-gray-500 slate:border-0 slate:pb-2 slate:pt-0 graphite:border-0 graphite:pt-2"},
        "Denies are checked first, then allows; a hostname no enabled allow covers is refused. ",
        mono("*.example.com"), " covers every name below ", mono("example.com"), ", not ", mono("example.com"), " itself.",
    );

    const openNotice = () => openToEverySpace(spaceDomainPoliciesS.val, spacesS.val) ? div(
        {class: "flex flex-none items-start gap-1.5 border-b border-amber-500/30 bg-amber-500/10 px-3 py-1.5 text-xs text-amber-200 slate:border-y slate:border-amber-500/20 slate:bg-amber-500/[0.07] graphite:border-y graphite:border-[#d8b36a]/25 graphite:bg-[#d8b36a]/[0.06] graphite:text-[#d8b36a]",
            "data-testid": "domain-policy-open-notice"},
        infoIcon({class: "mt-px h-3.5 w-3.5 flex-none"}),
        "Any space can publish any hostname that no deny covers. Add a deny for the hostnames that belong to one space.",
    ) : "";

    // --- list -----------------------------------------------------------------------

    const enableSwitch = (policy) => {
        const on = !policy.disabled;
        return button({
            type: "button",
            role: "switch",
            "aria-checked": String(on),
            "aria-label": `Policy ${policy.name} enabled`,
            title: on ? "Enabled" : "Disabled",
            "data-testid": `domain-policy-toggle-${policy.id}`,
            disabled: () => Boolean(busyId.val),
            class: () => "inline-flex h-7 items-center rounded px-1 " +
                (busyId.val === policy.id ? "cursor-wait opacity-60" : busyId.val ? "cursor-not-allowed" : "cursor-pointer"),
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
        return div(
            {class: `data-row grid ${ROW_COLS} items-center py-2 @3xl:py-1 classic:border-gray-800 classic:last:border-0 classic:hover:bg-transparent classic:@3xl:py-1.5`, "data-testid": `domain-policy-row-${policy.id}`},
            div({class: "flex min-w-0 items-center gap-2"},
                effectBadge(isDeny(policy.rule), off),
                span({class: `truncate ${off ? "text-gray-500" : "text-gray-200"}`, title: policy.name}, policy.name),
                off ? span({class: "flex-none rounded bg-gray-800 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-gray-500"}, "off") : ""),
            div({class: "flex items-center justify-end gap-1 @3xl:order-last @3xl:justify-start"},
                enableSwitch(policy),
                button({
                    type: "button",
                    title: `Edit policy ${policy.name}`,
                    "aria-label": `Edit policy ${policy.name}`,
                    "data-testid": `domain-policy-edit-${policy.id}`,
                    class: "inline-flex h-7 items-center justify-center rounded px-2 text-xs text-gray-400 hover:bg-surface-hover hover:text-gray-100 transition-colors cursor-pointer",
                    onclick: () => openEditor(policy),
                }, "Edit"),
                button({
                    type: "button",
                    title: `Delete policy ${policy.name}`,
                    "aria-label": `Delete policy ${policy.name}`,
                    "data-testid": `domain-policy-delete-${policy.id}`,
                    class: "inline-flex h-7 w-7 items-center justify-center rounded text-gray-400 hover:bg-surface-hover hover:text-red-400 transition-colors cursor-pointer",
                    onclick: () => { deleteError.val = null; deleteTarget.val = policy; },
                }, trashIcon())),
            summary("Spaces", describeSpaces(policy.rule?.selector?.spaces, names), tone),
            summary("Domains", describeDomains(policy.rule?.selector?.domains), `font-mono ${tone}`),
            () => rowError.val?.id === policy.id ? div(
                {class: "order-last col-span-full flex items-start gap-2 rounded border border-red-500/30 bg-red-500/10 px-2 py-1 text-xs text-red-300"},
                span({class: "min-w-0 flex-1 break-words", "data-testid": "domain-policy-error"}, rowError.val.message),
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
                return p({class: "px-4 py-3 text-sm text-gray-400", "data-testid": "domain-policy-empty"},
                    "No domain policies visible. Managing them needs the domain_policy permission.");
            }
            const names = spaceNames();
            return div({class: "pl-4 pr-2 text-[13px] slate:px-3 graphite:px-3"},
                div({class: `hidden @3xl:grid ${ROW_COLS} items-center border-b border-gray-800 py-1.5 slate:h-7 slate:border-line slate:py-0 graphite:h-6 graphite:border-0 graphite:py-0`},
                    headerCell("Policy"), headerCell("Spaces"), headerCell("Domains"), headerCell("Enabled", "pl-1")),
                ...list.map((policy) => policyRow(policy, names)));
        });

    // --- editor -----------------------------------------------------------------------

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
                if (policy) await capi.postV1SpaceDomainPoliciesUpdate(updatePayload(policy, draft()));
                else await capi.postV1SpaceDomainPoliciesCreate(createPayload(draft()));
                editor.val = null;
            } catch (e) {
                error.val = e.message;
            } finally {
                saving.val = false;
            }
        };

        const toggleSpace = (id) => {
            const ids = fields.spaceIds.val;
            fields.spaceIds.val = ids.includes(id) ? ids.filter((v) => v !== id) : [...ids, id];
        };

        // _system is offered only to a policy that already names it.
        const spaceChips = () => div({class: "flex flex-wrap items-center gap-1.5"},
            ...(spacesS.val || []).filter((s) => Number(s.id) !== 0 || fields.spaceIds.rawVal.includes(0)).map((s) => {
                const id = Number(s.id);
                const on = () => fields.spaceIds.val.includes(id);
                return button({
                    type: "button",
                    "aria-pressed": () => String(on()),
                    "data-testid": `domain-policy-space-${id}`,
                    class: () => "rounded border px-2 py-0.5 font-mono text-xs cursor-pointer transition-colors " +
                        (on() ? "border-brand bg-brand/15 text-gray-100" : "border-gray-600 text-gray-400 hover:bg-surface-hover"),
                    onclick: () => toggleSpace(id),
                }, s.name || `space ${id}`);
            }));

        const field = (heading, ...body) => div({class: "flex flex-col gap-1.5"}, fieldLabel(heading), ...body);
        const choices = (...buttons) => div({class: "flex flex-wrap items-center gap-2"}, ...buttons);

        const nameInput = input({
            class: "text-input text-sm",
            placeholder: "policy_name",
            maxLength: 64,
            spellcheck: false,
            "data-testid": "domain-policy-name",
            disabled: saving,
            value: fields.name,
            oninput: (e) => { fields.name.val = e.target.value; },
        });
        setTimeout(() => nameInput.focus());

        return div(
            {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"},
            div({
                class: "dialog flex max-h-[88vh] w-full max-w-xl flex-col overflow-hidden classic:rounded-[0.3rem] classic:border-gray-700 classic:bg-surface",
                role: "dialog",
                "aria-modal": "true",
                "aria-label": policy ? `Edit domain policy ${policy.name}` : "Add domain policy",
                "data-testid": "domain-policy-editor",
                onkeydown: (e) => { if (e.key === "Escape") close(); },
            },
                div({class: "flex-none border-b border-gray-800 px-4 py-3 slate:border-line graphite:border-line"},
                    h2({class: "truncate text-base font-semibold"}, policy ? `Edit domain policy ${policy.name}` : "Add domain policy")),
                div({class: "app-scroll flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 py-4"},
                    field("Name", nameInput),
                    field("Effect", choices(
                        choiceButton(fields.deny, false, "Allow", "domain-policy-effect-allow", "border-green-500/50 bg-green-500/10 text-green-300"),
                        choiceButton(fields.deny, true, "Deny", "domain-policy-effect-deny", "border-red-500/50 bg-red-500/10 text-red-300"))),
                    field("Spaces",
                        choices(
                            choiceButton(fields.spacesMode, MODE_EXACT, "These spaces", "domain-policy-spaces-mode-exact"),
                            choiceButton(fields.spacesMode, MODE_EXCEPT, "All spaces except", "domain-policy-spaces-mode-except")),
                        spaceChips),
                    field("Domains",
                        choices(
                            choiceButton(fields.domainsMode, MODE_EXACT, "These domains", "domain-policy-domains-mode-exact"),
                            choiceButton(fields.domainsMode, MODE_EXCEPT, "All domains except", "domain-policy-domains-mode-except")),
                        textarea({
                            class: "text-input min-h-24 font-mono text-xs",
                            rows: 4,
                            spellcheck: false,
                            placeholder: "api.example.com\n*.example.com",
                            "aria-label": "Domains",
                            "data-testid": "domain-policy-domains",
                            disabled: saving,
                            value: fields.domainsText,
                            oninput: (e) => { fields.domainsText.val = e.target.value; },
                        }),
                        span({class: "text-[11px] text-gray-500"}, "One per line or comma separated.")),
                    label({class: "flex cursor-pointer select-none items-center gap-2 self-start text-xs text-gray-300"},
                        input({
                            type: "checkbox",
                            class: "cursor-pointer accent-blue-500",
                            "data-testid": "domain-policy-enabled",
                            checked: fields.enabled,
                            onchange: (e) => { fields.enabled.val = e.target.checked; },
                        }),
                        "Enabled"),
                    field("Reads as", div(
                        {class: "break-words rounded-md border border-gray-800 bg-gray-950/40 px-3 py-2 text-xs text-gray-300 slate:border-line slate:bg-field graphite:border-line", "data-testid": "domain-policy-preview"},
                        () => ruleSentence(draftRule(draft()), spaceNames())))),
                div({class: "flex flex-none flex-col gap-2 border-t border-gray-800 px-4 py-3 slate:border-line graphite:border-line"},
                    () => error.val ? p({class: "break-words text-sm text-red-400", "data-testid": "domain-policy-error"}, error.val) : "",
                    div({class: "flex items-center justify-end gap-2"},
                        span({class: "min-w-0 flex-1 break-words text-xs text-gray-500", "data-testid": "domain-policy-problem"},
                            () => draftProblems(draft())[0] || ""),
                        smallBtn("Cancel", close, () => saving.val, "domain-policy-cancel"),
                        actionButton(policy ? "Save" : "Create", save, "bg-brand text-white hover:bg-blue-600",
                            () => saving.val || draftProblems(draft()).length > 0, "domain-policy-save")))),
        );
    };

    // --- delete -----------------------------------------------------------------------

    const confirmDelete = async () => {
        const target = deleteTarget.val;
        if (!target || deleteSaving.val) return;
        deleteSaving.val = true;
        deleteError.val = null;
        try {
            await capi.postV1SpaceDomainPoliciesDelete({id: target.id});
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
                {class: "card w-full max-w-md flex flex-col gap-4 shadow-2xl", "data-testid": "domain-policy-delete-dialog"},
                h2({class: "text-base font-semibold"}, "Confirm delete"),
                p({class: "break-words text-sm text-gray-300"},
                    `Delete domain policy ${target.name}? It says: ${ruleSentence(target.rule, spaceNames())}.`),
                () => deleteError.val ? p({class: "break-words text-sm text-red-400", "data-testid": "domain-policy-error"}, deleteError.val) : "",
                div({class: "flex items-center justify-end gap-2"},
                    smallBtn("Cancel", () => { if (!deleteSaving.val) deleteTarget.val = null; }, () => deleteSaving.val),
                    actionButton("Confirm", confirmDelete, "bg-red-600 text-white hover:bg-red-500",
                        () => deleteSaving.val, "domain-policy-delete-confirm"),
                ),
            ),
        );
    };

    return div(
        {class: "h-full min-h-0 flex flex-col overflow-hidden"},
        toolbar(),
        explainer(),
        openNotice,
        listArea(),
        () => editor.val ? editorOverlay(editor.val) : "",
        deleteOverlay,
    );
}
