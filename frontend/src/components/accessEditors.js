import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {spinnerButton} from "./spinnerbutton.js";
import {globalRuleDisplay, ruleDisplay} from "./ruleDisplay.js";
import {checkIcon, chevronDownIcon, closeIcon, plusIcon} from "../lib/icons.js";
import {
    ENTITY_TYPES,
    POSITIONS,
    VERBS,
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

// Room a menu wants below its trigger before it drops downward — a little more
// than the tallest menu (every entity type, plus "Any" and "Argument"). With
// less than this below and more above, it flips up, so a full list of options
// is reachable without scrolling anything.
const MENU_DROP_SPACE = 330;

// The menus are fixed rather than absolutely positioned: the dialog body
// scrolls, and an absolute menu inside it is clipped by that scroll container,
// which is what forced scrolling to reach the lower options.
const menuShell = (trigger, ...children) => {
    const el = div({
        class: "app-scroll fixed z-[60] flex w-max min-w-48 max-w-80 flex-col overflow-y-auto " +
            "rounded-md border border-gray-600 bg-surface p-1 shadow-2xl",
        onclick: (e) => e.stopPropagation(),
    }, ...children);
    const rect = trigger.getBoundingClientRect();
    const below = window.innerHeight - rect.bottom - 12;
    const above = rect.top - 12;
    if (below < MENU_DROP_SPACE && above > below) {
        el.style.bottom = `${window.innerHeight - rect.top + 6}px`;
        el.style.maxHeight = `${above}px`;
    } else {
        el.style.top = `${rect.bottom + 6}px`;
        el.style.maxHeight = `${below}px`;
    }
    el.style.left = `${Math.max(8, Math.min(rect.left, window.innerWidth - 320))}px`;
    return el;
};

const menuRow = (onclick, ...children) => button({
    type: "button",
    class: "flex items-center gap-2 rounded px-2 py-1.5 text-left text-xs text-gray-200 hover:bg-surface-hover cursor-pointer",
    onclick,
}, ...children);

const menuCheck = (on) => checkIcon({class: `w-3.5 h-3.5 flex-none text-brand ${on ? "" : "invisible"}`});
const menuDivider = () => div({class: "my-1 border-t border-gray-700"});

const fieldLabel = (text) => span({class: "text-[10px] uppercase tracking-wider text-gray-500"}, text);

const positionEditor = ({st, label, allowArgument, spaces, spaceNames, openMenu, menuKey}) => {
    const options = () => positionOptions(st.kind, spaces());
    const toggleValue = (id) => {
        const next = new Set(st.values.val);
        next.has(id) ? next.delete(id) : next.add(id);
        st.values.val = [...next];
        st.mode.val = next.size ? "list" : "any";
    };
    const trigger = button({
        type: "button",
        "aria-haspopup": "true",
        "aria-expanded": () => String(openMenu.val === menuKey),
        "aria-label": label,
        class: () => `flex w-full items-center justify-between gap-1.5 rounded px-2 py-1.5 text-xs cursor-pointer border transition-colors ` +
            (st.mode.val !== "any" ? "text-gray-100 border-brand" : "text-gray-300 border-gray-600 hover:bg-surface-hover"),
        onclick: (e) => {
            e.stopPropagation();
            openMenu.val = openMenu.val === menuKey ? null : menuKey;
        },
    },
    () => span({class: `truncate ${st.mode.val === "arg" ? "font-mono text-amber-300" : ""}`}, positionFace(st, spaceNames())),
    chevronDownIcon({class: "w-3 h-3 flex-none"}));

    const menu = () => {
        if (openMenu.val !== menuKey) return "";
        return menuShell(trigger,
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
    return div({class: "flex min-w-40 flex-1 flex-col gap-1"}, fieldLabel(label), trigger, menu);
};

// A labelled on/off control sitting in the same row as the position editors:
// grant rules use it for delegability, global rules for who the deny hits.
const toggleEditor = ({label, state, onText, offText, title}) => div({class: "flex w-36 flex-none flex-col gap-1"},
    fieldLabel(label),
    button({
        type: "button",
        title,
        class: () => `flex w-full items-center justify-center rounded px-2 py-1.5 text-xs cursor-pointer border transition-colors ` +
            (state.val ? "text-teal-300 border-teal-700" : "text-gray-400 border-gray-600 hover:bg-surface-hover"),
        onclick: () => { state.val = !state.val; },
    }, () => state.val ? onText : offText));

// One rule editor: the four selector positions, its toggle, and a live reading
// of the rule underneath in the same chip form the tables use, so what the
// dialog shows while editing matches what the page shows afterwards.
const ruleEditorCard = ({heading, positions, toggle, preview, onRemove, removeLabel}) => div(
    {class: "flex flex-col gap-3 rounded-md border border-gray-700/60 bg-gray-950/30 p-3"},
    div({class: "flex items-center gap-2"},
        span({class: "text-[10px] font-semibold uppercase tracking-wider text-gray-500"}, heading),
        div({class: "flex-1"}),
        onRemove ? button({
            type: "button",
            title: "Remove rule",
            "aria-label": removeLabel,
            class: "inline-flex h-6 w-6 items-center justify-center rounded text-gray-500 hover:bg-surface hover:text-red-400 cursor-pointer",
            onclick: onRemove,
        }, closeIcon({class: "w-3.5 h-3.5"})) : ""),
    div({class: "flex flex-wrap items-end gap-2"}, ...positions, toggle),
    div({class: "border-t border-gray-800 pt-2"}, preview),
);

const ruleEditorRow = ({rs, index, heading, menuPrefix, allowArgument, spaces, spaceNames, openMenu, onRemove}) => ruleEditorCard({
    heading: heading || `Rule ${index + 1}`,
    positions: POSITIONS.map(({key, label}) => positionEditor({
        st: rs.positions[key],
        label,
        allowArgument,
        spaces,
        spaceNames,
        openMenu,
        menuKey: `${menuPrefix || "rule"}${index}:${key}`,
    })),
    toggle: toggleEditor({
        label: "Agents",
        state: rs.delegation,
        onText: "agents ✓",
        offText: "agents ✗",
        title: "Whether an agent session inherits this rule",
    }),
    preview: () => ruleDisplay(ruleFromState(rs), {spaceNames: spaceNames(), argNames: ARG_NAMES}),
    onRemove,
    removeLabel: `Remove rule ${index + 1}`,
});

// The dialog keeps a fixed frame — header, scrolling body, footer — and a floor
// under its height so the rule editors always have room to breathe and their
// menus have somewhere to drop.
const overlayShell = ({title, subtitle, body, actions, backdrop}) => div(
    {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"},
    div({class: "flex w-full max-w-5xl max-h-[88vh] min-h-[min(88vh,34rem)] flex-col overflow-hidden " +
            "rounded-[0.3rem] border border-gray-700 bg-surface shadow-2xl"},
        div({class: "flex-none border-b border-gray-800 px-4 py-3"},
            h2({class: "text-base font-semibold"}, title),
            subtitle ? p({class: "mt-1 text-sm text-gray-400"}, subtitle) : ""),
        div({class: "app-scroll flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 py-4"}, ...body),
        div({class: "flex flex-none flex-col gap-2 border-t border-gray-800 px-4 py-3"}, ...actions),
        backdrop || ""),
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

const nameField = (name, placeholder) => div({class: "flex items-center gap-3"},
    fieldLabel("Name"),
    input({
        class: "text-input max-w-xs",
        placeholder,
        value: name,
        oninput: (e) => { name.val = e.target.value; },
    }));

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

    return overlayShell({
        title: editing ? `Edit role ${record.name}` : "New role",
        subtitle: "A role is a named set of rules. Positions set to an argument are filled in when the role is granted.",
        body: [
            nameField(name, "role_name"),
            () => div({class: "flex flex-col gap-3"},
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
        ],
        actions: overlayActions({
            error,
            saveLabel: editing ? "Save role" : "Create role",
            onSave: save,
            onClose,
            saving,
            disabled: () => !name.val.trim(),
        }),
        backdrop: menuBackdrop(openMenu),
    });
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

const previewPanel = (label, ...children) => div(
    {class: "flex flex-col gap-1.5 rounded-md border border-gray-800 bg-gray-950/40 px-3 py-2"},
    fieldLabel(label),
    ...children,
);

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
                if (!template) throw new Error("Choose a role");
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

    // The rules of the chosen role, so a grant is never made blind: bound
    // arguments still read as ${name} here since the binding applies per user.
    const templateRules = (template) => {
        const argNames = new Map(templateArguments(template.template).map((a) => [a.id, a.name]));
        const rules = template.template?.rules || [];
        if (!rules.length) return "";
        return previewPanel("Rules",
            ...rules.map((rule) => ruleDisplay(rule, {spaceNames: spaceNames(), argNames})));
    };

    return overlayShell({
        title: `Grant access to ${user.name || `user ${user.id}`}`,
        body: [
            div({class: "flex items-center gap-2"}, modeButton("template", "From role"), modeButton("direct", "Direct rule")),
            () => mode.val === "template"
                ? div({class: "flex flex-col gap-3"},
                    div({class: "flex items-center gap-3"},
                        fieldLabel("Role"),
                        select({
                            class: "input min-w-56",
                            onchange: (e) => { templateId.val = Number(e.target.value); },
                        }, ...usable().map((t) => option({value: t.id, selected: Number(t.id) === Number(templateId.val)}, t.name)))),
                    () => {
                        const template = selectedTemplate();
                        if (!template) return p({class: "text-sm text-gray-400"}, "No roles available.");
                        const args = templateArguments(template.template);
                        return div({class: "flex flex-col gap-3"},
                            args.length
                                ? div({class: "flex flex-col gap-2"},
                                    ...args.map((arg) => div({class: "flex flex-col gap-1"},
                                        fieldLabel("${" + arg.name + "}"),
                                        bindingValueChips(arg.kind, bindingState(arg), spaces, spaceNames))))
                                : p({class: "text-sm text-gray-400"}, "This role takes no arguments."),
                            templateRules(template));
                    })
                : ruleEditorRow({
                    rs: directRule,
                    index: 0,
                    heading: "Rule",
                    menuPrefix: "direct",
                    allowArgument: false,
                    spaces,
                    spaceNames,
                    openMenu,
                    onRemove: null,
                }),
        ],
        actions: overlayActions({error, saveLabel: "Grant", onSave: save, onClose, saving}),
        backdrop: menuBackdrop(openMenu),
    });
}

// globalRuleOverlay creates a global rule: a deny evaluated before every
// grant, or an allow evaluated alongside grants as if every user held it.
export function globalRuleOverlay({spaces, spaceNames, onClose}) {
    const name = van.state("");
    const rs = newRuleState(null);
    const mode = van.state("deny");
    const delegatedOnly = van.state(false);
    const error = van.state(null);
    const saving = van.state(false);
    const openMenu = van.state(null);

    const buildRule = () => {
        const rule = ruleFromState(rs);
        const deny = mode.val === "deny";
        return {
            permissions: rule.permissions,
            spaces: rule.spaces,
            entityTypes: rule.entityTypes,
            entityRefs: rule.entityRefs,
            deny,
            delegatedOnly: deny && delegatedOnly.val,
            delegationAllowed: !deny && rs.delegation.val,
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

    const modeButton = (value, text) => button({
        type: "button",
        class: () => `rounded px-2.5 py-1 text-xs border cursor-pointer transition-colors ` +
            (mode.val === value ? "border-brand text-gray-100 bg-brand/15" : "border-gray-600 text-gray-400 hover:bg-surface-hover"),
        onclick: () => { mode.val = value; },
    }, text);

    return overlayShell({
        title: "New global rule",
        subtitle: "A deny rule blocks matching requests before any user grant is considered; " +
            "an allow rule grants matching requests to every user, though denies still beat it.",
        body: [
            nameField(name, "rule_name"),
            div({class: "flex items-center gap-2"}, modeButton("deny", "Deny"), modeButton("allow", "Allow")),
            () => ruleEditorCard({
                heading: mode.val === "allow" ? "Allows" : "Denies",
                positions: POSITIONS.map(({key, label}) => positionEditor({
                    st: rs.positions[key],
                    label,
                    allowArgument: false,
                    spaces,
                    spaceNames,
                    openMenu,
                    menuKey: `global:${key}`,
                })),
                toggle: mode.val === "allow"
                    ? toggleEditor({
                        label: "Agents",
                        state: rs.delegation,
                        onText: "agents ✓",
                        offText: "agents ✗",
                        title: "Whether delegated agent sessions also receive this rule",
                    })
                    : toggleEditor({
                        label: "Applies to",
                        state: delegatedOnly,
                        onText: "agents only ✓",
                        offText: "agents only ✗",
                        title: "Only deny delegated agent sessions",
                    }),
                preview: () => globalRuleDisplay(buildRule(), {spaceNames: spaceNames()}),
                onRemove: null,
            }),
        ],
        actions: overlayActions({
            error,
            saveLabel: "Create rule",
            onSave: save,
            onClose,
            saving,
            disabled: () => !name.val.trim(),
        }),
        backdrop: menuBackdrop(openMenu),
    });
}
