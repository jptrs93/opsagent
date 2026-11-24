// Left sidebar: the OpenDeploy header, the grouped nav built from NAV_GROUPS,
// and a footer with the theme picker, the stream status and sign-out.
//
// Groups show their title only when collapsed. An open group has a slim
// chevron-only handle (no title, hovered or not) and a hairline separates it
// from the next group; collapsing it replaces the handle with the title row,
// which is what you click to reopen. Collapsed groups are remembered in
// localStorage, a group always opens to reveal the active page, and a
// collapsed group shows the sum of its items' pending badges so an inbox is
// never hidden by a fold.

import van from "vanjs-core";
import {clearLoginState} from "../state/login.js";
import {navigate} from "../lib/router.js";
import {chevronDownIcon, logOutIcon} from "../lib/icons.js";
import {NAV_GROUPS, groupOfPage} from "./nav.js";
import {themePicker} from "./themePicker.js";
import {streamStatus} from "./streamStatus.js";

const {div, span, h2, button} = van.tags;

const COLLAPSED_KEY = "opsagent_sidebar_collapsed_groups";

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

const soonPill = () => span({class: "nav-soon"}, "soon");

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
        class: `nav-item ${entry.future ? "nav-item-future" : ""}`,
        "aria-current": () => activePage.val === entry.key ? "page" : "false",
        onclick: () => activePage.val = entry.key,
    }, span(entry.label), trailing(entry));

    const groupBadgeTotal = (group) => group.items.reduce((sum, entry) => sum + badgeCount(entry), 0);

    const header = (group) => {
        const isCollapsed = () => collapsed.val.has(group.key);
        return div({
            "data-testid": `nav-group-${group.key}`,
            class: () => `nav-group-handle ${isCollapsed() ? "py-1.5" : "py-0.5"}`,
            title: group.hint,
            role: "button",
            "aria-label": `Toggle ${group.label}`,
            "aria-expanded": () => String(!isCollapsed()),
            onclick: () => toggleGroup(group.key),
        },
        () => chevronDownIcon({class: `h-3 w-3 flex-none transition-transform ${isCollapsed() ? "-rotate-90" : ""}`}),
        () => isCollapsed() ? span({class: "text-[13px] font-medium"}, group.label) : "",
        () => {
            if (!isCollapsed()) return "";
            const total = groupBadgeTotal(group);
            return total > 0 ? pendingBadge(total) : "";
        });
    };

    const groupBody = (group) => div({class: "flex flex-col gap-0.5"}, ...group.items.map(item));

    const nav = div(
        {class: "sidebar-nav"},
        ...NAV_GROUPS.map((group) => div(
            {class: "nav-group"},
            header(group),
            () => collapsed.val.has(group.key) ? "" : groupBody(group),
        )),
    );

    return div(
        {class: "sidebar"},
        div({class: "sidebar-head"}, h2("OpenDeploy")),
        div({class: "app-scroll flex-1 min-h-0 overflow-y-auto"}, nav),
        div(
            {class: "sidebar-foot"},
            themePicker(),
            streamStatus(),
            button({
                type: "button",
                "data-testid": "nav-sign-out",
                title: "Sign out",
                "aria-label": "Sign out",
                class: "icon-button",
                onclick: () => {
                    clearLoginState();
                    navigate("/login");
                },
            }, logOutIcon({class: "h-4 w-4"})),
        ),
    );
}
