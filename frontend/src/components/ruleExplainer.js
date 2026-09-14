// The rule explainer: a popover that opens from a rule pill, a role name or a
// grant chip on the Users & roles page and shows the rule as written beside
// what it allows, one table per space, each cell saying who reaches it: the
// person, their agent sessions, both, or nobody.
//
// Two halves. The popover machinery keeps one card open at a time, opens it
// on hover and pins it on click, places it under its anchor (above when short
// of room) and keeps it there while the table scrolls. The rendering turns a
// subject ({rules, spaceNames, spaces, argNames, bindings, subtitle})
// into space tabs over the icon table through explainMatrix.
import van from "vanjs-core";
import {closeIcon} from "../lib/icons.js";
import {CLUSTER_LEVEL, ENTITY_GLOSS, ENTITY_SHORT, PAIR_GLOSS, VERB_GLOSS, VERB_SHORT, explainMatrix, mergeGrid, ruleDefinition} from "../lib/authzExplain.js";

const {button, div, span, p, sup, table, thead, tbody, tr, th, td} = van.tags;
const {svg, path, circle, rect} = van.tags("http://www.w3.org/2000/svg");

// --- popover state -----------------------------------------------------------

// open: {anchor, width, render, pinned}
const openS = van.state(null);
let hoverTimer = 0;
let leaveTimer = 0;
let popoverEl = null;

// After Esc closes a card the pointer is often resting on another pill (the
// card covered it), and the browser reports that pill as entered without the
// mouse moving. Hover stays off until the pointer really moves.
let hoverArmed = true;
let hoverLock = {x: NaN, y: NaN};
let lastMouse = {x: NaN, y: NaN};
// anchor → its hover starter, so re-arming can open the pill under the pointer
// (its enter event fired while hover was off).
const hoverStarts = new WeakMap();

const cancelTimers = () => { clearTimeout(hoverTimer); clearTimeout(leaveTimer); };
export const closeExplainer = () => { cancelTimers(); openS.val = null; };

const scheduleClose = () => {
    clearTimeout(leaveTimer);
    leaveTimer = setTimeout(() => { if (openS.val && !openS.val.pinned) openS.val = null; }, 180);
};

// anchorVisible says whether the anchor is on screen and inside every
// scrolling or clipping ancestor, so a card whose row has scrolled out of
// its panel hides instead of floating over the toolbar or the tab strip.
const anchorVisible = (anchor) => {
    const rect = anchor.getBoundingClientRect();
    if (rect.bottom <= 0 || rect.top >= window.innerHeight || rect.height === 0) return false;
    for (let node = anchor.parentElement; node; node = node.parentElement) {
        const {overflowY} = getComputedStyle(node);
        if (overflowY !== "auto" && overflowY !== "scroll" && overflowY !== "hidden") continue;
        const box = node.getBoundingClientRect();
        if (rect.bottom <= box.top || rect.top >= box.bottom) return false;
    }
    return true;
};

// place puts the popover under its anchor, flipping above when the room below
// is short, and keeps it inside the viewport horizontally. The card's natural
// height is read from scrollHeight, so a card scrolled inside keeps its
// position, and a side is kept while the card still fits there, so a pinned
// card following a scrolling row does not jump across it.
const place = (el, open) => {
    const margin = 8;
    const rect = open.anchor.getBoundingClientRect();
    const width = Math.min(open.width, window.innerWidth - 2 * margin);
    el.style.width = `${width}px`;
    const left = Math.max(margin, Math.min(rect.left, window.innerWidth - width - margin));
    el.style.left = `${left}px`;
    const below = window.innerHeight - rect.bottom - margin - 6;
    const above = rect.top - margin - 6;
    const height = el.scrollHeight + el.offsetHeight - el.clientHeight;
    const fits = (side) => side === "below" ? height <= below : height <= above;
    const side = open.side && fits(open.side) ? open.side : (height <= below || below >= above) ? "below" : "above";
    open.side = side;
    if (side === "below") {
        el.style.top = `${rect.bottom + 6}px`;
        el.style.bottom = "";
        el.style.maxHeight = `${below}px`;
    } else {
        el.style.bottom = `${window.innerHeight - rect.top + 6}px`;
        el.style.top = "";
        el.style.maxHeight = `${above}px`;
    }
    el.style.visibility = anchorVisible(open.anchor) ? "" : "hidden";
};

const layer = () => {
    const open = openS.val;
    if (!open) { popoverEl = null; return ""; }
    const el = div({
        class: "app-scroll fixed z-40 flex flex-col overflow-y-auto overscroll-contain rounded-md border border-gray-600 bg-surface text-xs shadow-2xl shadow-black/60",
        role: "dialog",
        "aria-label": "Rule explanation",
        "data-testid": "rule-explainer",
        onmouseenter: () => clearTimeout(leaveTimer),
        onmouseleave: scheduleClose,
        onclick: (e) => e.stopPropagation(),
        // The wheel over a card scrolls the card, never the page under it:
        // a card with nothing left to scroll swallows the event.
        onwheel: (e) => {
            const room = el.scrollHeight - el.clientHeight;
            const atEnd = e.deltaY > 0 ? el.scrollTop >= room - 1 : el.scrollTop <= 0;
            if (room <= 0 || atEnd) e.preventDefault();
        },
    }, open.render(open));
    popoverEl = el;
    el.style.visibility = "hidden";
    requestAnimationFrame(() => place(el, open));
    return el;
};

// One layer for the whole app, appended to body once; the page that uses the
// explainer calls this when it mounts (repeat calls are no-ops).
let mounted = false;
export const mountExplainerLayer = () => {
    if (mounted) return;
    mounted = true;
    van.add(document.body, layer);
    document.addEventListener("keydown", (e) => {
        if (e.key !== "Escape" || !openS.val) return;
        closeExplainer();
        hoverArmed = false;
        hoverLock = lastMouse;
    });
    document.addEventListener("mousemove", (e) => {
        lastMouse = {x: e.clientX, y: e.clientY};
        if (hoverArmed || (Math.abs(e.clientX - hoverLock.x) <= 2 && Math.abs(e.clientY - hoverLock.y) <= 2)) return;
        hoverArmed = true;
        const anchor = e.target.closest?.("[data-explain]");
        if (anchor) hoverStarts.get(anchor)?.();
    }, {passive: true});
    document.addEventListener("mousedown", (e) => {
        const open = openS.val;
        if (!open) return;
        if (popoverEl?.contains(e.target) || open.anchor.contains(e.target)) return;
        closeExplainer();
    }, true);
    // A scroll moves the anchor: hover popovers close, pinned ones follow.
    // Scrolling inside the card itself moves nothing under it.
    document.addEventListener("scroll", (e) => {
        const open = openS.val;
        if (!open || popoverEl?.contains(e.target)) return;
        if (!open.pinned) { closeExplainer(); return; }
        if (popoverEl) place(popoverEl, open);
    }, true);
    window.addEventListener("resize", () => { if (openS.val && popoverEl) place(popoverEl, openS.val); });
    // A revoke or delete re-renders the row, so the anchor leaves the document
    // without a mouseleave: close rather than float over a chip that is gone.
    new MutationObserver(() => {
        if (openS.val && !openS.val.anchor.isConnected) closeExplainer();
    }).observe(document.body, {childList: true, subtree: true});
};

// --- trigger -----------------------------------------------------------------

// explainable(anchor, render, {width, label}) wires an element as the trigger
// of an explanation: hover opens it after a short delay, click pins it until
// Esc, a click elsewhere or a second click. `render(open)` builds the popover
// body; `width` is a number or a function of the anchor.
export const explainable = (anchor, render, {width, label = "Explain this rule"}) => {
    const widthOf = () => typeof width === "function" ? width(anchor) : width;
    const isOpenHere = () => openS.val?.anchor === anchor;
    const open = (pinned) => {
        cancelTimers();
        openS.val = {anchor, width: widthOf(), render, pinned};
    };

    anchor.setAttribute("tabindex", "0");
    anchor.setAttribute("role", "button");
    anchor.setAttribute("aria-haspopup", "dialog");
    anchor.setAttribute("aria-label", label);
    anchor.classList.add("cursor-pointer", "outline-none", "focus-visible:ring-1", "focus-visible:ring-blue-400/60");

    const startHover = () => {
        if (!hoverArmed || openS.val?.pinned) return;
        clearTimeout(leaveTimer);
        clearTimeout(hoverTimer);
        hoverTimer = setTimeout(() => { if (!openS.val?.pinned) open(false); }, 220);
    };
    anchor.addEventListener("mouseenter", startHover);
    anchor.setAttribute("data-explain", "");
    hoverStarts.set(anchor, startHover);
    anchor.addEventListener("mouseleave", () => {
        clearTimeout(hoverTimer);
        if (isOpenHere() && !openS.val.pinned) scheduleClose();
    });
    anchor.addEventListener("click", (e) => {
        e.stopPropagation();
        if (isOpenHere() && openS.val.pinned) { closeExplainer(); return; }
        open(true);
    });
    anchor.addEventListener("keydown", (e) => {
        if (e.key !== "Enter" && e.key !== " ") return;
        e.preventDefault();
        if (isOpenHere()) closeExplainer(); else open(true);
    });
    // Pinned state shows on the anchor so the reader knows which row the
    // pinned card belongs to.
    van.derive(() => {
        const here = isOpenHere();
        anchor.classList.toggle("explain-open", here);
        anchor.classList.toggle("explain-pinned", here && !!openS.val.pinned);
    });
    return anchor;
};

// --- shared pieces -----------------------------------------------------------

const argBadge = (arg) => span(
    {class: "self-start rounded border border-amber-500/30 bg-amber-500/10 px-1 py-px font-mono text-[10px] text-amber-300"},
    "${" + arg + "}");

const closeButton = () => button({
    type: "button",
    "aria-label": "Close",
    "data-testid": "explainer-close",
    class: "-mr-1 -mt-0.5 flex h-5 w-5 flex-none items-center justify-center rounded text-gray-500 hover:bg-white/10 hover:text-gray-100 cursor-pointer",
    onclick: closeExplainer,
}, closeIcon({class: "h-3.5 w-3.5"}));

// --- icons -------------------------------------------------------------------

const iconAttrs = (cls) => ({
    viewBox: "0 0 24 24", fill: "none", stroke: "currentColor", "stroke-width": "2.25",
    "stroke-linecap": "round", "stroke-linejoin": "round", class: cls, "aria-hidden": "true",
});
const personIcon = (cls) => svg(iconAttrs(cls), circle({cx: 12, cy: 8, r: 4}), path({d: "M20 21a8 8 0 0 0-16 0"}));
const agentIcon = (cls) => svg(iconAttrs(cls),
    path({d: "M12 8V4H8"}), rect({width: 16, height: 12, x: 4, y: 8, rx: 2}),
    path({d: "M2 14h2"}), path({d: "M20 14h2"}), path({d: "M15 13v2"}), path({d: "M9 13v2"}));

const ICON = "h-3.5 w-3.5";
const pair = (cls) => span({class: "inline-flex items-center gap-0.5"}, personIcon(`${ICON} ${cls}`), agentIcon(`${ICON} ${cls}`));

// --- the table ---------------------------------------------------------------

// iconGlyph draws who a cell reaches: the person, the person and an agent,
// an agent alone (an agent-only deny), or nothing.
const iconGlyph = (cell, deny) => {
    const tone = deny ? "text-red-400" : "text-green-400";
    if (cell.state === "both") return pair(tone);
    if (cell.state === "user") return personIcon(`${ICON} ${tone}`);
    if (cell.state === "agents") return agentIcon(`${ICON} ${tone}`);
    if (cell.state === "arg") return span({class: "text-amber-300"}, "?");
    if (cell.state === "none") return span({class: "text-gray-700"}, "·");
    return "";
};

// cellState phrases a cell's state for the readout line.
const cellState = (cell, deny) => {
    const scope = cell.scope ? `, ${cell.scope}` : "";
    if (cell.state === "both") return `${deny ? "Blocked" : "Allowed"} in person & agent sessions${scope}.`;
    if (cell.state === "user") return `Allowed in person sessions${scope}${cell.agentsArg ? `; agent sessions: ${cell.agentsArg}` : ""}.`;
    if (cell.state === "agents") return `Blocked in agent sessions${scope}.`;
    if (cell.state === "none") return "Not affected.";
    if (cell.state === "na") return `Never applies here: ${cell.title}.`;
    return cell.title || "";
};

// readout is the line under the table that follows the pointer: the hovered
// pair with what the permission grants and the cell's state, or the gloss
// of a hovered row or column header. It reserves two lines so the legend
// and key below never move.
const readout = (hover, merged, deny) => p(
    {class: "min-h-[2.75em] text-[10px] leading-snug text-gray-400 line-clamp-2", "data-testid": "cell-readout"},
    () => {
        const h = hover.val;
        if (!h) return span({class: "text-gray-600"}, "Hover a cell for what it grants.");
        const label = (text) => span({class: "font-mono text-gray-200"}, text);
        if (h.verb && h.type) {
            const gloss = PAIR_GLOSS[`${h.type}:${h.verb}`];
            const c = merged.cells[`${h.type}:${h.verb}`];
            return span(label(`${h.verb} ${h.type}`), ": ", gloss ? `${gloss}. ` : "", cellState(c, deny));
        }
        if (h.type) {
            // The gloss adds nothing when it is only the plural; where the
            // check happens is the useful part of a column.
            const gloss = ENTITY_GLOSS[h.type] === `${h.type}s` ? "" : `${ENTITY_GLOSS[h.type]}; `;
            const where = CLUSTER_LEVEL.has(h.type) ? "checked in the System space" : "checked in the item's own space";
            return span(label(h.type), ": ", gloss, where);
        }
        return span(label(h.verb), ": ", VERB_GLOSS[h.verb]);
    },
);

// iconTable is one table for a tab: the tab's resource types across the top,
// its actions down the left, one cell each, with the readout under it. The
// hovered cell's row and column headers light up.
const iconTable = (grid, deny) => {
    const merged = mergeGrid(grid);
    const hover = van.state(null);
    const enter = (h) => () => { hover.val = h; };
    const cell = (c, verb, type) => td({
        class: () => {
            const on = hover.val?.verb === verb && hover.val?.type === type;
            const fill = c.state === "na" ? (on ? "bg-black/20" : "bg-black/30") : (on ? "bg-white/5" : "");
            return `h-7 border border-gray-700/70 text-center text-[12px] leading-none ${fill}`;
        },
        onmouseenter: enter({verb, type}),
        "data-cell": `${type}:${verb}`,
        "data-state": c.state,
    }, span({class: "inline-flex items-center justify-center gap-px align-middle"},
        iconGlyph(c, deny),
        c.markers?.length ? sup({class: "ml-px text-[8px] text-gray-400"}, c.markers.join(" ")) : ""));
    return div({class: "flex flex-col gap-1"},
        table(
            {class: "w-full table-fixed border-collapse", "data-testid": "matrix-table", onmouseleave: () => { hover.val = null; }},
            thead(tr(th({class: "w-[5.5rem]"}), ...grid.types.map((t) => th({
                class: () => `px-1 py-1 text-center font-mono text-[10px] font-normal ${hover.val?.type === t.name ? "text-gray-100" : "text-gray-400"}`,
                onmouseenter: enter({type: t.name}),
            }, ENTITY_SHORT[t.name])))),
            tbody(...grid.verbs.map((v) => tr(
                th({
                    class: () => `py-0.5 pl-1 pr-2 text-right font-mono text-[10px] font-normal ${hover.val?.verb === v.name ? "text-gray-100" : "text-gray-300"}`,
                    onmouseenter: enter({verb: v.name}),
                }, VERB_SHORT[v.name]),
                ...grid.types.map((t) => cell(merged.cells[`${t.name}:${v.name}`], v.name, t.name)),
            ))),
        ),
        readout(hover, merged, deny));
};

const legend = (grid) => !grid.legend.length ? "" : div(
    {class: "flex flex-col gap-0.5 pt-1"},
    ...grid.legend.map((l) => p({class: "text-[10px] leading-snug text-gray-400"}, sup({class: "mr-0.5 text-[8px]"}, String(l.marker)), l.text)),
);

// key lists what the glyphs mean, one per line. The person and agent icons
// are independent: a cell shows whichever apply, side by side.
const keyRow = (glyph, text) => div({class: "flex items-center gap-1.5"}, span({class: "inline-flex w-4 justify-center"}, glyph), text);
const key = (deny, hasArg) => div(
    {class: "flex flex-col gap-0.5 px-3 pb-2 text-[10px] text-gray-500", "data-testid": "explainer-key"},
    keyRow(personIcon(`${ICON} ${deny ? "text-red-400" : "text-green-400"}`), deny ? "Blocked in person sessions" : "Allowed in person sessions"),
    keyRow(agentIcon(`${ICON} ${deny ? "text-red-400" : "text-green-400"}`), deny ? "Blocked in agent sessions" : "Allowed in agent sessions"),
    keyRow(span({class: "text-gray-700"}, "·"), "Not affected"),
    keyRow(span({class: "inline-block h-2.5 w-2.5 rounded-sm border border-gray-800 bg-black/40"}), "Never applies here"),
    hasArg ? keyRow(span({class: "text-amber-300"}, "?"), "Depends on an argument") : "",
);

// --- definition pane ---------------------------------------------------------

// The left pane shows the rule as written, one section per position: the
// effect, then every value in the position's universe as a token (covered or
// not; an explicit exclusion reads the same as a value never selected) or
// the open argument, the instance ids, and the sessions the rule reaches as
// two badges, Users and Agents. A role or a role grant gets one tab per rule;
// a grant's rules show the bound values.
const TOKEN_CLASS = {
    on: "border-blue-500/40 bg-blue-500/15 text-blue-200",
    off: "border-gray-800 text-gray-600",
};

const tokens = (list) => !list ? "" : div(
    {class: "flex flex-wrap gap-0.5"},
    ...list.map((t) => span({
        class: `rounded border px-1 py-px font-mono text-[10px] leading-4 ${TOKEN_CLASS[t.state === "on" ? "on" : "off"]}`,
        title: t.state === "on" ? `${t.name}: covered` : `${t.name}: not covered`,
    }, t.name)),
);

const section = (key, label, ...body) => div(
    {class: "flex flex-col gap-1", "data-testid": `definition-${key}`},
    span({class: "text-[10px] font-semibold uppercase tracking-wider text-gray-500"}, label),
    ...body,
);

const effectBadge = (deny) => span({
    class: `self-start rounded border px-1 py-px font-mono text-[10px] leading-4 ${deny
        ? "border-red-500/40 bg-red-500/10 text-red-300" : "border-green-500/40 bg-green-500/10 text-green-300"}`,
}, deny ? "deny" : "allow");

// A selector position: its universe as tokens, or the open argument.
const selectorSection = (key, label, res) => section(key, label, res.mode === "arg" ? argBadge(res.arg.name) : tokens(res.tokens));

// The instance position has no universe: any item, the listed ids, any
// except the listed ids, the open argument, or none.
const instanceTokens = (refs) => {
    const id = (i, state) => ({name: `#${i.id}`, state});
    if (refs.mode === "all") return [{name: "any", state: "on"}];
    if (refs.mode === "allExcept") return [{name: "any", state: "on"}, ...refs.exclude.map((i) => id(i, "excluded"))];
    if (refs.mode === "list") return refs.include.map((i) => id(i, "on"));
    return [{name: "none", state: "off"}];
};
const instancesSection = (refs) => section("instances", "Instances", refs.mode === "arg" ? argBadge(refs.arg.name) : tokens(instanceTokens(refs)));

const sessionsSection = (def) => section("sessions", "Sessions",
    tokens([{name: "Users", state: def.users ? "on" : "off"}, {name: "Agents", state: def.agents ? "on" : "off"}]));

const ruleTabs = (count, active) => div(
    {class: "flex flex-wrap items-end gap-0.5 border-b border-gray-800 px-2 pt-1", role: "tablist", "aria-label": "Rules"},
    ...Array.from({length: count}, (_, i) => button({
        type: "button",
        role: "tab",
        "aria-selected": () => String(active.val === i),
        "data-testid": `rule-tab-${i + 1}`,
        class: () => `-mb-px border-b-2 px-2 py-1 text-[11px] cursor-pointer transition-colors ${active.val === i
            ? "border-brand text-gray-100" : "border-transparent text-gray-400 hover:text-gray-200"}`,
        onclick: (e) => { e.stopPropagation(); active.val = i; },
    }, `Rule ${i + 1}`)),
);

const DEFINITION_WIDTH = 248;

// paneTitle heads each half of the card so the split reads as two things:
// the rule's definition and the access it produces.
const paneTitle = (text) => div(
    {class: "flex-none border-b border-gray-800 bg-gray-950/50 px-3 py-1 text-[10px] font-semibold uppercase tracking-wider text-gray-400"},
    text);

const definitionPane = (subject) => {
    const active = van.state(0);
    const define = (rule) => ruleDefinition(rule, {spaceNames: subject.spaceNames, spaces: subject.spaces, argNames: subject.argNames, bindings: subject.bindings});
    return div(
        {class: "flex flex-none flex-col border-r border-gray-700 bg-gray-950/25", style: `width:${DEFINITION_WIDTH}px`, "data-testid": "rule-definition"},
        paneTitle("Definition"),
        subject.rules.length > 1 ? ruleTabs(subject.rules.length, active) : "",
        () => {
            const rule = subject.rules[active.val];
            if (!rule) return p({class: "px-3 py-3 text-[11px] text-gray-400"}, "This role has no rules.");
            const def = define(rule);
            return div({class: "flex flex-col gap-2.5 px-3 py-2"},
                section("effect", "Effect", effectBadge(def.deny)),
                selectorSection("actions", "Actions", def.actions),
                selectorSection("resources", "Resources", def.types),
                instancesSection(def.refs),
                selectorSection("spaces", "Spaces", def.spaces),
                sessionsSection(def));
        },
    );
};

// --- space tabs --------------------------------------------------------------

// spaceTabs is the strip inside the popover: System first when the rules
// reach it, then one tab per group of spaces with the same grid; spaces the
// rules never reach get no tab.
// leadLine renders a tab's lead sentence with its argument token in the
// argument colour, the rest in plain text.
const leadLine = (tab) => {
    const token = "${" + tab.leadArg + "}";
    const [before, after] = tab.leadArg ? tab.lead.split(token) : [tab.lead, undefined];
    return p({class: "text-[10px] leading-snug text-gray-300", "data-testid": "tab-lead"},
        before, after === undefined ? "" : span({class: "font-mono text-amber-300"}, token), after ?? "");
};

const spaceTabs = (m, active) => div(
    {class: "flex flex-wrap items-end gap-0.5 border-b border-gray-800 px-2 pt-1", role: "tablist", "aria-label": "Spaces"},
    ...m.tabs.map((t) => button({
        type: "button",
        role: "tab",
        "aria-selected": () => String(active.val === t.key),
        "data-testid": `matrix-tab-${t.key}`,
        title: t.lead ? `${t.lead} ${t.subtitle}` : t.subtitle,
        class: () => `-mb-px border-b-2 px-2 py-1 text-[11px] cursor-pointer transition-colors ${t.key === "arg" ? "font-mono" : ""} ${active.val === t.key
            ? (t.system ? "border-amber-400/70 text-gray-100" : "border-brand text-gray-100")
            : "border-transparent text-gray-400 hover:text-gray-200"}`,
        onclick: (e) => { e.stopPropagation(); active.val = t.key; },
    }, t.label)),
);

// --- body --------------------------------------------------------------------

// renderExplainer builds the popover body for a subject: a one-line header
// naming the subject, the definition pane, and the space tabs over the
// active tab's table, legend and key.
export const renderExplainer = (subject, open) => {
    const m = explainMatrix(subject.rules, subject);
    const active = van.state(m.tabs[0]?.key);
    const current = () => m.tabs.find((t) => t.key === active.val) || m.tabs[0];
    const hasArg = m.tabs.some((t) => Object.values(mergeGrid(t.grid).cells).some((c) => c.state === "arg"));
    return div(
        {class: "flex flex-col"},
        div({class: "flex items-center gap-3 border-b border-gray-800 px-3 py-1.5"},
            p({class: "min-w-0 flex-1 truncate leading-snug text-gray-200", "data-testid": "explainer-title"}, subject.subtitle || ""),
            closeButton()),
        div({class: "flex min-h-0"},
            definitionPane(subject),
            div({class: "flex min-w-0 flex-1 flex-col", "data-testid": "access-table"},
                // A role or grant with several rules: the table is their union.
                paneTitle(subject.rules.length > 1 ? "Access table (All rules)" : "Access table"),
                m.tabs.length ? spaceTabs(m, active) : "",
                !m.tabs.length
                    ? p({class: "px-3 py-3 text-[11px] text-gray-400"}, m.deny ? "This rule blocks nothing in any space." : "This rule allows nothing in any space.")
                    : () => {
                        const tab = current();
                        return div({class: "flex flex-col gap-1 px-3 pb-2 pt-1.5"},
                            tab.lead ? leadLine(tab) : "",
                            tab.system ? "" : p({class: "text-[10px] leading-snug text-gray-500", "data-testid": "tab-subtitle"}, tab.subtitle),
                            iconTable(tab.grid, m.deny),
                            legend(tab.grid));
                    },
                m.tabs.length ? key(m.deny, hasArg) : "",
                // The System tabs describe the space under the key rather than
                // above the table, where the lead line (if any) stays.
                () => current()?.system
                    ? p({class: "border-t border-gray-800 px-3 py-2 text-[10px] leading-snug text-gray-500", "data-testid": "system-note"}, current().subtitle)
                    : "")),
    );
};

// explainerWidth sizes the popover: the definition pane plus a matrix as wide
// as its widest tab, so a card with only the cluster-level columns is
// narrower than one with the space-scoped ones.
export const explainerWidth = (subject) => {
    const m = explainMatrix(subject.rules, subject);
    const cols = Math.max(1, ...m.tabs.map((t) => t.grid.types.length));
    return DEFINITION_WIDTH + Math.max(440, 26 + 88 + cols * 68);
};
