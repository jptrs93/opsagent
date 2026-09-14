// A static replica of the dashboard shell (src/components/sidebar.js) so the
// page is judged in situ: the tab strip has to read as flush with the sidebar
// the way the Deployments page's does. No login state, no stream, no
// localStorage; every group is open and "Users & roles" is the active item.
import van from "vanjs-core";
import {NAV_GROUPS} from "/src/components/nav.js";
import {chevronDownIcon, logOutIcon} from "/src/lib/icons.js";

const {div, span, h2, p, button} = van.tags;

const ACTIVE = "users";

const item = (entry) => div({
    class: `flex items-center gap-2 py-1.5 pl-4 pr-3 rounded cursor-pointer text-sm transition-colors ${entry.key === ACTIVE
        ? "bg-surface text-white"
        : entry.future ? "text-gray-500 hover:text-gray-200 hover:bg-surface-hover" : "text-gray-400 hover:text-gray-200 hover:bg-surface-hover"}`,
}, span(entry.label), entry.future ? span({class: "ml-auto rounded px-1 text-[9px] font-semibold uppercase tracking-wider text-gray-600"}, "soon") : "");

const header = (group) => div(
    {class: "flex items-center gap-1.5 px-2 py-0.5 rounded text-gray-500 select-none cursor-pointer hover:text-gray-300", title: group.hint},
    chevronDownIcon({class: "h-3 w-3 flex-none"}),
);

const sidebar = () => div(
    {class: "w-48 shrink-0 h-full min-h-0 bg-sidebar border-r border-gray-800 flex flex-col overflow-hidden"},
    div({class: "px-4 py-3 border-b border-gray-800"}, h2({class: "text-lg font-bold text-white"}, "OpenDeploy")),
    div({class: "app-scroll flex-1 min-h-0 overflow-y-auto"},
        div({class: "px-3 pt-1.5 pb-3 flex flex-col"},
            ...NAV_GROUPS.map((group, index) => div(
                {class: index > 0 ? "mt-1.5 border-t border-gray-800 pt-1.5" : ""},
                header(group),
                div({class: "flex flex-col gap-0.5"}, ...group.items.map(item)))))),
    div(
        {class: "flex flex-none items-center justify-between border-t border-gray-800 py-2 pl-4 pr-3"},
        div({class: "flex min-w-0 items-center gap-2"},
            span({class: "h-2 w-2 flex-none rounded-full bg-green-400"}),
            p({class: "truncate text-xs text-green-400"}, "fixture (in memory)")),
        button({type: "button", title: "Sign out (inert in the fixture)", class: "flex h-7 w-7 flex-none items-center justify-center rounded text-gray-500 hover:bg-surface-hover hover:text-gray-200 cursor-pointer"},
            logOutIcon({class: "h-4 w-4"})),
    ),
);

export const shell = (content) => div(
    {class: "h-dvh min-h-dvh w-dvw flex overflow-hidden"},
    sidebar(),
    div({class: "h-full flex-1 min-w-0 min-h-0 overflow-hidden"}, content),
);
