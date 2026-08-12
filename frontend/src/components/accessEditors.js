import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {spinnerButton} from "./spinnerbutton.js";
import {checkIcon, chevronDownIcon, closeIcon, plusIcon} from "../lib/icons.js";
import {
    ENTITY_TYPES,
    POSITIONS,
    VERBS,
    formatGlobalRule,
    formatRule,
    positionValueName,
    templateArguments,
} from "../lib/authz.js";

const {div, span, p, h2, input, button, select, option} = van.tags;

// Template arguments are shared across all rules of a template, so the UI
// assigns one fixed argument per position kind: choosing "Argument" for the
// spaces position of any rule references the same ${spaces} argument.
const TEMPLATE_ARGUMENTS = {
    permissions: {id: 1, name: "permissions"},
    spaces: {id: 2, name: "spaces"},
    entityTypes: {id: 3, name: "entity_types"},
    entityRefs: {id: 4, name: "entity_refs"},
};

const ARG_NAMES = new Map(Object.values(TEMPLATE_ARGUMENTS).map((a) => [a.id, a.name]));

const parseRefs = (text) => (text || "")
    .split(/[\s,]+/)
    .filter(Boolean)
    .map(Number)
    .filter((n) => Number.isInteger(n) && n > 0);

const positionOptions = (kind, spaces) => {
    if (kind === "permissions") return VERBS.map((v) => ({id: v.id, label: v.name}));
    if (kind === "entityTypes") return ENTITY_TYPES.map((e) => ({id: e.id, label: e.name}));
    if (kind === "spaces") {
        return (spaces || [])
            .filter((s) => s && !s.deleted)
            .map((s) => ({id: Number(s.id), label: s.name || `space ${s.id}`}));
    }
    return [];
};

// mode: "any" (wildcard), "arg" (template argument), or "list" (specific
// values; entity refs keep free text since they are raw ids).
const newPositionState = (kind, sel) => {
    const st = {kind, mode: van.state("any"), values: van.state([]), refsText: van.state("")};
    if (sel) {
        if (sel.argumentId) {
            st.mode.val = "arg";
        } else if (!sel.wildcard) {
            st.mode.val = "list";
            if (kind === "entityRefs") st.refsText.val = (sel.include || []).join(", ");
            else st.values.val = (sel.include || []).map(Number);
        }
    }
    return st;
};

const selectorFromState = (st) => {
    if (st.mode.val === "any") return {wildcard: true, argumentId: 0, include: [], exclude: []};
    if (st.mode.val === "arg") return {wildcard: false, argumentId: TEMPLATE_ARGUMENTS[st.kind].id, include: [], exclude: []};
    const include = st.kind === "entityRefs" ? parseRefs(st.refsText.val) : [...st.values.val];
    return {wildcard: false, argumentId: 0, include, exclude: []};
};

export const newRuleState = (rule) => ({
    positions: Object.fromEntries(POSITIONS.map(({key}) => [key, newPositionState(key, rule?.[key])])),
    delegation: van.state(!!rule?.delegationAllowed),
});

const ruleFromState = (rs) => ({
    permissions: selectorFromState(rs.positions.permissions),
    spaces: selectorFromState(rs.positions.spaces),
    entityTypes: selectorFromState(rs.positions.entityTypes),
    entityRefs: selectorFromState(rs.positions.entityRefs),
    delegationAllowed: rs.delegation.val,
});

const positionFace = (st, spaceNames) => {
    if (st.mode.val === "any") return "Any (*)";
    if (st.mode.val === "arg") return "${" + TEMPLATE_ARGUMENTS[st.kind].name + "}";
    if (st.kind === "entityRefs") return st.refsText.val.trim() || "ids…";
    const values = st.values.val;
    if (!values.length) return "None";
    return values.map((v) => positionValueName(st.kind, v, spaceNames)).join(", ");
};

const menuShell = (...children) => div({
    class: "absolute top-full left-0 z-[60] mt-1.5 min-w-44 rounded-md border border-gray-600 bg-surface p-1 shadow-2xl flex flex-col",
    onclick: (e) => e.stopPropagation(),
}, ...children);

const menuRow = (onclick, ...children) => button({
    type: "button",
    class: "flex items-center gap-2 rounded px-2 py-1.5 text-left text-xs text-gray-200 hover:bg-surface-hover cursor-pointer",
    onclick,
}, ...children);

const menuCheck = (on) => checkIcon({class: `w-3.5 h-3.5 flex-none text-brand ${on ? "" : "invisible"}`});
const menuDivider = () => div({class: "my-1 border-t border-gray-700"});

const positionEditor = ({st, label, allowArgument, spaces, spaceNames, openMenu, menuKey}) => {
    const options = () => positionOptions(st.kind, spaces());
    const toggleValue = (id) => {
        const next = new Set(st.values.val);
        next.has(id) ? next.delete(id) : next.add(id);
        st.values.val = [...next];
        st.mode.val = next.size ? "list" : "any";
    };
    const menu = () => {
        if (openMenu.val !== menuKey) return "";
        return menuShell(
            menuRow(() => {
                st.mode.val = "any";
                st.values.val = [];
                st.refsText.val = "";
            }, menuCheck(st.mode.val === "any"), "Any (*)"),
            ...(st.kind === "entityRefs" ? [
                menuDivider(),
                div({class: "px-2 py-1"},
                    input({
                        class: "text-input w-40 text-xs",
                        placeholder: "ids, e.g. 4, 7",
                        value: st.refsText,
                        oninput: (e) => {
                            st.refsText.val = e.target.value;
                            st.mode.val = e.target.value.trim() ? "list" : "any";
                        },
                    })),
            ] : options().length ? [
                menuDivider(),
                ...options().map((o) => menuRow(() => toggleValue(o.id),
                    menuCheck(st.mode.val === "list" && st.values.val.includes(o.id)),
                    span({class: "font-mono"}, o.label))),
            ] : []),
            ...(allowArgument ? [
                menuDivider(),
                menuRow(() => {
                    st.mode.val = "arg";
                    st.values.val = [];
                    st.refsText.val = "";
                }, menuCheck(st.mode.val === "arg"),
                span({class: "text-amber-300"}, "Argument "),
                span({class: "font-mono text-amber-300 text-[11px]"}, "${" + TEMPLATE_ARGUMENTS[st.kind].name + "}")),
            ] : []),
        );
    };
    return div({class: "flex flex-col gap-1"},
        span({class: "text-[10px] uppercase tracking-wider text-gray-500"}, label),
        span({class: "relative inline-flex"},
            button({
                type: "button",
                "aria-haspopup": "true",
                "aria-expanded": () => String(openMenu.val === menuKey),
                "aria-label": label,
                class: () => `inline-flex items-center gap-1.5 rounded px-2 py-1.5 text-xs cursor-pointer border transition-colors max-w-56 ` +
                    (st.mode.val !== "any" ? "text-gray-100 border-brand" : "text-gray-300 border-gray-600 hover:bg-surface-hover"),
                onclick: (e) => {
                    e.stopPropagation();
                    openMenu.val = openMenu.val === menuKey ? null : menuKey;
                },
            },
            () => span({class: `truncate ${st.mode.val === "arg" ? "font-mono text-amber-300" : ""}`}, positionFace(st, spaceNames())),
            chevronDownIcon({class: "w-3 h-3 flex-none"})),
            menu),
    );
};

const delegationToggle = (rs) => button({
    type: "button",
    class: () => `mt-5 inline-flex items-center gap-1.5 rounded px-2 py-1.5 text-xs cursor-pointer border transition-colors ` +
        (rs.delegation.val ? "text-teal-300 border-teal-700" : "text-gray-400 border-gray-600 hover:bg-surface-hover"),
    onclick: () => { rs.delegation.val = !rs.delegation.val; },
}, () => rs.delegation.val ? "agents ✓" : "agents ✗");

const ruleEditorRow = ({rs, index, allowArgument, spaces, spaceNames, openMenu, onRemove}) => div(
    {class: "flex flex-wrap items-start gap-2 rounded-md border border-gray-700/60 bg-gray-950/30 p-2"},
    ...POSITIONS.flatMap(({key, label}, i) => [
        ...(i ? [span({class: "mt-6 font-mono text-gray-500"}, ":")] : []),
        positionEditor({st: rs.positions[key], label, allowArgument, spaces, spaceNames, openMenu, menuKey: `rule${index}:${key}`}),
    ]),
    span({class: "mt-6 font-mono text-gray-500"}, ":"),
    delegationToggle(rs),
    div({class: "flex-1"}),
    onRemove ? button({
        type: "button",
        title: "Remove rule",
        "aria-label": `Remove rule ${index + 1}`,
        class: "mt-5 inline-flex h-7 w-7 items-center justify-center rounded text-gray-500 hover:bg-surface hover:text-red-400 cursor-pointer",
        onclick: onRemove,
    }, closeIcon({class: "w-3.5 h-3.5"})) : "",
);

const overlayShell = (title, ...children) => div(
    {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"},
    div({class: "card w-full max-w-4xl max-h-full overflow-y-auto flex flex-col gap-4 shadow-2xl"},
        h2({class: "text-base font-semibold"}, title),
        ...children),
);

const overlayActions = ({error, saveLabel, onSave, onClose, saving, disabled}) => [
    () => error.val ? p({class: "text-sm text-red-400"}, error.val) : "",
    div({class: "flex items-center justify-end gap-2"},
        button({
            type: "button",
            class: "text-xs px-3 py-1 rounded-md font-medium bg-gray-700 text-gray-200 hover:bg-gray-600 cursor-pointer",
            onclick: () => { if (!saving.val) onClose(); },
        }, "Cancel"),
        spinnerButton(saveLabel, onSave,
            "text-xs px-3 py-1 rounded-md font-medium bg-brand text-white hover:bg-blue-600",
            "button", () => saving.val || (disabled ? disabled() : false)),
    ),
];

const menuBackdrop = (openMenu) => () => openMenu.val
    ? div({class: "fixed inset-0 z-[55]", onclick: () => { openMenu.val = null; }})
    : "";

const fieldLabel = (text) => span({class: "text-[10px] uppercase tracking-wider text-gray-500"}, text);

// ruleTemplateOverlay creates a template, or edits `record` when given.
export function ruleTemplateOverlay({record, spaces, spaceNames, onClose}) {
    const editing = Boolean(record);
    const name = van.state(record?.name || "");
    const ruleStates = van.state((record?.template?.rules || [null]).map(newRuleState));
    const error = van.state(null);
    const saving = van.state(false);
    const openMenu = van.state(null);

    const buildTemplate = () => {
        const rules = ruleStates.val.map(ruleFromState);
        const usedKinds = new Set();
        for (const rule of rules) {
            for (const {key} of POSITIONS) {
                if (rule[key].argumentId) usedKinds.add(key);
            }
        }
        return {
            arguments: [...usedKinds].map((kind) => ({...TEMPLATE_ARGUMENTS[kind]})),
            rules,
        };
    };

    const save = async () => {
        if (saving.val) return;
        try {
            saving.val = true;
            error.val = null;
            const payload = {name: name.val.trim(), template: buildTemplate()};
            if (editing) {
                await capi.postV1AccessRuleTemplatesUpdate({id: record.id, ...payload});
            } else {
                await capi.postV1AccessRuleTemplatesCreate(payload);
            }
            onClose();
        } catch (e) {
            error.val = e.message;
        } finally {
            saving.val = false;
        }
    };

    return overlayShell(editing ? `Edit template ${record.name}` : "New template",
        div({class: "flex items-center gap-3"},
            fieldLabel("Name"),
            input({
                class: "text-input max-w-xs",
                placeholder: "template_name",
                value: name,
                oninput: (e) => { name.val = e.target.value; },
            })),
        () => div({class: "flex flex-col gap-2"},
            ...ruleStates.val.map((rs, i) => ruleEditorRow({
                rs,
                index: i,
                allowArgument: true,
                spaces,
                spaceNames,
                openMenu,
                onRemove: ruleStates.val.length > 1
                    ? () => { ruleStates.val = ruleStates.val.filter((r) => r !== rs); }
                    : null,
            }))),
        div({class: "flex items-center gap-2"},
            button({
                type: "button",
                class: "inline-flex items-center gap-1 text-xs px-2 py-1 rounded-md border border-gray-600 text-gray-300 hover:bg-surface-hover cursor-pointer",
                onclick: () => { ruleStates.val = [...ruleStates.val, newRuleState(null)]; },
            }, plusIcon({class: "w-3 h-3"}), "Add rule")),
        div({class: "flex flex-col gap-1 rounded-md border border-gray-800 bg-gray-950/40 px-3 py-2"},
            fieldLabel("Rules"),
            () => div({class: "flex flex-col gap-0.5 font-mono text-[11px] text-gray-300"},
                ...ruleStates.val.map((rs) => span(formatRule(ruleFromState(rs), {spaceNames: spaceNames(), argNames: ARG_NAMES}))))),
        ...overlayActions({
            error,
            saveLabel: editing ? "Save template" : "Create template",
            onSave: save,
            onClose,
            saving,
            disabled: () => !name.val.trim(),
        }),
        menuBackdrop(openMenu),
    );
}

const bindingValueChips = (kind, valuesState, spaces, spaceNames) => {
    const options = () => positionOptions(kind, spaces());
    return div({class: "flex flex-wrap items-center gap-1.5"},
        ...(kind === "entityRefs" ? [] : [() => div({class: "flex flex-wrap items-center gap-1.5"},
            ...options().map((o) => button({
                type: "button",
                class: () => `rounded px-2 py-0.5 text-xs border cursor-pointer transition-colors font-mono ` +
                    (valuesState.values.val.includes(o.id)
                        ? "border-brand text-gray-100 bg-brand/15"
                        : "border-gray-600 text-gray-400 hover:bg-surface-hover"),
                onclick: () => {
                    const next = new Set(valuesState.values.val);
                    next.has(o.id) ? next.delete(o.id) : next.add(o.id);
                    valuesState.values.val = [...next];
                },
            }, o.label)))]),
        ...(kind === "entityRefs" ? [input({
            class: "text-input max-w-48 text-xs",
            placeholder: "ids, e.g. 4, 7",
            value: valuesState.refsText,
            oninput: (e) => { valuesState.refsText.val = e.target.value; },
        })] : []),
    );
};

// grantOverlay assigns a template (with argument bindings) or a direct rule
// to one user.
export function grantOverlay({user, templates, spaces, spaceNames, onClose}) {
    const usable = () => (templates() || []).filter((t) => t && !t.deleted);
    const mode = van.state("template");
    const templateId = van.state(usable()[0]?.id || 0);
    const bindingStates = new Map();
    const directRule = newRuleState(null);
    const error = van.state(null);
    const saving = van.state(false);
    const openMenu = van.state(null);

    const selectedTemplate = () => usable().find((t) => Number(t.id) === Number(templateId.val)) || null;

    const bindingState = (arg) => {
        const key = `${templateId.val}:${arg.id}`;
        if (!bindingStates.has(key)) {
            bindingStates.set(key, {values: van.state([]), refsText: van.state("")});
        }
        return bindingStates.get(key);
    };

    const save = async () => {
        if (saving.val) return;
        try {
            saving.val = true;
            error.val = null;
            const request = {userId: Number(user.id), templateId: 0, grant: {args: [], rule: null}};
            if (mode.val === "template") {
                const template = selectedTemplate();
                if (!template) throw new Error("Choose a template");
                request.templateId = Number(template.id);
                request.grant.args = templateArguments(template.template).map((arg) => {
                    const st = bindingState(arg);
                    const values = arg.kind === "entityRefs" ? parseRefs(st.refsText.val) : [...st.values.val];
                    return {argumentId: arg.id, values};
                });
            } else {
                request.grant.rule = ruleFromState(directRule);
            }
            await capi.postV1AccessGrantsCreate(request);
            onClose();
        } catch (e) {
            error.val = e.message;
        } finally {
            saving.val = false;
        }
    };

    const modeButton = (value, text) => button({
        type: "button",
        class: () => `rounded px-2.5 py-1 text-xs border cursor-pointer transition-colors ` +
            (mode.val === value ? "border-brand text-gray-100 bg-brand/15" : "border-gray-600 text-gray-400 hover:bg-surface-hover"),
        onclick: () => { mode.val = value; },
    }, text);

    return overlayShell(`Grant access to ${user.name || `user ${user.id}`}`,
        div({class: "flex items-center gap-2"}, modeButton("template", "From template"), modeButton("direct", "Direct rule")),
        () => mode.val === "template"
            ? div({class: "flex flex-col gap-3"},
                div({class: "flex items-center gap-3"},
                    fieldLabel("Template"),
                    select({
                        class: "input min-w-56",
                        onchange: (e) => { templateId.val = Number(e.target.value); },
                    }, ...usable().map((t) => option({value: t.id, selected: Number(t.id) === Number(templateId.val)}, t.name)))),
                () => {
                    const template = selectedTemplate();
                    if (!template) return p({class: "text-sm text-gray-400"}, "No templates available.");
                    const args = templateArguments(template.template);
                    if (!args.length) return p({class: "text-sm text-gray-400"}, "This template takes no arguments.");
                    return div({class: "flex flex-col gap-2"},
                        ...args.map((arg) => div({class: "flex flex-col gap-1"},
                            fieldLabel("${" + arg.name + "}"),
                            bindingValueChips(arg.kind, bindingState(arg), spaces, spaceNames))));
                })
            : div({class: "flex flex-col gap-2"},
                ruleEditorRow({rs: directRule, index: 0, allowArgument: false, spaces, spaceNames, openMenu, onRemove: null}),
                div({class: "flex flex-col gap-1 rounded-md border border-gray-800 bg-gray-950/40 px-3 py-2"},
                    fieldLabel("Rule"),
                    () => span({class: "font-mono text-[11px] text-gray-300"},
                        formatRule(ruleFromState(directRule), {spaceNames: spaceNames()})))),
        ...overlayActions({error, saveLabel: "Grant", onSave: save, onClose, saving}),
        menuBackdrop(openMenu),
    );
}

// globalRuleOverlay creates a deny rule evaluated before every grant.
export function globalRuleOverlay({spaces, spaceNames, onClose}) {
    const name = van.state("");
    const rs = newRuleState(null);
    const delegatedOnly = van.state(false);
    const error = van.state(null);
    const saving = van.state(false);
    const openMenu = van.state(null);

    const buildRule = () => {
        const rule = ruleFromState(rs);
        return {
            permissions: rule.permissions,
            spaces: rule.spaces,
            entityTypes: rule.entityTypes,
            entityRefs: rule.entityRefs,
            delegatedOnly: delegatedOnly.val,
        };
    };

    const save = async () => {
        if (saving.val) return;
        try {
            saving.val = true;
            error.val = null;
            await capi.postV1AccessGlobalRulesCreate({name: name.val.trim(), rule: buildRule()});
            onClose();
        } catch (e) {
            error.val = e.message;
        } finally {
            saving.val = false;
        }
    };

    return overlayShell("New global rule",
        p({class: "text-sm text-gray-400"},
            "Global rules deny matching requests before any user grant is considered."),
        div({class: "flex items-center gap-3"},
            fieldLabel("Name"),
            input({
                class: "text-input max-w-xs",
                placeholder: "rule_name",
                value: name,
                oninput: (e) => { name.val = e.target.value; },
            })),
        div({class: "flex flex-wrap items-start gap-2 rounded-md border border-gray-700/60 bg-gray-950/30 p-2"},
            ...POSITIONS.flatMap(({key, label}, i) => [
                ...(i ? [span({class: "mt-6 font-mono text-gray-500"}, ":")] : []),
                positionEditor({st: rs.positions[key], label, allowArgument: false, spaces, spaceNames, openMenu, menuKey: `global:${key}`}),
            ]),
            div({class: "flex-1"}),
            button({
                type: "button",
                title: "Only deny delegated agent sessions",
                class: () => `mt-5 inline-flex items-center gap-1.5 rounded px-2 py-1.5 text-xs cursor-pointer border transition-colors ` +
                    (delegatedOnly.val ? "text-teal-300 border-teal-700" : "text-gray-400 border-gray-600 hover:bg-surface-hover"),
                onclick: () => { delegatedOnly.val = !delegatedOnly.val; },
            }, () => delegatedOnly.val ? "agents only ✓" : "agents only ✗")),
        div({class: "flex flex-col gap-1 rounded-md border border-gray-800 bg-gray-950/40 px-3 py-2"},
            fieldLabel("Denies"),
            () => span({class: "font-mono text-[11px] text-gray-300"},
                formatGlobalRule(buildRule(), {spaceNames: spaceNames()}))),
        ...overlayActions({
            error,
            saveLabel: "Create rule",
            onSave: save,
            onClose,
            saving,
            disabled: () => !name.val.trim(),
        }),
        menuBackdrop(openMenu),
    );
}
