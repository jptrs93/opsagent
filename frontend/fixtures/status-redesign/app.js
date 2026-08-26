import van from "vanjs-core";
import {formatDateTime, formatHistoryTime} from "/src/lib/date.js";
import {
    caretRightIcon, checkIcon, chevronDownIcon, closeIcon, copyIcon, editIcon,
    plusIcon, searchIcon,
} from "/src/lib/icons.js";
import {spaceHue} from "/src/lib/valueExplorer.js";
import {deployments, historyFor, spaces} from "./mockData.js";

const {button, col, colgroup, div, h1, input, label: labelTag, p, span, table, tbody, td, th, thead, tr} = van.tags;

// ---------------------------------------------------------------------------
// Fixture state
// ---------------------------------------------------------------------------

const SYSTEM_SPACE_ID = 0;

const selectedId = van.state(null);
const inspectorTab = van.state("details");
const search = van.state("");
// History shows only config rows by default; status rows are opt-in.
const showStatusRows = van.state(false);
// Spaces filter: ids currently hidden. _system starts deselected, matching
// "all visible spaces except _system are selected".
const hiddenSpaces = van.state(new Set([SYSTEM_SPACE_ID]));
const collapsedSpaces = van.state(new Set());
const openMenu = van.state(null); // "spaces" | null

const select = (id) => {
    if (selectedId.val !== id) inspectorTab.val = "details";
    selectedId.val = id;
};

const toggleCollapsed = (spaceId) => {
    const next = new Set(collapsedSpaces.val);
    next.has(spaceId) ? next.delete(spaceId) : next.add(spaceId);
    collapsedSpaces.val = next;
};

const visibleSpaces = () => spaces.filter((s) => !hiddenSpaces.val.has(s.id));
const spacesDirty = () => !(hiddenSpaces.val.size === 1 && hiddenSpaces.val.has(SYSTEM_SPACE_ID));

// ---------------------------------------------------------------------------
// Shared vocabulary
// ---------------------------------------------------------------------------

const STATUS_ORDER = ["running", "starting", "preparing", "stopped", "crashed", "prepare failed"];
const STATUS_META = {
    "running": {dot: "bg-green-500", text: "text-green-300"},
    "starting": {dot: "bg-yellow-500", text: "text-yellow-300"},
    "preparing": {dot: "bg-blue-500", text: "text-blue-300"},
    "stopped": {dot: "bg-gray-500", text: "text-gray-400"},
    "crashed": {dot: "bg-red-500", text: "text-red-300"},
    "prepare failed": {dot: "bg-red-500", text: "text-red-300"},
};
const statusMeta = (key) => STATUS_META[key] || STATUS_META.stopped;

const PREPARE_TEXT = {ready: "text-green-300", progress: "text-blue-300", failed: "text-red-300"};

const shortVersion = (v) => (v.length > 7 && /^[0-9a-f]+$/i.test(v) ? v.slice(0, 7) : v);
const spaceName = (id) => spaces.find((s) => s.id === id)?.name || `space ${id}`;

const spaceDot = (spaceId) => span({
    class: "inline-block w-[7px] h-[7px] rounded-full flex-none",
    style: `background:${spaceHue(spaceId)}`,
});

// groupBy keeps first-seen order; the status variant re-sorts by severity
// vocabulary so "2 running · 1 crashed" always reads in the same order.
const groupBy = (items, keyFn) => {
    const out = [];
    const index = new Map();
    for (const item of items) {
        const key = keyFn(item);
        if (!index.has(key)) {
            index.set(key, out.length);
            out.push({key, count: 0, first: item});
        }
        out[index.get(key)].count++;
    }
    return out;
};

const statusGroups = (deployment) => groupBy(deployment.instances, (i) => i.status)
    .sort((a, b) => STATUS_ORDER.indexOf(a.key) - STATUS_ORDER.indexOf(b.key));
const versionGroups = (deployment) => groupBy(deployment.instances, (i) => i.version);
const prepareGroups = (deployment) => groupBy(deployment.instances, (i) => i.prepare.label);

const distinctNodes = (deployment) => [...new Set(deployment.instances.map((i) => i.node))];

// ---------------------------------------------------------------------------
// Aggregated cell treatments. Each cell tries the one-line rendering first
// ("2 running · 1 crashed") and falls back to one sub-line per group when the
// estimate says the line would truncate in its fixed column. Estimates use
// average glyph widths for the 13px UI face / mono face rather than measuring
// DOM — the columns are fixed, so this stays deterministic.
// ---------------------------------------------------------------------------

const UI_CHAR_PX = 6.7;
const MONO_CHAR_PX = 6.4;
const SEP_PX = 12;  // middot plus its two gaps
const DOT_PX = 10;  // status dot plus its gap
const CELL_PAD_PX = 22;  // px-2 padding plus a small truncation buffer

// Columns are user-resizable; widths live in state so the one-line/stacked
// estimate follows the live column width — narrowing Status flips its rows
// to stacked, widening flips them back. The widths here are minimum-content
// baselines; on first layout any free container width is distributed evenly
// on top of them (see distributeFreeWidth).
const COLUMN_DEFS = [
    {key: "deployment", label: "Deployment", width: 160, min: 100},
    {key: "nodes", label: "Nodes", width: 120, min: 70},
    {key: "status", label: "Status", width: 200, min: 90},
    {key: "version", label: "Version", width: 192, min: 90},
    {key: "prepare", label: "Prepare", width: 176, min: 90},
    {key: "restarts", label: "Restarts", width: 128, min: 60},
    {key: "deployedBy", label: "Deployed by", width: 96, min: 60},
    {key: "deployedAt", label: "Deployed at", width: 152, min: 90},
    {key: "edit", label: "", width: 42, min: 42},
];
const colWidths = van.state(Object.fromEntries(COLUMN_DEFS.map((c) => [c.key, c.width])));

// rawVal: callers run inside the deploymentTable binding, which registers the
// colWidths dependency itself with one explicit read.
const usablePx = (key) => (colWidths.rawVal[key] ?? 0) - CELL_PAD_PX;

const oneLineFits = (labels, charPx, perGroupPx, usablePx) =>
    labels.reduce((w, l) => w + l.length * charPx + perGroupPx, 0)
        + (labels.length - 1) * SEP_PX <= usablePx;

const countLabel = (group, total) => (total > 1 ? `${group.count} ${group.key}` : group.key);

const oneLineCell = (parts, title) => div(
    {class: "flex items-center gap-1 overflow-hidden whitespace-nowrap", title},
    ...parts.flatMap((part, i) => [i > 0 ? span({class: "text-gray-600"}, "·") : "", ...part]));

const stackedCell = (lines) => div({class: "flex flex-col gap-px py-px"}, ...lines);

const statusCell = (deployment) => {
    const groups = statusGroups(deployment);
    const total = deployment.instances.length;
    const labels = groups.map((g) => countLabel(g, total));
    const dot = (g) => span({class: `inline-block w-1.5 h-1.5 rounded-full flex-none ${statusMeta(g.key).dot}`});
    if (oneLineFits(labels, UI_CHAR_PX, DOT_PX, usablePx("status"))) {
        return oneLineCell(
            groups.map((g, i) => [dot(g), span({class: `truncate ${statusMeta(g.key).text}`}, labels[i])]),
            labels.join(" · "));
    }
    return stackedCell(groups.map((g, i) => span({class: `flex items-center gap-1.5 ${statusMeta(g.key).text}`},
        dot(g), labels[i])));
};

const versionTone = (deployment, version) =>
    version === deployment.desiredVersion ? "text-gray-300" : "text-orange-400";

const versionCell = (deployment) => {
    const groups = versionGroups(deployment);
    const total = deployment.instances.length;
    const labels = groups.map((g) => (total > 1 ? `${shortVersion(g.key)} ×${g.count}` : shortVersion(g.key)));
    const line = (g, i) => span(
        {class: `truncate font-mono ${versionTone(deployment, g.key)}`, title: g.key}, labels[i]);
    if (oneLineFits(labels, MONO_CHAR_PX, 0, usablePx("version"))) {
        return oneLineCell(groups.map((g, i) => [line(g, i)]), labels.join(" · "));
    }
    return stackedCell(groups.map(line));
};

const prepareCell = (deployment) => {
    const groups = prepareGroups(deployment);
    const total = deployment.instances.length;
    const labels = groups.map((g) => countLabel(g, total));
    const line = (g, i) => span({class: `truncate ${PREPARE_TEXT[g.first.prepare.tone]}`}, labels[i]);
    if (oneLineFits(labels, UI_CHAR_PX, 0, usablePx("prepare"))) {
        return oneLineCell(groups.map((g, i) => [line(g, i)]), labels.join(" · "));
    }
    return stackedCell(groups.map(line));
};

const nodesCell = (deployment) => {
    const nodes = distinctNodes(deployment);
    return nodes.length === 1
        ? span({class: "truncate text-gray-300"}, nodes[0])
        : span({class: "truncate text-gray-300", title: nodes.join(", ")}, `${nodes.length} nodes`);
};

// ---------------------------------------------------------------------------
// Main table
// ---------------------------------------------------------------------------

const thBase = "sticky top-0 z-[1] bg-surface px-2 py-1.5 text-left text-[10.5px] font-semibold uppercase " +
    "tracking-wider whitespace-nowrap text-gray-500 shadow-[inset_0_-1px_0_#374151]";
const tdBase = "border-b border-gray-800/60 border-r border-r-gray-800/30 last:border-r-0 px-2 py-[3px] " +
    "align-middle overflow-hidden text-[13px]";

const columns = () => colgroup(...COLUMN_DEFS.map((c) =>
    col({"data-col": c.key, style: `width:${colWidths.rawVal[c.key]}px`})));

// Same drag/keyboard resize mechanics as the explorer pages: the drag writes
// straight to the col element, state (and the adaptive cells) update on mouseup.
const startColResize = (event, colKey, min) => {
    event.preventDefault();
    event.stopPropagation();
    const tableEl = event.target.closest("table");
    const colEl = tableEl?.querySelector(`col[data-col="${colKey}"]`);
    const startX = event.clientX;
    const startW = colWidths.rawVal[colKey];
    const startTotal = COLUMN_DEFS.reduce((sum, c) => sum + colWidths.rawVal[c.key], 0);
    let width = startW;
    const move = (ev) => {
        width = Math.max(min, startW + (ev.clientX - startX));
        if (colEl) colEl.style.width = `${width}px`;
        // Grow/shrink the table with the column so the drag doesn't squeeze
        // the neighbours; the refit on mouseup reflows them deliberately.
        if (tableEl) tableEl.style.width = `${startTotal + (width - startW)}px`;
    };
    const up = () => {
        document.removeEventListener("mousemove", move);
        document.removeEventListener("mouseup", up);
        document.body.classList.remove("resizing");
        colWidths.val = {...colWidths.rawVal, [colKey]: width};
        fitColumns(tableScroll.clientWidth, colKey);
    };
    document.addEventListener("mousemove", move);
    document.addEventListener("mouseup", up);
    document.body.classList.add("resizing");
};

const nudgeColWidth = (event, colKey, min) => {
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
    event.preventDefault();
    const step = event.shiftKey ? 48 : 16;
    const current = colWidths.rawVal[colKey];
    colWidths.val = {...colWidths.rawVal, [colKey]: Math.max(min, current + (event.key === "ArrowRight" ? step : -step))};
    fitColumns(tableScroll.clientWidth, colKey);
};

const headerCell = (c, isLast) => {
    const grip = isLast ? "" : span({
        class: "colgrip",
        tabindex: "0",
        role: "separator",
        "aria-orientation": "vertical",
        "aria-label": `Resize ${c.label || "actions"} column`,
        onclick: (e) => e.stopPropagation(),
        onmousedown: (e) => startColResize(e, c.key, c.min),
        onkeydown: (e) => nudgeColWidth(e, c.key, c.min),
    });
    return th({class: thBase}, c.label, grip);
};

const restartsCell = (deployment) => {
    const total = deployment.instances.reduce((n, i) => n + i.restarts, 0);
    const last = deployment.instances
        .map((i) => i.lastRestartAt)
        .filter(Boolean)
        .sort((a, b) => b - a)[0];
    return div({class: "flex items-baseline gap-1.5 overflow-hidden whitespace-nowrap"},
        span({class: total > 0 ? "text-gray-300" : "text-gray-500"}, String(total)),
        last ? span({class: "truncate text-[11px] text-gray-500"}, formatDateTime(last, "")) : "");
};

const deploymentRow = (deployment) => tr(
    {
        class: () => `cursor-default ${selectedId.val === deployment.id ? "bg-brand/15" : "hover:bg-gray-700/35"}`,
        onclick: () => select(deployment.id),
        "data-testid": `deployment-row-${deployment.name}-${deployment.spaceId}`,
    },
    td({class: `${tdBase} whitespace-nowrap`},
        span({class: "truncate font-medium text-white"}, deployment.name)),
    td({class: `${tdBase} whitespace-nowrap`}, nodesCell(deployment)),
    td({class: tdBase}, statusCell(deployment)),
    td({class: tdBase}, versionCell(deployment)),
    td({class: tdBase}, prepareCell(deployment)),
    td({class: tdBase}, restartsCell(deployment)),
    td({class: `${tdBase} whitespace-nowrap text-gray-400`}, deployment.deployedBy),
    td({class: `${tdBase} whitespace-nowrap text-gray-500`, title: formatDateTime(deployment.deployedAt, "")},
        formatDateTime(deployment.deployedAt, "-")),
    td({class: `${tdBase} px-1 text-right whitespace-nowrap`},
        button({
            type: "button",
            title: `Edit deployment ${deployment.name}`,
            "aria-label": `Edit deployment ${deployment.name}`,
            class: () => "inline-flex h-6 w-6 items-center justify-center rounded text-gray-500 hover:text-gray-100 " +
                "hover:bg-white/10 transition-opacity cursor-pointer " +
                (selectedId.val === deployment.id ? "opacity-100" : "opacity-40 hover:opacity-100"),
            onclick: (e) => {
                e.stopPropagation();
                select(deployment.id);
                // Real page: opens the deploy/update overlay.
            },
        }, editIcon({class: "w-3.5 h-3.5"}))),
);

const spaceBandRow = (space, count, collapsed) => tr(
    {class: "cursor-default bg-gray-950/30 hover:bg-gray-700/35", onclick: () => toggleCollapsed(space.id)},
    td({class: "border-b border-gray-800/80 px-2 py-1", colSpan: 9},
        span({class: "flex items-center gap-1.5 font-mono text-[13px]"},
            button({
                type: "button",
                "aria-label": collapsed ? `Expand ${space.name}` : `Collapse ${space.name}`,
                class: "flex h-4 w-4 flex-none items-center justify-center rounded-sm text-gray-500 hover:text-gray-100 hover:bg-white/10 cursor-pointer",
                onclick: (e) => { e.stopPropagation(); toggleCollapsed(space.id); },
            }, caretRightIcon({class: `w-[11px] h-[11px] transition-transform ${collapsed ? "" : "rotate-90"}`})),
            spaceDot(space.id),
            span({class: "font-semibold text-gray-100"}, space.name),
            span({class: "text-[10.5px] text-gray-500"}, String(count)))),
);

const matchesSearch = (deployment) => {
    const query = search.val.trim().toLowerCase();
    if (!query) return true;
    const values = [
        deployment.name, spaceName(deployment.spaceId), deployment.repo,
        ...deployment.instances.flatMap((i) => [i.node, i.version, i.status]),
    ];
    return values.some((value) => String(value).toLowerCase().includes(query));
};

const deploymentTable = () => {
    const shown = visibleSpaces();
    if (!shown.length) {
        return div({class: "p-6 text-sm text-gray-500"}, "No spaces shown. Add one from the Spaces filter.");
    }
    const rows = shown.flatMap((space) => {
        const members = deployments.filter((d) => d.spaceId === space.id && matchesSearch(d));
        if (!members.length) return [];
        const collapsed = collapsedSpaces.val.has(space.id);
        return [
            spaceBandRow(space, members.length, collapsed),
            ...(collapsed ? [] : members.map(deploymentRow)),
        ];
    });
    if (!rows.length) {
        return div({class: "p-6 text-sm text-gray-500"}, "No deployments match your search.");
    }
    // Explicit table width = sum of column widths, so resizing one column
    // never redistributes into the others; the container scrolls instead.
    const widths = colWidths.val;
    const totalWidth = COLUMN_DEFS.reduce((sum, c) => sum + widths[c.key], 0);
    return table(
        {class: "table-fixed border-separate border-spacing-0 text-sm", style: `width:${totalWidth}px`},
        columns(),
        thead(tr(...COLUMN_DEFS.map((c, i) => headerCell(c, i === COLUMN_DEFS.length - 1)))),
        tbody(...rows),
    );
};

// fitColumns spreads the container's free width (positive or negative) evenly
// across every column except the edit-icon gutter, clamping at each column's
// min — below the min floor the table scrolls horizontally instead. keepKey
// pins a just-resized column at its user-chosen width so only the others
// absorb the delta. Runs on every container size change (window resize,
// inspector open/close/drag) and after each column resize.
const fitColumns = (avail, keepKey = null) => {
    if (avail < 200) return; // container not laid out yet
    const widths = {...colWidths.rawVal};
    const total = () => COLUMN_DEFS.reduce((sum, c) => sum + widths[c.key], 0);
    let growable = COLUMN_DEFS.filter((c) => c.key !== "edit" && c.key !== keepKey);
    // Multiple passes because min-clamped columns hand their share back.
    for (let pass = 0; pass < 3; pass++) {
        const free = avail - total();
        if (!growable.length || Math.abs(free) < 1) break;
        const share = free / growable.length;
        for (const c of growable) widths[c.key] = Math.max(c.min, Math.round(widths[c.key] + share));
        if (free < 0) growable = growable.filter((c) => widths[c.key] > c.min);
    }
    // Rounding remainder goes to the first growable column (deployment).
    const rem = avail - total();
    const catchAll = growable[0];
    if (catchAll && rem !== 0) widths[catchAll.key] = Math.max(catchAll.min, widths[catchAll.key] + rem);
    if (COLUMN_DEFS.some((c) => widths[c.key] !== colWidths.rawVal[c.key])) colWidths.val = widths;
};

// ---------------------------------------------------------------------------
// Toolbar with the explorer pages' spaces multi-select
// ---------------------------------------------------------------------------

const filterButton = ({menu, dirty, label, ariaLabel}) => button({
    type: "button",
    "aria-haspopup": "true",
    "aria-expanded": () => String(openMenu.val === menu),
    "aria-label": ariaLabel,
    class: () => `inline-flex items-center gap-1.5 rounded px-2 py-1.5 text-xs cursor-pointer border transition-colors ` +
        (dirty() ? "text-gray-100 border-brand" : "text-gray-400 border-gray-600 hover:bg-surface-hover hover:text-gray-100"),
    onclick: (e) => {
        e.stopPropagation();
        openMenu.val = openMenu.val === menu ? null : menu;
    },
}, () => span({class: "inline-flex items-center gap-1.5"}, ...label()));

const menuShell = (...children) => div({
    class: "absolute top-full left-0 z-30 mt-1.5 min-w-52 rounded-md border border-gray-600 bg-surface p-1 shadow-2xl flex flex-col",
    onclick: (e) => e.stopPropagation(),
}, ...children);

const menuRow = (onclick, ...children) => button({
    type: "button",
    class: "flex items-center gap-2 rounded px-2 py-1.5 text-left text-xs text-gray-200 hover:bg-surface-hover cursor-pointer",
    onclick,
}, ...children);

const menuCheck = (on) => checkIcon({class: `w-3.5 h-3.5 flex-none text-brand ${on ? "" : "invisible"}`});

const toggleSpace = (id) => {
    const next = new Set(hiddenSpaces.val);
    next.has(id) ? next.delete(id) : next.add(id);
    hiddenSpaces.val = next;
};

const spacesMenu = () => menuShell(
    ...spaces.map((space) => menuRow(
        () => toggleSpace(space.id),
        menuCheck(!hiddenSpaces.val.has(space.id)),
        spaceDot(space.id),
        span({class: "font-mono"}, space.name),
    )),
    ...(spacesDirty() ? [
        div({class: "my-1 border-t border-gray-700"}),
        menuRow(() => { hiddenSpaces.val = new Set([SYSTEM_SPACE_ID]); },
            closeIcon({class: "w-3.5 h-3.5 flex-none text-brand"}), "Reset to default"),
    ] : []),
);

const toolbar = () => div(
    {class: "flex flex-none flex-wrap items-center gap-2 border-b border-gray-700 px-2 py-2"},
    div({class: "relative"},
        searchIcon({class: "pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-gray-500"}),
        input({
            class: "text-input search-input search-input-iconed",
            type: "search",
            placeholder: "Search deployments",
            "aria-label": "Search deployments",
            // Match the spaces filter button's height (text-xs + py-1.5 +
            // borders = 30px) so their top and bottom borders align. Inline
            // because .text-input/.search-input padding is un-layered and
            // beats padding utilities.
            style: "height:30px;padding-top:0;padding-bottom:0;",
            value: search,
            oninput: (e) => { search.val = e.target.value; },
        })),
    span({class: "relative inline-flex"},
        filterButton({
            menu: "spaces",
            dirty: spacesDirty,
            ariaLabel: "Filter spaces",
            label: () => [
                span({class: "inline-flex items-center gap-1"}, ...visibleSpaces().map((s) => spaceDot(s.id))),
                `${visibleSpaces().length} space${visibleSpaces().length === 1 ? "" : "s"}`,
                chevronDownIcon({class: "w-3 h-3"}),
            ],
        }),
        () => openMenu.val === "spaces" ? spacesMenu() : ""),
    div({class: "flex-1"}),
    // Outlined like the assets page's "New folder" button, at text-xs so the
    // height lands on the search/filter row's 30px.
    button({
        type: "button",
        class: "flex items-center gap-1.5 whitespace-nowrap rounded-lg border border-gray-600 px-3 py-1.5 text-xs text-gray-300 hover:bg-surface-hover transition-colors cursor-pointer",
    }, "Export"),
    button({
        type: "button",
        class: "flex items-center gap-1.5 whitespace-nowrap rounded-lg border border-gray-600 px-3 py-1.5 text-xs text-gray-300 hover:bg-surface-hover transition-colors cursor-pointer",
    }, plusIcon({class: "w-3.5 h-3.5"}), "Add deployment"),
);

// ---------------------------------------------------------------------------
// Inspector
// ---------------------------------------------------------------------------

const copyButton = (text, what) => {
    const done = van.state(false);
    return button({
        type: "button",
        title: `Copy ${what}`,
        "aria-label": `Copy ${what}`,
        class: "inline-flex h-5 w-5 flex-none items-center justify-center rounded text-gray-500 hover:text-gray-100 hover:bg-white/10 transition-colors cursor-pointer",
        onclick: async (e) => {
            e.stopPropagation();
            try { await navigator.clipboard.writeText(text); } catch { /* fixture */ }
            done.val = true;
            setTimeout(() => { done.val = false; }, 1200);
        },
    }, () => done.val ? checkIcon({class: "w-3 h-3 text-green-400"}) : copyIcon({class: "w-3 h-3"}));
};

const factTh = "w-[92px] border-b border-gray-800/60 px-2 py-1 text-left align-baseline text-[10px] font-semibold uppercase tracking-wide text-gray-500";
const factTd = "border-b border-gray-800/60 px-2 py-1 align-baseline text-xs text-gray-300 break-all";

const factRow = (label, ...value) => tr(th({class: factTh, scope: "row"}, label), td({class: factTd}, ...value));

const factsTable = (deployment) => {
    const nodes = distinctNodes(deployment);
    // A _system row is really an aggregate of one deployment per node, so the
    // single-deployment facts — created/deployed audit, one target version,
    // one inbound address — have no single true value. Omit them and let the
    // per-instance table below carry the per-node detail.
    const system = deployment.spaceId === SYSTEM_SPACE_ID;
    return table({class: "w-full border-collapse"},
        tbody(
            factRow("Deployment", span({class: "font-mono text-gray-100"}, deployment.name)),
            factRow("Space", span({class: "inline-flex items-center gap-1.5 font-mono"},
                spaceDot(deployment.spaceId), spaceName(deployment.spaceId))),
            factRow("Nodes", span({class: "font-mono"}, nodes.join(", "))),
            ...(system ? [] : [
                factRow("Created", formatDateTime(deployment.createdAt, "-")),
                factRow("Deployed", `${formatDateTime(deployment.deployedAt, "-")} · ${deployment.deployedBy}`),
                factRow("Target", span({class: "font-mono", title: deployment.desiredVersion},
                    shortVersion(deployment.desiredVersion))),
                factRow("Inbound IP", div({class: "flex items-center gap-1.5 min-w-0"},
                    span({class: "truncate font-mono"}, deployment.inbound.addr),
                    copyButton(deployment.inbound.addr, "inbound address"))),
                factRow("DNS name", div({class: "flex items-center gap-1.5 min-w-0"},
                    span({class: "truncate font-mono"}, deployment.inbound.dns),
                    copyButton(deployment.inbound.dns, "DNS name"))),
            ]),
        ));
};

const miniTh = (text, extra = "") => th(
    {class: `border-b border-gray-700/70 border-r border-r-gray-800/40 last:border-r-0 bg-gray-950/40 px-2 py-1 text-left text-[10px] font-medium uppercase tracking-wide text-gray-500 ${extra}`},
    text);
const miniTd = (extra, ...children) => td(
    {class: `border-b border-gray-800/50 border-r border-r-gray-800/30 last:border-r-0 px-2 py-[3px] whitespace-nowrap overflow-hidden ${extra}`},
    ...children);

const instancesTable = (deployment) => table(
    {class: "w-full table-fixed border-collapse text-xs"},
    colgroup(
        col({style: "width:2.2rem"}),
        col({style: "width:4.8rem"}),
        col({style: "width:5.2rem"}),
        col(),
        col({style: "width:6.6rem"}),
        col({style: "width:2.2rem"}),
    ),
    thead(tr(miniTh("Id"), miniTh("Node"), miniTh("Status"), miniTh("Version"), miniTh("Prepare"), miniTh("Rst", "text-right"))),
    tbody(...deployment.instances.map((instance) => tr(
        {class: "hover:bg-gray-700/35"},
        miniTd("font-mono text-gray-500", String(instance.instanceId)),
        miniTd("font-mono text-gray-300 truncate", instance.node),
        miniTd("", span({class: `flex items-center gap-1.5 ${statusMeta(instance.status).text}`},
            span({class: `inline-block w-1.5 h-1.5 rounded-full flex-none ${statusMeta(instance.status).dot}`}),
            span({class: "truncate"}, instance.status))),
        miniTd(`font-mono truncate ${versionTone(deployment, instance.version)}`,
            span({title: instance.version}, shortVersion(instance.version))),
        miniTd(`truncate ${PREPARE_TEXT[instance.prepare.tone]}`, instance.prepare.label),
        miniTd("text-right tabular-nums " + (instance.restarts > 0 ? "text-gray-300" : "text-gray-600"),
            String(instance.restarts)),
    ))),
);

const sectionLabel = (text) => p({class: "px-2 pt-3 pb-1 text-[10px] font-semibold uppercase tracking-wider text-gray-500"}, text);

const inspectorActionButton = (text, cls = "bg-gray-700 text-gray-200 hover:bg-gray-600") => button({
    type: "button",
    class: `text-xs px-2.5 py-1.5 rounded-md font-medium transition-colors cursor-pointer whitespace-nowrap ${cls}`,
}, text);

const detailsTab = (deployment) => [
    div({class: "app-scroll flex-1 min-h-0 overflow-y-auto"},
        sectionLabel("Overview"),
        factsTable(deployment),
        sectionLabel(`Instances · ${deployment.instances.length}`),
        div({class: "px-2 pb-2"}, instancesTable(deployment))),
    div({class: "flex flex-none flex-wrap gap-1.5 border-t border-gray-800 px-3 py-2.5"},
        inspectorActionButton("Edit", "bg-brand text-white hover:bg-blue-600"),
        inspectorActionButton("Fork"),
        inspectorActionButton("View config"),
        inspectorActionButton("Prepare output"),
        inspectorActionButton("Delete", "bg-gray-700 text-gray-200 hover:bg-red-600 hover:text-white")),
];

const historyTab = (deployment) => {
    const entries = historyFor(deployment);
    const rows = () => (showStatusRows.val ? entries : entries.filter((e) => e.kind === "config"));
    return [
        div({class: "flex flex-none items-center justify-between px-3 py-1.5"},
            span({class: "text-[11px] text-gray-500"}, () => `${rows().length} entries`),
            labelTag({class: "flex cursor-pointer items-center gap-1.5 text-[11px] text-gray-400 select-none"},
                input({
                    type: "checkbox",
                    class: "accent-blue-500",
                    checked: showStatusRows,
                    onchange: (e) => { showStatusRows.val = e.target.checked; },
                }),
                "Status rows")),
        div({class: "app-scroll flex-1 min-h-0 overflow-auto"},
            () => table(
                {class: "w-full table-fixed border-collapse text-xs"},
                colgroup(
                    col({style: "width:7.2rem"}),
                    col({style: "width:3.2rem"}),
                    // Wide enough for "v12 (revert)" with the inline link.
                    col({style: "width:5.4rem"}),
                    col({style: "width:3rem"}),
                    col(),
                ),
                thead(tr(miniTh("Time"), miniTh("Type"), miniTh("V"), miniTh("By"), miniTh("Change"))),
                tbody(...rows().map((entry) => tr(
                    {class: "hover:bg-gray-700/35"},
                    miniTd("font-mono text-gray-500 tabular-nums", formatHistoryTime(entry.at)),
                    miniTd(entry.kind === "config" ? "text-orange-300" : "text-gray-500", entry.kind),
                    miniTd("font-mono text-gray-500 tabular-nums",
                        entry.v === null ? "" : span(`v${entry.v}`),
                        entry.kind === "config" && entry.targetVersion && !entry.latestConfig
                            ? span(" ",
                                button({
                                    type: "button",
                                    title: `Revert target version to ${shortVersion(entry.targetVersion)}`,
                                    class: "p-0 text-[11px] text-blue-400 underline hover:text-blue-300 cursor-pointer",
                                }, "revert"))
                            : ""),
                    miniTd("truncate text-gray-500", entry.by || ""),
                    miniTd(`font-mono truncate ${entry.kind === "config" ? "text-orange-300" : "text-gray-400"}`,
                        span({title: entry.change}, entry.change)),
                ))))),
    ];
};

const inspectorTabButton = (key, text) => button({
    type: "button",
    class: () => "flex-1 border-b-2 px-3 py-2 text-xs font-medium transition-colors cursor-pointer " +
        (inspectorTab.val === key
            ? "border-brand text-gray-100"
            : "border-transparent text-gray-500 hover:text-gray-300"),
    onclick: () => { inspectorTab.val = key; },
}, text);

const INSPECTOR_MIN = 340;
const INSPECTOR_MAX = 760;
// rawVal on build, DOM width written directly during the drag: resizing never
// rebuilds the inspector, the state only remembers the width for the next open.
const inspectorWidth = van.state(448);

const startInspectorResize = (event) => {
    event.preventDefault();
    event.stopPropagation();
    const pane = event.target.parentElement;
    const startX = event.clientX;
    const startW = inspectorWidth.rawVal;
    let width = startW;
    const move = (ev) => {
        width = Math.min(INSPECTOR_MAX, Math.max(INSPECTOR_MIN, startW - (ev.clientX - startX)));
        if (pane) pane.style.width = `${width}px`;
    };
    const up = () => {
        document.removeEventListener("mousemove", move);
        document.removeEventListener("mouseup", up);
        document.body.classList.remove("resizing");
        inspectorWidth.val = width;
    };
    document.addEventListener("mousemove", move);
    document.addEventListener("mouseup", up);
    document.body.classList.add("resizing");
};

const inspector = () => {
    const deployment = deployments.find((d) => d.id === selectedId.val);
    if (!deployment) return "";
    return div(
        {
            class: "relative flex h-full flex-none flex-col border-l border-gray-700 bg-gray-950/35",
            style: `width:${inspectorWidth.rawVal}px`,
        },
        button({
            type: "button",
            class: "vgrip",
            "aria-label": "Resize inspector",
            onmousedown: startInspectorResize,
        }),
        div({class: "flex flex-none items-center gap-2 border-b border-gray-800 py-2.5 pl-3 pr-2"},
            span({class: "min-w-0 truncate font-mono text-sm text-white"}, deployment.name),
            span({class: "inline-flex items-center gap-1.5 font-mono text-[11px] text-gray-400"},
                spaceDot(deployment.spaceId), spaceName(deployment.spaceId)),
            div({class: "flex-1"}),
            button({
                type: "button",
                title: "Close inspector",
                "aria-label": "Close inspector",
                class: "inline-flex h-6 w-6 flex-none items-center justify-center rounded text-gray-500 hover:text-gray-100 hover:bg-white/10 transition-colors cursor-pointer",
                onclick: () => { selectedId.val = null; },
            }, closeIcon({class: "w-3.5 h-3.5"}))),
        div({class: "flex flex-none border-b border-gray-800"},
            inspectorTabButton("details", "Details"),
            inspectorTabButton("history", "History")),
        () => div({class: "flex min-h-0 flex-1 flex-col"},
            ...(inspectorTab.val === "history" ? historyTab(deployment) : detailsTab(deployment))),
    );
};

// ---------------------------------------------------------------------------
// Fixture chrome + assembly
// ---------------------------------------------------------------------------

const fixtureHeader = div(
    {class: "flex flex-none flex-wrap items-center gap-3 border-b border-gray-700 bg-gray-950/60 px-3 py-2"},
    h1({class: "text-sm font-semibold text-white"}, "Deployment status redesign"),
    p({class: "text-xs text-gray-500"},
        "Aggregated cells render one-line groups when they fit their column, stacked sub-lines otherwise."),
);

const tableScroll = div({class: "app-scroll flex-1 min-h-0 overflow-auto"}, deploymentTable);

van.add(document.body, div(
    // bg-surface: like the explorer pages, the whole page is one flush card
    // surface rather than content floating on the body background.
    {class: "flex h-screen min-h-0 flex-col bg-surface"},
    fixtureHeader,
    div({class: "flex min-h-0 flex-1"},
        div({class: "flex min-h-0 min-w-0 flex-1 flex-col"},
            toolbar(),
            tableScroll),
        inspector),
    () => openMenu.val ? div({class: "fixed inset-0 z-20", onclick: () => { openMenu.val = null; }}) : "",
));

// Refit whenever the table's container changes size: the initial layout,
// window resizes, and the inspector opening, closing, or being dragged.
new ResizeObserver(() => fitColumns(tableScroll.clientWidth)).observe(tableScroll);
