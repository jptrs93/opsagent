// Left sidebar: the OpenDeploy header, the grouped nav built from NAV_GROUPS,
// and a footer with the deployments stream status and sign-out.
//
// Groups show their title only when collapsed. An open group has a slim
// chevron handle (the title fades in on hover) and a hairline separates it
// from the next group; collapsing it replaces the handle with the title row,
// which is what you click to reopen. Collapsed groups are remembered in
// localStorage, a group always opens to reveal the active page, and a
// collapsed group shows the sum of its items' pending badges so an inbox is
// never hidden by a fold.

import van from "vanjs-core";
import {clearLoginState} from "../state/login.js";
import {deploymentsStreamS} from "../state/deployments.js";
import {navigate} from "../lib/router.js";
import {chevronDownIcon, logOutIcon} from "../lib/icons.js";
import {NAV_GROUPS, groupOfPage} from "./nav.js";

const {div, span, h2, p, button} = van.tags;

const COLLAPSED_KEY = "opsagent_sidebar_collapsed_groups";

const streamStatusClass = (status) => status === "connected" ? "text-green-400" : "text-red-400";
const streamDotClass = (status) => status === "connected"
    ? "bg-green-400 animate-pulse [animation-duration:2s]"
    : "bg-red-400";

const readCollapsed = () => {
    try {
        const raw = localStorage.getItem(COLLAPSED_KEY);
        return new Set(raw ? JSON.parse(raw) : []);
    } catch {
        return new Set();
    }
};

const writeCollapsed = (set) => {
    try { localStorage.setItem(COLLAPSED_KEY, JSON.stringify([...set])); } catch {}
};

const pendingBadge = (count) => span({
    class: "ml-auto rounded-full bg-amber-500/20 px-1.5 text-[10px] font-semibold tabular-nums text-amber-300",
    title: `${count} pending`,
}, String(count));

const soonPill = () => span(
    {class: "ml-auto rounded px-1 text-[9px] font-semibold uppercase tracking-wider text-gray-600"}, "soon");

// sidebar(activePage, {badges})
//   badges  {[badgeKey]: van.state(number)} — pending counts shown on the
//           items whose nav entry names that badge key.
export function sidebar(activePage, {badges = {}} = {}) {
    const collapsed = van.state(readCollapsed());

    const toggleGroup = (groupKey) => {
        const next = new Set(collapsed.rawVal);
        if (next.has(groupKey)) next.delete(groupKey); else next.add(groupKey);
        collapsed.val = next;
        writeCollapsed(next);
    };

    // Navigating to a page inside a collapsed group opens the group: the
    // active item must never be hidden.
    van.derive(() => {
        const group = groupOfPage(activePage.val);
        if (!group || !collapsed.rawVal.has(group.key)) return;
        const next = new Set(collapsed.rawVal);
        next.delete(group.key);
        collapsed.val = next;
        writeCollapsed(next);
    });

    const rowClass = (active, future) => `flex items-center gap-2 py-1.5 pl-4 pr-3 rounded cursor-pointer text-sm transition-colors ${
        active
            ? "bg-surface text-white"
            : future
                ? "text-gray-500 hover:text-gray-200 hover:bg-surface-hover"
                : "text-gray-400 hover:text-gray-200 hover:bg-surface-hover"
    }`;

    const badgeCount = (entry) => {
        const count = entry.badge ? badges[entry.badge] : null;
        return count ? Number(count.val) || 0 : 0;
    };

    // A pending badge takes precedence over the "soon" tag: the two together
    // do not fit beside a long label.
    const trailing = (entry) => () => {
        const count = badgeCount(entry);
        if (count > 0) return pendingBadge(count);
        return entry.future ? soonPill() : "";
    };

    const item = (entry) => div({
        "data-testid": `nav-${entry.key}`,
        class: () => rowClass(activePage.val === entry.key, entry.future),
        onclick: () => activePage.val = entry.key,
    }, span(entry.label), trailing(entry));

    const groupBadgeTotal = (group) => group.items.reduce((sum, entry) => sum + badgeCount(entry), 0);

    const header = (group) => {
        const isCollapsed = () => collapsed.val.has(group.key);
        return div({
            "data-testid": `nav-group-${group.key}`,
            class: () => `group flex items-center gap-1.5 px-2 ${isCollapsed() ? "py-1.5" : "py-0.5"} rounded text-gray-500 select-none cursor-pointer hover:text-gray-300`,
            title: group.hint,
            role: "button",
            "aria-label": `Toggle ${group.label}`,
            "aria-expanded": () => String(!isCollapsed()),
            onclick: () => toggleGroup(group.key),
        },
        () => chevronDownIcon({class: `h-3 w-3 flex-none transition-transform ${isCollapsed() ? "-rotate-90" : ""}`}),
        () => isCollapsed()
            ? span({class: "text-[13px] font-medium"}, group.label)
            : span({class: "text-[10px] font-semibold uppercase tracking-wider opacity-0 transition-opacity group-hover:opacity-100"}, group.label),
        () => {
            if (!isCollapsed()) return "";
            const total = groupBadgeTotal(group);
            return total > 0 ? pendingBadge(total) : "";
        });
    };

    const groupBody = (group) => div({class: "flex flex-col gap-0.5"}, ...group.items.map(item));

    const nav = div(
        // The header's bottom border is the first group's separator, so the
        // nav starts with the same gap a separator leaves below itself.
        {class: "px-3 pt-1.5 pb-3 flex flex-col"},
        ...NAV_GROUPS.map((group, index) => div(
            // Without a visible title, a hairline is what separates groups.
            {class: index > 0 ? "mt-1.5 border-t border-gray-800 pt-1.5" : ""},
            header(group),
            () => collapsed.val.has(group.key) ? "" : groupBody(group),
        )),
    );

    return div(
        {class: "w-48 shrink-0 h-full min-h-0 bg-sidebar border-r border-gray-800 flex flex-col overflow-hidden"},
        div(
            {class: "px-4 py-3 border-b border-gray-800"},
            h2({class: "text-lg font-bold text-white"}, "OpenDeploy"),
        ),
        div({class: "app-scroll flex-1 min-h-0 overflow-y-auto"}, nav),
        // One footer row: stream status on the left, aligned with the item
        // labels, and a sign-out icon button on the right.
        div(
            {class: "flex flex-none items-center justify-between border-t border-gray-800 py-2 pl-4 pr-3"},
            div(
                {class: "flex min-w-0 items-center gap-2"},
                span({class: () => `h-2 w-2 flex-none rounded-full ${streamDotClass(deploymentsStreamS.val.status)}`}),
                p({
                    class: () => `truncate text-xs ${streamStatusClass(deploymentsStreamS.val.status)}`,
                    title: () => deploymentsStreamS.val.sentence,
                }, () => deploymentsStreamS.val.sentence),
            ),
            button({
                type: "button",
                "data-testid": "nav-sign-out",
                title: "Sign out",
                "aria-label": "Sign out",
                class: "flex h-7 w-7 flex-none items-center justify-center rounded text-gray-500 transition-colors hover:bg-surface-hover hover:text-gray-200 cursor-pointer",
                onclick: () => {
                    clearLoginState();
                    navigate("/login");
                },
            }, logOutIcon({class: "h-4 w-4"})),
        ),
    );
}
