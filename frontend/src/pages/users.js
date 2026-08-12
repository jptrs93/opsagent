import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {loginS} from "../state/login.js";
import {
    authzGlobalRulesS,
    authzGrantsS,
    authzTemplatesS,
    spacesS,
    usersMapS,
} from "../state/deployments.js";
import {
    describeGrant,
    formatGlobalRule,
    formatRule,
    groupGrantsByUser,
    templateArguments,
} from "../lib/authz.js";
import {globalRuleOverlay, grantOverlay, ruleTemplateOverlay} from "../components/accessEditors.js";
import {chevronDownIcon, closeIcon, editIcon, plusIcon, trashIcon} from "../lib/icons.js";

const {div, p, span, input, button, table, thead, tbody, tr, th, td, colgroup, col, h2} = van.tags;

// Users come into existence through passkey registration during bootstrap or
// recovery; this page manages what they are allowed to do, not who they are.
const sortedUsers = () => [...usersMapS.val.entries()]
    .map(([id, name]) => ({id: Number(id), name: name || ""}))
    .sort((a, b) => a.id - b.id);

const liveSpaces = () => (spacesS.val || []).filter((space) => space && !space.deleted);
const spaceNameMap = () => new Map(liveSpaces().map((space) => [Number(space.id), space.name || `space ${space.id}`]));
const templatesById = () => new Map((authzTemplatesS.val || []).map((t) => [Number(t.id), t]));

const ARG_PREVIEW_NAMES = (template) => new Map(templateArguments(template).map((a) => [a.id, a.name]));

export function usersPage() {
    const search = van.state("");
    const error = van.state(null);
    // One overlay at a time: {type: "template", record} | {type: "grant", user}
    // | {type: "globalRule"} | {type: "confirm", ...}.
    const overlayS = van.state(null);
    const open = {users: van.state(true), templates: van.state(true), global: van.state(true)};

    const filteredUsers = () => {
        const query = search.val.trim().toLowerCase();
        const users = sortedUsers();
        if (!query) return users;
        return users.filter((user) => user.name.toLowerCase().includes(query));
    };

    const run = async (action) => {
        try {
            error.val = null;
            await action();
        } catch (e) {
            error.val = e.message;
        }
    };

    // There is no create-user endpoint: users come into existence by signing
    // in with the setup password under a new name. The button exists for
    // discoverability and explains that flow.
    const newUserOverlay = () => div(
        {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"},
        div({class: "card w-full max-w-md flex flex-col gap-4 shadow-2xl"},
            h2({class: "text-base font-semibold"}, "New user"),
            p({class: "text-sm text-gray-300"},
                "Users are created at the login screen: sign in with the setup password and a new username, " +
                "then register a passkey. New users automatically receive the cluster_admin template."),
            div({class: "flex items-center justify-end"},
                button({
                    type: "button",
                    class: "text-xs px-3 py-1 rounded-md font-medium bg-gray-700 text-gray-200 hover:bg-gray-600 cursor-pointer",
                    onclick: () => { overlayS.val = null; },
                }, "Close"))),
    );

    const confirmOverlay = ({title, body, onConfirm}) => div(
        {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"},
        div({class: "card w-full max-w-md flex flex-col gap-4 shadow-2xl"},
            h2({class: "text-base font-semibold"}, title),
            p({class: "text-sm text-gray-300"}, body),
            div({class: "flex items-center justify-end gap-2"},
                button({
                    type: "button",
                    class: "text-xs px-3 py-1 rounded-md font-medium bg-gray-700 text-gray-200 hover:bg-gray-600 cursor-pointer",
                    onclick: () => { overlayS.val = null; },
                }, "Cancel"),
                button({
                    type: "button",
                    class: "text-xs px-3 py-1 rounded-md font-medium bg-red-600 text-white hover:bg-red-500 cursor-pointer",
                    onclick: () => run(async () => {
                        await onConfirm();
                        overlayS.val = null;
                    }),
                }, "Delete"))),
    );

    // ---- section band ------------------------------------------------------

    const sectionBand = (openState, title, count, ...actions) => div(
        // first:border-t-0 keeps the top band flush with the window edge now
        // that the page has no surrounding card.
        {class: "flex flex-none flex-wrap items-center gap-2 border-y border-gray-700 first:border-t-0 bg-gray-950/40 px-2 py-1"},
        button({
            type: "button",
            "aria-expanded": () => String(openState.val),
            class: "inline-flex items-center gap-1.5 rounded px-1 py-0.5 text-[11px] font-semibold uppercase tracking-wider text-gray-400 hover:text-gray-200 cursor-pointer",
            onclick: () => { openState.val = !openState.val; },
        },
        chevronDownIcon({class: () => `w-3 h-3 transition-transform ${openState.val ? "" : "-rotate-90"}`}),
        title),
        span({class: "text-[11px] text-gray-500 tabular-nums"}, count),
        div({class: "flex-1"}),
        ...actions,
    );

    const bandButton = (text, onclick) => button({
        type: "button",
        class: "inline-flex items-center gap-1 text-xs px-2 py-0.5 rounded border border-gray-600 text-gray-300 hover:bg-surface-hover cursor-pointer",
        onclick,
    }, plusIcon({class: "w-3 h-3"}), text);

    const iconButton = (icon, title, onclick, hoverClass = "hover:text-gray-100") => button({
        type: "button",
        title,
        "aria-label": title,
        class: `inline-flex h-6 w-6 items-center justify-center rounded text-gray-500 hover:bg-surface ${hoverClass} cursor-pointer`,
        onclick,
    }, icon);

    const headerRow = (...labels) => thead(tr(
        {class: "text-left text-gray-500 border-b border-gray-800"},
        ...labels.map(([text, cls]) => th({class: `py-1 pr-3 text-[10px] font-semibold uppercase tracking-wider ${cls || ""}`}, text)),
    ));

    // ---- users section -----------------------------------------------------

    const grantChip = (user, grant) => {
        const chip = describeGrant(grant, templatesById(), spaceNameMap());
        return span(
            {
                class: `inline-flex items-center gap-1.5 rounded border px-1.5 py-px text-[11px] ` +
                    (chip.template
                        ? "border-blue-500/30 bg-blue-500/10 text-gray-300"
                        : "border-gray-700 bg-gray-950/40 text-gray-400"),
                title: chip.title,
            },
            span({class: `font-medium ${chip.template ? "text-blue-300" : "text-gray-200"}`}, chip.label),
            chip.detail ? span({class: "text-gray-400"}, chip.template ? `(${chip.detail})` : chip.detail) : "",
            chip.delegable ? span({class: "text-teal-400 whitespace-nowrap"}, "agents ✓") : "",
            button({
                type: "button",
                title: "Revoke grant",
                "aria-label": `Revoke ${chip.label} from ${user.name || user.id}`,
                class: "inline-flex h-3.5 w-3.5 items-center justify-center rounded text-gray-500 hover:text-red-400 cursor-pointer",
                onclick: () => run(() => capi.postV1AccessGrantsDelete({userId: user.id, id: grant.id})),
            }, closeIcon({class: "w-3 h-3"})),
        );
    };

    const userRow = (user, grantsByUser) => {
        const isSelf = Number(loginS.val?.userId || 0) === user.id;
        const grants = grantsByUser.get(user.id) || [];
        return tr(
            {class: "border-b border-gray-800 last:border-0 align-middle", "data-testid": `user-row-${user.id}`},
            td({class: "py-0.5 pr-3 min-w-0"},
                div({class: "flex items-center gap-2 min-w-0"},
                    span({class: "truncate text-gray-200"}, user.name || `user ${user.id}`),
                    isSelf ? span(
                        {class: "shrink-0 rounded bg-gray-800 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-gray-500"},
                        "you") : "")),
            td({class: "py-0.5 pr-3 text-gray-400 whitespace-nowrap tabular-nums"}, String(user.id)),
            td({class: "py-0.5 pr-3"},
                div({class: "flex flex-wrap items-center gap-1"},
                    ...grants.map((grant) => grantChip(user, grant)),
                    button({
                        type: "button",
                        title: `Grant access to ${user.name || user.id}`,
                        "aria-label": `Grant access to ${user.name || user.id}`,
                        class: "inline-flex h-5 w-5 items-center justify-center rounded border border-dashed border-gray-600 text-gray-500 hover:text-gray-200 hover:border-gray-400 cursor-pointer",
                        onclick: () => { overlayS.val = {type: "grant", user}; },
                    }, plusIcon({class: "w-3 h-3"})))),
        );
    };

    const usersSection = () => {
        if (!open.users.val) return "";
        const visible = filteredUsers();
        if (!visible.length) {
            return p({class: "px-2 py-2 text-gray-400 text-sm"},
                search.val.trim() ? "No users match your search." : "No users yet.");
        }
        const grantsByUser = groupGrantsByUser(authzGrantsS.val);
        return div({class: "px-2"},
            table({class: "w-full table-fixed text-[13px]"},
                colgroup(col({style: "width:24%"}), col({style: "width:8%"}), col({style: "width:68%"})),
                headerRow(["Name"], ["ID"], ["Permissions"]),
                tbody(...visible.map((user) => userRow(user, grantsByUser)))));
    };

    // ---- templates section -------------------------------------------------

    const templateRow = (record) => {
        const args = templateArguments(record.template);
        const argNames = ARG_PREVIEW_NAMES(record.template);
        return tr(
            {class: "border-b border-gray-800 last:border-0 align-top", "data-testid": `template-row-${record.id}`},
            td({class: "py-0.5 pr-3 min-w-0"},
                div({class: "flex items-center gap-2 min-w-0"},
                    span({class: "truncate text-gray-200"}, record.name),
                    record.builtin ? span(
                        {class: "shrink-0 rounded bg-gray-800 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-gray-500"},
                        "built in") : "")),
            td({class: "py-0.5 pr-3 font-mono text-[11px] text-amber-300"},
                args.length ? args.map((a) => "${" + a.name + "}").join(", ") : span({class: "text-gray-600"}, "—")),
            td({class: "py-0.5 pr-3"},
                div({class: "flex flex-col"},
                    ...(record.template?.rules || []).map((rule) => span(
                        {class: "font-mono text-[11px] text-gray-300 whitespace-nowrap"},
                        formatRule(rule, {spaceNames: spaceNameMap(), argNames}))))),
            td({class: "py-0.5 pl-2 text-right whitespace-nowrap w-px"},
                record.builtin ? "" : div({class: "flex items-center justify-end gap-0.5"},
                    iconButton(editIcon({class: "w-3.5 h-3.5"}), `Edit template ${record.name}`,
                        () => { overlayS.val = {type: "template", record}; }),
                    iconButton(trashIcon({class: "w-3.5 h-3.5"}), `Delete template ${record.name}`,
                        () => {
                            overlayS.val = {
                                type: "confirm",
                                title: "Delete template",
                                body: `Delete the rule template ${record.name}? Templates referenced by grants cannot be deleted.`,
                                onConfirm: () => capi.postV1AccessRuleTemplatesDelete({id: record.id}),
                            };
                        }, "hover:text-red-400"))),
        );
    };

    const templatesSection = () => {
        if (!open.templates.val) return "";
        const templates = authzTemplatesS.val || [];
        if (!templates.length) {
            return p({class: "px-2 py-2 text-gray-400 text-sm"}, "No templates yet.");
        }
        return div({class: "px-2"},
            table({class: "w-full table-fixed text-[13px]"},
                colgroup(col({style: "width:20%"}), col({style: "width:14%"}), col({style: "width:58%"}), col({style: "width:8%"})),
                headerRow(["Name"], ["Arguments"], ["Rules"], ["", "w-px"]),
                tbody(...templates.map(templateRow))));
    };

    // ---- global rules section ----------------------------------------------

    const globalRuleRow = (record) => tr(
        {class: "border-b border-gray-800 last:border-0 align-middle", "data-testid": `global-rule-row-${record.id}`},
        td({class: "py-0.5 pr-3 min-w-0"},
            span({class: "truncate text-gray-200"}, record.name || `rule ${record.id}`)),
        td({class: "py-0.5 pr-3"},
            record.rule?.delegatedOnly
                ? span({class: "rounded border border-teal-700/60 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-teal-400 whitespace-nowrap"}, "agents only")
                : span({class: "text-gray-500 text-[11px]"}, "everyone")),
        td({class: "py-0.5 pr-3"},
            span({class: "font-mono text-[11px] text-gray-300 whitespace-nowrap"},
                formatGlobalRule(record.rule, {spaceNames: spaceNameMap()}))),
        td({class: "py-0.5 pl-2 text-right whitespace-nowrap w-px"},
            iconButton(trashIcon({class: "w-3.5 h-3.5"}), `Delete global rule ${record.name || record.id}`,
                () => {
                    overlayS.val = {
                        type: "confirm",
                        title: "Delete global rule",
                        body: `Delete the global rule ${record.name || record.id}? Requests it denied become subject to user grants again.`,
                        onConfirm: () => capi.postV1AccessGlobalRulesDelete({id: record.id}),
                    };
                }, "hover:text-red-400")),
    );

    const globalRulesSection = () => {
        if (!open.global.val) return "";
        const rules = authzGlobalRulesS.val || [];
        if (!rules.length) {
            return p({class: "px-2 py-2 text-gray-400 text-sm"},
                "No global rules. Global rules deny matching requests for everyone, before any grant applies.");
        }
        return div({class: "px-2"},
            table({class: "w-full table-fixed text-[13px]"},
                colgroup(col({style: "width:20%"}), col({style: "width:12%"}), col({style: "width:60%"}), col({style: "width:8%"})),
                headerRow(["Name"], ["Applies to"], ["Denies"], ["", "w-px"]),
                tbody(...rules.map(globalRuleRow))));
    };

    // ---- overlays ----------------------------------------------------------

    const overlay = () => {
        const active = overlayS.val;
        if (!active) return "";
        const close = () => { overlayS.val = null; };
        const shared = {spaces: liveSpaces, spaceNames: spaceNameMap, onClose: close};
        if (active.type === "template") return ruleTemplateOverlay({record: active.record, ...shared});
        if (active.type === "grant") {
            return grantOverlay({user: active.user, templates: () => authzTemplatesS.val, ...shared});
        }
        if (active.type === "globalRule") return globalRuleOverlay(shared);
        if (active.type === "newUser") return newUserOverlay();
        if (active.type === "confirm") return confirmOverlay(active);
        return "";
    };

    return div(
        // bg-surface: like the assets and secrets explorers, the page is one
        // flush surface running to the window and sidebar edges.
        {class: "h-full min-h-0 flex flex-col overflow-hidden bg-surface"},
        () => error.val ? p(
            {class: "flex-none border-b border-red-500/30 bg-red-500/10 px-3 py-1.5 text-xs text-red-300"},
            `Error: ${error.val}`) : "",
        div({class: "flex-1 flex flex-col min-w-0 min-h-0 overflow-y-auto"},
            sectionBand(open.users, "Users", () => String(sortedUsers().length),
                input({
                    class: "text-input search-input py-1! text-xs",
                    type: "search",
                    placeholder: "Search users",
                    "aria-label": "Search users",
                    value: search,
                    oninput: (e) => { search.val = e.target.value; },
                }),
                bandButton("New user", () => { overlayS.val = {type: "newUser"}; })),
            usersSection,
            sectionBand(open.templates, "Templates", () => String((authzTemplatesS.val || []).length),
                bandButton("New template", () => { overlayS.val = {type: "template", record: null}; })),
            templatesSection,
            sectionBand(open.global, "Global rules", () => String((authzGlobalRulesS.val || []).length),
                bandButton("New rule", () => { overlayS.val = {type: "globalRule"}; })),
            globalRulesSection,
        ),
        overlay,
    );
}
