import van from "vanjs-core";
import {formatGlobalRule, formatRule, formatSelector, positionValueName} from "../lib/authz.js";

const {div, span} = van.tags;

// Never spell out more than this many specific values, even when there is
// room — "staging, prod, default, dev" always renders as "4 spaces".
const MAX_LISTED = 3;

let measureCtx = null;
const textWidth = (font, text) => {
    if (!measureCtx) measureCtx = document.createElement("canvas").getContext("2d");
    measureCtx.font = font;
    return measureCtx.measureText(text).width;
};

// A tier is either a plain string or {text, nodes} built by tier(): text for
// width measurement, nodes (strings and coloured spans) for rendering. The
// accent spans keep the chip's font so measuring the plain text stays exact.
const tier = (...parts) => ({
    text: parts.map((p) => typeof p === "string" ? p : p.textContent).join(""),
    nodes: parts,
});
const tierText = (t) => typeof t === "string" ? t : t.text;
const tierNodes = (t) => typeof t === "string" ? [t] : t.nodes;

// Specific item names (space names, type names, verbs, resource ids) are
// accented so they stand out from the surrounding phrasing.
const nameSpan = (text) => span({class: "text-blue-300"}, text);
const nameList = (names) => names.flatMap((n, i) => i ? [", ", nameSpan(n)] : [nameSpan(n)]);
const argSpan = (argName) => span({class: "text-amber-300"}, "${" + argName + "}");

// The intersected selector chips read as set expressions: an explicit list of
// two or more values is braced and union-joined ("{ nodes ∪ users }"), a
// single value stands bare.
const unionList = (names) => names.length === 1 ? [nameSpan(names[0])] :
    ["{ ", ...names.flatMap((n, i) => i ? [" ∪ ", nameSpan(n)] : [nameSpan(n)]), " }"];

// chip builds one segment of a rule row. Variants are ordered longest-first
// and end with a form acceptable at any width; which one shows is decided for
// the row as a whole by fitRow(), not by the segment itself.
const chip = (variants, {title, chipClass} = {}) => {
    const tiers = variants.filter(Boolean).filter((v, i, all) => !i || tierText(v) !== tierText(all[i - 1]));
    const shown = van.state(tiers[0]);
    const el = div({
        class: "min-w-0 overflow-hidden whitespace-nowrap px-1.5 py-0.5 text-xs " +
            (chipClass || "text-gray-300"),
        title: title || "",
    }, () => span(...tierNodes(shown.val)));
    // Segments take exactly the width of the phrasing they show; fitRow only
    // lets them shrink when even the shortest phrasing overflows.
    el.style.flex = "0 0 auto";
    return {el, tiers, shown, widths: null};
};

const chipWidths = (c) => {
    const style = getComputedStyle(c.el);
    const font = `${style.fontStyle} ${style.fontWeight} ${style.fontSize} ${style.fontFamily}`;
    const chrome = parseFloat(style.paddingLeft) + parseFloat(style.paddingRight) +
        parseFloat(style.borderLeftWidth) + parseFloat(style.borderRightWidth);
    return c.tiers.map((t) => Math.ceil(textWidth(font, tierText(t))) + chrome + 1);
};

// fitRow chooses phrasings for the whole row at once: everything starts at its
// longest form and the segment currently occupying the most width steps down,
// repeatedly, until the row fits. Letting each segment decide alone (sized to
// its longest tier and shrunk by flexbox) abbreviated every segment as soon as
// the row was one pixel too wide, and left the abbreviated text sitting in an
// over-wide slot — "1 space" with room to spare for "default space".
const fitRow = (chips, chrome, available) => {
    const widths = chips.map((c) => (c.widths ||= chipWidths(c)));
    const pick = chips.map(() => 0);
    let total = chrome + widths.reduce((sum, w) => sum + w[0], 0);
    while (total > available) {
        let worst = -1;
        for (let i = 0; i < chips.length; i++) {
            if (pick[i] + 1 >= widths[i].length) continue;
            if (worst < 0 || widths[i][pick[i]] > widths[worst][pick[worst]]) worst = i;
        }
        if (worst < 0) break;
        total -= widths[worst][pick[worst]] - widths[worst][pick[worst] + 1];
        pick[worst]++;
    }
    const overflowing = total > available;
    chips.forEach((c, i) => {
        c.shown.val = c.tiers[pick[i]];
        c.el.style.flex = overflowing ? "0 1 auto" : "0 0 auto";
    });
};

const listable = (values) => values.length <= MAX_LISTED;

// Each selector kind gets its own component: the noun, pluralisation, and
// tier ladder differ enough that sharing one generic builder obscures them.

const spacesChip = (sel, {spaceNames, argNames} = {}) => {
    const name = (v) => positionValueName("spaces", v, spaceNames);
    const title = formatSelector(sel, "spaces", {spaceNames, argNames});
    let tiers;
    if (sel?.argumentId) {
        const arg = argNames?.get?.(Number(sel.argumentId)) || `arg_${sel.argumentId}`;
        tiers = [tier(argSpan(arg))];
    } else if (sel?.wildcard) {
        const excluded = sel.exclude || [];
        tiers = !excluded.length ? ["all spaces"] : [
            listable(excluded) && tier("all spaces except ", ...unionList(excluded.map(name))),
            `all spaces except ${excluded.length}`,
        ];
    } else if ((sel?.include || []).length) {
        const included = sel.include;
        tiers = [
            listable(included) && tier(...unionList(included.map(name)), included.length === 1 ? " space" : " spaces"),
            `${included.length} ${included.length === 1 ? "space" : "spaces"}`,
        ];
    } else {
        tiers = ["no spaces"];
    }
    return chip(tiers.filter(Boolean), {title});
};

const entityTypesChip = (sel, {spaceNames, argNames} = {}) => {
    // Resource types render as their plural noun alone — "secrets", never
    // "secret type" — so the chip reads as the set of things the rule touches.
    const name = (v) => {
        const singular = positionValueName("entityTypes", v, spaceNames);
        return singular === "access" ? singular : `${singular}s`;
    };
    const title = formatSelector(sel, "entityTypes", {spaceNames, argNames});
    let tiers;
    if (sel?.argumentId) {
        const arg = argNames?.get?.(Number(sel.argumentId)) || `arg_${sel.argumentId}`;
        tiers = [tier(argSpan(arg))];
    } else if (sel?.wildcard) {
        const excluded = sel.exclude || [];
        tiers = !excluded.length ? ["all resources"] : [
            listable(excluded) && tier("all resources except ", ...unionList(excluded.map(name))),
            `all resources except ${excluded.length}`,
        ];
    } else if ((sel?.include || []).length) {
        const included = sel.include;
        tiers = [
            listable(included) && tier(...unionList(included.map(name))),
            `${included.length} ${included.length === 1 ? "resource" : "resources"}`,
        ];
    } else {
        tiers = ["no resources"];
    }
    return chip(tiers.filter(Boolean), {title});
};

const entityRefsChip = (sel, {argNames} = {}) => {
    const ref = (v) => `#${v}`;
    const title = formatSelector(sel, "entityRefs", {argNames});
    let tiers;
    if (sel?.argumentId) {
        const arg = argNames?.get?.(Number(sel.argumentId)) || `arg_${sel.argumentId}`;
        tiers = [tier(argSpan(arg))];
    } else if (sel?.wildcard) {
        const excluded = sel.exclude || [];
        tiers = !excluded.length ? ["all instances"] : [
            listable(excluded) && tier("all instances except ", ...unionList(excluded.map(ref))),
            `all instances except ${excluded.length}`,
        ];
    } else if ((sel?.include || []).length) {
        const included = sel.include;
        tiers = [
            listable(included) && tier(included.length === 1 ? "instance " : "instances ", ...unionList(included.map(ref))),
            `${included.length} ${included.length === 1 ? "instance" : "instances"}`,
        ];
    } else {
        tiers = ["no instances"];
    }
    return chip(tiers.filter(Boolean), {title});
};

const permissionsChip = (sel, {argNames} = {}) => {
    const name = (v) => positionValueName("permissions", v);
    const title = formatSelector(sel, "permissions", {argNames});
    let tiers;
    if (sel?.argumentId) {
        const arg = argNames?.get?.(Number(sel.argumentId)) || `arg_${sel.argumentId}`;
        tiers = [tier(argSpan(arg))];
    } else if (sel?.wildcard) {
        const excluded = sel.exclude || [];
        tiers = !excluded.length ? ["all actions"] : [
            listable(excluded) && tier("all actions except ", ...nameList(excluded.map(name))),
            `all actions except ${excluded.length}`,
        ];
    } else if ((sel?.include || []).length) {
        const included = sel.include;
        tiers = [
            listable(included) && tier(...nameList(included.map(name))),
            `${included.length} ${included.length === 1 ? "action" : "actions"}`,
        ];
    } else {
        tiers = ["no actions"];
    }
    return chip(tiers.filter(Boolean), {title});
};

const delegationChip = (delegationAllowed) => chip(
    delegationAllowed ? ["user + agents"] : ["user only"],
    {
        title: `delegation ${delegationAllowed ? "allowed" : "not allowed"}`,
        chipClass: delegationAllowed ? "bg-teal-950/40 text-teal-300" : "text-gray-500",
    },
);

// On a global deny rule the flag means something different: it narrows when
// the rule fires (delegated agent sessions only) rather than what a grant
// allows, so it gets its own chip with "applies to" phrasing.
const delegatedOnlyChip = (delegatedOnly) => chip(
    delegatedOnly
        ? ["delegated agent sessions only", "delegated agents only", "agents only"]
        : ["everyone"],
    {
        title: delegatedOnly ? "denies delegated agent sessions only" : "denies everyone",
        chipClass: delegatedOnly ? "bg-teal-950/40 text-teal-300" : "text-gray-500",
    },
);

// Connector words ("allow", "on", "by") are fixed phrasing between chips: like
// arrows they never shrink or change tier.
const chipWord = (text, wordClass) => div({
    class: "flex shrink-0 items-center px-1 text-xs select-none " + (wordClass || "text-gray-500"),
}, text);

// The segments of one rule form a single unioned pill: one rounded border and
// background around the row, no per-chip chrome. w-fit keeps the pill hugging
// its content while max-w-full still lets the segments shrink inside a narrow
// cell. Items are chips or fixed separators (connector words); separators
// are laid out as-is and their width counts as chrome when fitting the chips.
const chipRow = (title, ...items) => {
    const chips = items.filter((it) => it.el);
    const separators = items.filter((it) => !it.el);
    const pill = div({
        class: "flex w-fit max-w-full min-w-0 items-stretch overflow-hidden rounded-md " +
            "border border-gray-700 bg-gray-950/40",
        title,
    }, ...items.map((it) => it.el || it));
    // The available width is read from a full-width wrapper, not from the pill:
    // the pill hugs its content, so observing it would feed every relayout back
    // into the next measurement.
    const wrap = div({class: "w-full min-w-0"}, pill);
    const observer = new ResizeObserver(() => {
        const style = getComputedStyle(pill);
        const chrome = parseFloat(style.borderLeftWidth) + parseFloat(style.borderRightWidth) +
            separators.reduce((sum, s) => sum + s.getBoundingClientRect().width, 0);
        fitRow(chips, chrome, wrap.getBoundingClientRect().width);
    });
    observer.observe(wrap);
    return wrap;
};

// The selectors read as one prepositional chain — a rule matches where all of
// them overlap: "on <instances> of <resources> in <spaces>".
const selectorChips = (rule, opts) => [
    entityRefsChip(rule.entityRefs, opts),
    chipWord("of"),
    entityTypesChip(rule.entityTypes, opts),
    chipWord("in"),
    spacesChip(rule.spaces, opts),
];

// ruleDisplay renders one authz rule as a sentence of human-readable chips:
// "allow <actions> on <instances> of <resources> in <spaces> by <user +
// agents|user only>". The row never wraps; the phrasings step down together as
// the row narrows. Hovering shows the raw grammar.
export const ruleDisplay = (rule, {spaceNames, argNames} = {}) => {
    if (!rule) return "";
    const opts = {spaceNames, argNames};
    return chipRow(formatRule(rule, opts),
        chipWord("allow", "text-green-400"),
        permissionsChip(rule.permissions, opts),
        chipWord("on"),
        ...selectorChips(rule, opts),
        chipWord("by"),
        delegationChip(rule.delegationAllowed));
};

// globalRuleDisplay is the deny-rule variant: same sentence shape with a red
// "deny", and the trailing chip states who the deny applies to instead of
// delegability.
export const globalRuleDisplay = (rule, {spaceNames, argNames} = {}) => {
    if (!rule) return "";
    const opts = {spaceNames, argNames};
    return chipRow(formatGlobalRule(rule, opts),
        chipWord("deny", "text-red-400"),
        permissionsChip(rule.permissions, opts),
        chipWord("on"),
        ...selectorChips(rule, opts),
        chipWord("by"),
        delegatedOnlyChip(rule.delegatedOnly));
};
