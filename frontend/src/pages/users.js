// The Users & roles page: a top tab strip in the Deployments page's style
// with two pinned tabs, Users and Roles & rules, each with a toolbar on the
// 30px baseline. Every rule pill, role name and grant chip opens the rule
// explainer (components/ruleExplainer.js). Users come into existence through
// passkey registration during bootstrap or recovery; this page manages what
// they are allowed to do, not who they are.
import van from "vanjs-core";
import {capi} from "../capi/index.js";
import {loginS} from "../state/login.js";
import {authzGlobalRulesS, authzGrantsS, authzTemplatesS, spacesS, usersMapS} from "../state/deployments.js";
import {describeGrant, grantRevokeBlock, groupGrantsByUser, isClusterAdminGrant, templateArguments} from "../lib/authz.js";
import {grantSubject} from "../lib/authzExplain.js";
import {globalRuleOverlay, grantOverlay, ruleTemplateOverlay} from "../components/accessEditors.js";
import {formatDate, formatDateTime} from "../lib/date.js";
import {globalRuleDisplay, ruleDisplay} from "../components/ruleDisplay.js";
import {closeExplainer, explainable, explainerWidth, mountExplainerLayer, renderExplainer} from "../components/ruleExplainer.js";
import {caretRightIcon, closeIcon, editIcon, infoIcon, lockIcon, plusIcon, searchIcon, trashIcon} from "../lib/icons.js";

const {div, p, span, input, button, table, thead, tbody, tr, th, td, colgroup, col, h2} = van.tags;

const USERS = "users";
const RULES = "rules";

const sortedUsers = () => [...usersMapS.val.entries()]
    .map(([id, user]) => ({
        id: Number(id),
        name: user?.name || "",
        createdAt: Number(user?.createdAt || 0),
        lastLoginAt: Number(user?.lastLoginAt || 0),
    }))
    .sort((a, b) => a.id - b.id);

const liveSpaces = () => (spacesS.val || []).filter((space) => space && !space.deleted);
const spaceNameMap = () => new Map(liveSpaces().map((space) => [Number(space.id), space.name || `space ${space.id}`]));
const templatesById = () => new Map((authzTemplatesS.val || []).map((t) => [Number(t.id), t]));
const argNamesOf = (template) => new Map(templateArguments(template).map((a) => [a.id, a.name]));

export function usersPage() {
    mountExplainerLayer();
    const activeTab = van.state(USERS);
    const userSearch = van.state("");
    const ruleSearch = van.state("");
    const error = van.state(null);
    // One overlay at a time: {type: "template", record} | {type: "grant", user}
    // | {type: "globalRule"} | {type: "newUser"} | {type: "confirm", ...}.
    const overlayS = van.state(null);
    const open = {templates: van.state(true), global: van.state(true)};

    // A dialog opened from a chip (revoke, grant) takes the pointer with it,
    // so an explanation that is open, or about to open on hover, goes away.
    van.derive(() => { if (overlayS.val) closeExplainer(); });

    const run = async (action) => {
        try {
            error.val = null;
            await action();
        } catch (e) {
            error.val = e.message;
        }
    };

    // --- explainers ----------------------------------------------------------

    // explainPill makes a rule pill from ruleDisplay (built without its native
    // tooltips, since the card carries the grammar) the trigger of its own
    // explanation, with a hover accent so it reads as something to open.
    // `subject()` runs at open time so the explanation sees the current spaces.
    const explainPill = (wrap, subject) => {
        const pill = wrap.firstElementChild;
        pill.classList.add("transition-colors", "hover:border-blue-400/60");
        explainable(pill, (openState) => renderExplainer(subject(), openState), {
            width: () => explainerWidth(subject()),
        });
        return wrap;
    };

    // The card's header is one line naming what is explained.
    const templateRulePill = (rule, record, index) => explainPill(
        ruleDisplay(rule, {spaceNames: spaceNameMap(), argNames: argNamesOf(record.template), titles: false}),
        () => ({
            kind: "template",
            subtitle: `Rule ${index + 1} of ${record.name}`,
            rules: [rule],
            spaceNames: spaceNameMap(),
            spaces: liveSpaces(),
            argNames: argNamesOf(record.template),
        }));

    const globalRulePill = (record) => explainPill(
        globalRuleDisplay(record.rule, {spaceNames: spaceNameMap(), titles: false}),
        () => ({kind: "global", subtitle: `Global rule ${record.name || record.id}`, rules: [record.rule], spaceNames: spaceNameMap(), spaces: liveSpaces()}));

    // A role's name explains the whole role: every rule together, so the
    // access table is their union with a Rule tab per rule on the left.
    const templateName = (record) => {
        // Same radius as a rule pill, so the pinned ring reads the same;
        // the negative margin keeps the text on the column's left edge.
        const el = span({class: "-mx-1 truncate rounded-md px-1 py-px text-gray-200 transition-colors hover:text-blue-300", "data-testid": `template-name-${record.id}`}, record.name);
        const rules = record.template?.rules || [];
        const subject = () => ({
            kind: "template",
            subtitle: `Role ${record.name}`,
            rules,
            spaceNames: spaceNameMap(),
            spaces: liveSpaces(),
            argNames: argNamesOf(record.template),
        });
        return explainable(el, (openState) => renderExplainer(subject(), openState), {
            width: () => explainerWidth(subject()),
            label: `Explain role ${record.name}`,
        });
    };

    // --- overlays -----------------------------------------------------------

    const smallButton = (text, onclick, tone = "bg-gray-700 text-gray-200 hover:bg-gray-600") => button({
        type: "button",
        class: `text-xs px-3 py-1 rounded-md font-medium ${tone} cursor-pointer`,
        onclick,
    }, text);

    const newUserOverlay = () => div(
        {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"},
        div({class: "card w-full max-w-md flex flex-col gap-4 shadow-2xl"},
            h2({class: "text-base font-semibold"}, "New user"),
            p({class: "text-sm text-gray-300"},
                "Users are created at the login screen: sign in with the setup password and a new username, " +
                "then register a passkey. New users automatically receive the cluster_admin role."),
            div({class: "flex items-center justify-end"}, smallButton("Close", () => { overlayS.val = null; }))),
    );

    const confirmOverlay = ({title, body, confirmLabel, onConfirm}) => div(
        {class: "fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"},
        div({class: "card w-full max-w-md flex flex-col gap-4 shadow-2xl"},
            h2({class: "text-base font-semibold"}, title),
            p({class: "text-sm text-gray-300"}, body),
            div({class: "flex items-center justify-end gap-2"},
                smallButton("Cancel", () => { overlayS.val = null; }),
                smallButton(confirmLabel || "Delete", () => run(async () => {
                    await onConfirm();
                    overlayS.val = null;
                }), "bg-red-600 text-white hover:bg-red-500"))),
    );

    const overlay = () => {
        const active = overlayS.val;
        if (!active) return "";
        const close = () => { overlayS.val = null; };
        const shared = {spaces: liveSpaces, spaceNames: spaceNameMap, onClose: close};
        if (active.type === "template") return ruleTemplateOverlay({record: active.record, ...shared});
        if (active.type === "grant") return grantOverlay({user: active.user, templates: () => authzTemplatesS.val, ...shared});
        if (active.type === "globalRule") return globalRuleOverlay(shared);
        if (active.type === "newUser") return newUserOverlay();
        if (active.type === "confirm") return confirmOverlay(active);
        return "";
    };

    // --- shared table pieces ------------------------------------------------

    const iconButton = (icon, title, onclick, hoverClass = "hover:text-gray-100") => button({
        type: "button",
        title,
        "aria-label": title,
        class: `inline-flex h-6 w-6 items-center justify-center rounded text-gray-500 hover:bg-surface ${hoverClass} cursor-pointer`,
        onclick,
    }, icon);

    const headerRow = (...labels) => thead(tr(
        {class: "text-left text-gray-500 border-b border-gray-800"},
        ...labels.map(([text, cls]) => th({class: `py-1.5 pr-3 text-[10px] font-semibold uppercase tracking-wider ${cls || ""}`}, text)),
    ));

    const emptyLine = (text) => p({class: "px-3 py-3 text-sm text-gray-400"}, text);

    const searchBox = (state, placeholder) => div({class: "relative"},
        searchIcon({class: "pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-gray-500"}),
        input({
            class: "text-input search-input search-input-iconed toolbar-input",
            type: "search",
            placeholder,
            "aria-label": placeholder,
            value: state,
            oninput: (e) => { state.val = e.target.value; },
        }));

    const toolbar = (...children) => div(
        {class: "flex flex-none flex-wrap items-center gap-2 border-b border-gray-700 px-2 py-2"},
        ...children,
    );

    const toolbarButton = (text, onclick, attrs = {}) => button({
        type: "button",
        class: "toolbar-button",
        onclick,
        ...attrs,
    }, plusIcon({class: "w-3.5 h-3.5"}), text);

    // The hint under each toolbar says how to open an explanation; it is the
    // one line of help the page carries, and the explainer does the rest.
    const readingHint = (what) => div(
        {class: "flex flex-none items-center gap-1.5 px-3 py-1 text-[11px] text-gray-500"},
        infoIcon({class: "h-3 w-3 flex-none"}),
        `Hover a ${what} to see exactly what it allows; click to keep the explanation open.`,
    );

    // --- users tab ------------------------------------------------------------

    const filteredUsers = () => {
        const query = userSearch.val.trim().toLowerCase();
        const users = sortedUsers();
        return query ? users.filter((user) => user.name.toLowerCase().includes(query)) : users;
    };

    const revokeControl = (user, grant, chip) => {
        const templates = templatesById();
        const blocked = grantRevokeBlock(grant, {
            grants: authzGrantsS.val,
            templatesById: templates,
            selfUserId: Number(loginS.val?.userId || 0),
        });
        const who = user.name || `user ${user.id}`;
        if (blocked) {
            return span({
                title: blocked,
                "aria-label": `${chip.label} cannot be revoked from ${who}`,
                class: "inline-flex h-3.5 w-3.5 items-center justify-center rounded text-gray-600 cursor-not-allowed",
                onclick: (e) => e.stopPropagation(),
            }, lockIcon({class: "w-2.5 h-2.5"}));
        }
        const revoke = () => capi.postV1AccessGrantsDelete({userId: user.id, id: grant.id});
        return button({
            type: "button",
            title: "Revoke grant",
            "aria-label": `Revoke ${chip.label} from ${who}`,
            class: "inline-flex h-3.5 w-3.5 items-center justify-center rounded text-gray-500 hover:text-red-400 cursor-pointer",
            onclick: (e) => {
                // The chip around this button opens the explainer on click.
                e.stopPropagation();
                if (!isClusterAdminGrant(grant, templates)) return run(revoke);
                overlayS.val = {
                    type: "confirm",
                    title: "Revoke cluster_admin",
                    body: `Remove the cluster_admin role from ${who}? They keep only their remaining grants, ` +
                        "which may leave them with no access at all.",
                    confirmLabel: "Revoke",
                    onConfirm: revoke,
                };
            },
        }, closeIcon({class: "w-3 h-3"}));
    };

    // A grant chip explains the whole grant: the role's rules with the bound
    // arguments filled in, or the direct rule.
    const grantChip = (user, grant) => {
        const chip = describeGrant(grant, templatesById(), spaceNameMap());
        const el = span(
            {
                class: `inline-flex items-center gap-1.5 rounded border px-1.5 py-px text-[11px] transition-colors ` +
                    (chip.template
                        ? "border-blue-500/30 bg-blue-500/10 text-gray-300 hover:border-blue-400/60"
                        : "border-gray-700 bg-gray-950/40 text-gray-400 hover:border-blue-400/60"),
            },
            span({class: `font-medium ${chip.template ? "text-blue-300" : "text-gray-200"}`}, chip.label),
            chip.detail ? span({class: "text-gray-400"}, chip.template ? `(${chip.detail})` : chip.detail) : "",
            revokeControl(user, grant, chip),
        );
        const subject = () => grantSubject(grant, {templatesById: templatesById(), spaceNames: spaceNameMap(), spaces: liveSpaces()});
        return explainable(el, (openState) => renderExplainer(subject(), openState), {
            width: () => explainerWidth(subject()),
            label: `Explain ${chip.label} granted to ${user.name || user.id}`,
        });
    };

    const userRow = (user, grantsByUser) => {
        const isSelf = Number(loginS.val?.userId || 0) === user.id;
        const grants = grantsByUser.get(user.id) || [];
        return tr(
            {class: "border-b border-gray-800 last:border-0 align-middle", "data-testid": `user-row-${user.id}`},
            td({class: "py-1.5 pr-3 min-w-0"},
                div({class: "flex items-center gap-2 min-w-0"},
                    span({class: "truncate text-gray-200"}, user.name || `user ${user.id}`),
                    isSelf ? span(
                        {class: "shrink-0 rounded bg-gray-800 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-gray-500"},
                        "you") : "")),
            td({class: "py-1.5 pr-3 text-gray-400 whitespace-nowrap tabular-nums"}, String(user.id)),
            td({class: "py-1.5 pr-3 text-gray-400 whitespace-nowrap"}, formatDate(new Date(user.createdAt), "—")),
            td({class: "py-1.5 pr-3 text-gray-400 whitespace-nowrap"}, formatDateTime(new Date(user.lastLoginAt), "—")),
            td({class: "py-1.5 pr-3"},
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

    const usersTable = () => {
        const visible = filteredUsers();
        if (!visible.length) return emptyLine(userSearch.val.trim() ? "No users match your search." : "No users yet.");
        const grantsByUser = groupGrantsByUser(authzGrantsS.val);
        return div({class: "px-3"},
            table({class: "w-full table-fixed text-[13px]"},
                colgroup(col({style: "width:18%"}), col({style: "width:5%"}), col({style: "width:11%"}), col({style: "width:12%"}), col({style: "width:54%"})),
                headerRow(["Name"], ["ID"], ["Joined"], ["Last login"], ["Permissions"]),
                tbody(...visible.map((user) => userRow(user, grantsByUser)))));
    };

    const usersPanel = div(
        {class: "flex h-full min-h-0 flex-col", "data-testid": "users-tab-panel"},
        toolbar(
            searchBox(userSearch, "Search users"),
            div({class: "flex-1"}),
            toolbarButton("New user", () => { overlayS.val = {type: "newUser"}; }, {"data-testid": "new-user-button"})),
        div({class: "app-scroll flex flex-1 min-h-0 flex-col overflow-y-auto"}, readingHint("permission"), usersTable),
    );

    // --- roles & rules tab ------------------------------------------------------

    const matchesRuleSearch = (name) => {
        const query = ruleSearch.val.trim().toLowerCase();
        return !query || (name || "").toLowerCase().includes(query);
    };

    const usedBy = (templateId) => {
        const names = new Set();
        for (const grant of authzGrantsS.val || []) {
            if (Number(grant.templateId) !== Number(templateId)) continue;
            const user = usersMapS.val.get(Number(grant.userId));
            names.add(user?.name || `user ${grant.userId}`);
        }
        return [...names];
    };

    const templateRow = (record) => {
        const args = templateArguments(record.template);
        const holders = usedBy(record.id);
        return tr(
            {class: "border-b border-gray-800 last:border-0 align-top", "data-testid": `template-row-${record.id}`},
            td({class: "py-1.5 pr-3 min-w-0"},
                div({class: "flex items-center gap-2 min-w-0"},
                    templateName(record),
                    record.builtin ? span(
                        {class: "shrink-0 rounded bg-gray-800 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-gray-500"},
                        "built in") : "")),
            td({class: "py-1.5 pr-3 font-mono text-[11px] text-amber-300"},
                args.length ? args.map((a) => "${" + a.name + "}").join(", ") : span({class: "text-gray-600"}, "—")),
            td({class: "py-1.5 pr-3 min-w-0"},
                div({class: "flex flex-col gap-1"},
                    ...(record.template?.rules || []).map((rule, i) => templateRulePill(rule, record, i)))),
            td({class: "py-1.5 pr-3 min-w-0"},
                holders.length
                    ? div({class: "flex flex-wrap gap-1"}, ...holders.map((name) => span(
                        {class: "rounded border border-gray-700 bg-gray-950/40 px-1.5 py-px text-[11px] text-gray-300"}, name)))
                    : span({class: "text-gray-600"}, "—")),
            td({class: "py-1.5 pl-2 text-right whitespace-nowrap w-px"},
                record.builtin ? "" : div({class: "flex items-center justify-end gap-0.5"},
                    iconButton(editIcon({class: "w-3.5 h-3.5"}), `Edit role ${record.name}`,
                        () => { overlayS.val = {type: "template", record}; }),
                    iconButton(trashIcon({class: "w-3.5 h-3.5"}), `Delete role ${record.name}`,
                        () => {
                            overlayS.val = {
                                type: "confirm",
                                title: "Delete role",
                                body: holders.length
                                    ? `The role ${record.name} is granted to ${holders.join(", ")}. Revoke those grants first; roles referenced by grants cannot be deleted.`
                                    : `Delete the role ${record.name}?`,
                                onConfirm: () => capi.postV1AccessRuleTemplatesDelete({id: record.id}),
                            };
                        }, "hover:text-red-400"))),
        );
    };

    const templatesSection = () => {
        if (!open.templates.val) return "";
        const templates = (authzTemplatesS.val || []).filter((t) => matchesRuleSearch(t.name));
        if (!templates.length) return emptyLine(ruleSearch.val.trim() ? "No roles match your search." : "No roles yet.");
        return div({class: "px-3"},
            table({class: "w-full table-fixed text-[13px]"},
                colgroup(col({style: "width:15%"}), col({style: "width:9%"}), col({style: "width:58%"}), col({style: "width:12%"}), col({style: "width:6%"})),
                headerRow(["Name"], ["Arguments"], ["Rules"], ["Granted to"], ["", "w-px"]),
                tbody(...templates.map(templateRow))));
    };

    const globalRuleRow = (record) => tr(
        {class: "border-b border-gray-800 last:border-0 align-middle", "data-testid": `global-rule-row-${record.id}`},
        td({class: "py-1.5 pr-3 min-w-0"},
            div({class: "truncate text-gray-200", title: record.name || ""}, record.name || `rule ${record.id}`)),
        td({class: "py-1.5 pr-3 min-w-0"}, globalRulePill(record)),
        td({class: "py-1.5 pl-2 text-right whitespace-nowrap w-px"},
            iconButton(trashIcon({class: "w-3.5 h-3.5"}), `Delete global rule ${record.name || record.id}`,
                () => {
                    overlayS.val = {
                        type: "confirm",
                        title: "Delete global rule",
                        body: record.rule?.deny
                            ? `Delete the global rule ${record.name || record.id}? Requests it denied become subject to user grants again.`
                            : `Delete the global rule ${record.name || record.id}? Everyone loses what it allowed unless their own grants cover it.`,
                        onConfirm: () => capi.postV1AccessGlobalRulesDelete({id: record.id}),
                    };
                }, "hover:text-red-400")),
    );

    const globalRulesSection = () => {
        if (!open.global.val) return "";
        const rules = (authzGlobalRulesS.val || []).filter((r) => matchesRuleSearch(r.name));
        if (!rules.length) {
            return emptyLine(ruleSearch.val.trim() ? "No global rules match your search."
                : "No global rules. A global rule denies matching requests for everyone before any grant applies, or allows them for everyone alongside grants.");
        }
        return div({class: "px-3"},
            table({class: "w-full table-fixed text-[13px]"},
                colgroup(col({style: "width:16%"}), col({style: "width:77%"}), col({style: "width:7%"})),
                headerRow(["Name"], ["Rule"], ["", "w-px"]),
                tbody(...rules.map(globalRuleRow))));
    };

    // The section bands take the space band's style from the Deployments,
    // Assets and Secrets pages: a recessed row with a caret, a semibold mono
    // title and a small count, the whole band toggling the section.
    const sectionBand = (openState, title, count) => div(
        {
            class: "flex flex-none cursor-default items-center gap-1.5 border-b border-gray-800/80 bg-gray-950/30 px-2 py-1 font-mono text-[13px] hover:bg-gray-700/35",
            onclick: () => { openState.val = !openState.val; },
        },
        button({
            type: "button",
            "aria-expanded": () => String(openState.val),
            "aria-label": () => openState.val ? `Collapse ${title}` : `Expand ${title}`,
            class: "flex h-4 w-4 flex-none items-center justify-center rounded-sm text-gray-500 hover:text-gray-100 hover:bg-white/10 cursor-pointer",
            onclick: (e) => { e.stopPropagation(); openState.val = !openState.val; },
        }, caretRightIcon({class: () => `w-[11px] h-[11px] transition-transform ${openState.val ? "rotate-90" : ""}`})),
        h2({class: "font-semibold text-gray-100"}, title),
        span({class: "text-[10.5px] text-gray-500"}, count),
    );

    const rulesPanel = div(
        {class: "flex h-full min-h-0 flex-col", "data-testid": "rules-tab-panel"},
        toolbar(
            searchBox(ruleSearch, "Search roles and rules"),
            div({class: "flex-1"}),
            toolbarButton("New role", () => { overlayS.val = {type: "template", record: null}; }, {"data-testid": "new-role-button"}),
            toolbarButton("New global rule", () => { overlayS.val = {type: "globalRule"}; }, {"data-testid": "new-global-rule-button"})),
        div({class: "app-scroll flex flex-1 min-h-0 flex-col overflow-y-auto"},
            readingHint("rule or role name"),
            sectionBand(open.templates, "Roles", () => String((authzTemplatesS.val || []).length)),
            templatesSection,
            sectionBand(open.global, "Global rules", () => String((authzGlobalRulesS.val || []).length)),
            globalRulesSection),
    );

    // --- tab strip ----------------------------------------------------------------

    // Same geometry as pages/deployments.js: the first tab is flush with the
    // sidebar, the active tab shares the panel's surface tone so it reads as
    // attached to it. Both tabs are pinned; nothing here opens or closes.
    const tabButton = ({id, title, count, first = false}) => {
        const active = () => activeTab.val === id;
        return div(
            {
                role: "tab",
                "aria-selected": () => String(active()),
                "data-testid": `users-tab-${id}`,
                tabindex: 0,
                class: () => `relative -mb-px flex h-8 shrink-0 cursor-pointer select-none items-center gap-1.5 border border-b-0 px-3 text-xs transition-colors ${first ? "rounded-tr-md border-l-0" : "rounded-t-md"} ` + (active()
                    ? "border-gray-700 bg-surface text-white"
                    : "border-transparent text-gray-400 hover:bg-white/5 hover:text-gray-200"),
                onclick: () => { activeTab.val = id; },
                onkeydown: (event) => {
                    if (event.key === "Enter" || event.key === " ") { event.preventDefault(); activeTab.val = id; }
                },
            },
            span({class: "font-medium"}, title),
            span({class: () => `rounded px-1 text-[10px] tabular-nums ${active() ? "bg-white/10 text-gray-300" : "bg-white/5 text-gray-500"}`}, count),
        );
    };

    const tabStrip = div(
        {class: "flex h-[34px] flex-none flex-nowrap items-end gap-0.5 border-b border-gray-700 bg-gray-900/80 pr-2 pt-0.5", role: "tablist", "aria-label": "Users and roles"},
        tabButton({id: USERS, title: "Users", count: () => String(sortedUsers().length), first: true}),
        tabButton({id: RULES, title: "Roles & rules", count: () => String((authzTemplatesS.val || []).length + (authzGlobalRulesS.val || []).length)}),
    );

    const panel = (id, content) => div({class: () => `h-full min-h-0 min-w-0 ${activeTab.val === id ? "" : "hidden"}`}, content);

    return div(
        {class: "flex h-full min-h-0 min-w-0 flex-col bg-surface", "data-testid": "users-page"},
        tabStrip,
        () => error.val ? p(
            {class: "flex-none border-b border-red-500/30 bg-red-500/10 px-3 py-1.5 text-xs text-red-300"},
            `Error: ${error.val}`) : "",
        div({class: "relative flex-1 min-h-0 min-w-0"},
            panel(USERS, usersPanel),
            panel(RULES, rulesPanel)),
        overlay,
    );
}
