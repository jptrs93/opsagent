import van from "vanjs-core";
import {formatGlobalRule, formatRule, formatSelector, positionValueName} from "../lib/authz.js";

const {div} = van.tags;

// Never spell out more than this many specific values, even when there is
// room — "staging, prod, default, dev" always renders as "4 spaces".
const MAX_LISTED = 3;

let measureCtx = null;
const textWidth = (font, text) => {
    if (!measureCtx) measureCtx = document.createElement("canvas").getContext("2d");
    measureCtx.font = font;
    return measureCtx.measureText(text).width;
};

// responsiveChip shows the longest variant that fits its flex slot. Variants
// are ordered longest-first and end with a form that is acceptable at any
// width. The chip advertises the longest variant as its flex basis and the
// shortest as its min width, so flexbox squeezes it between the two and a
// ResizeObserver picks the phrasing for whatever width was granted.
const responsiveChip = (variants, {title, chipClass} = {}) => {
    const tiers = variants.filter((v, i) => v && v !== variants[i - 1]);
    const shown = van.state(tiers[0]);
    const el = div({
        class: "min-w-0 overflow-hidden whitespace-nowrap rounded border px-2 py-0.5 text-xs " +
            (chipClass || "border-gray-700 bg-gray-950/40 text-gray-300"),
        title: title || "",
    }, () => shown.val);
    el.style.flex = "0 1 auto";
    const observer = new ResizeObserver(() => {
        const style = getComputedStyle(el);
        const font = `${style.fontStyle} ${style.fontWeight} ${style.fontSize} ${style.fontFamily}`;
        const chrome = parseFloat(style.paddingLeft) + parseFloat(style.paddingRight) +
            parseFloat(style.borderLeftWidth) + parseFloat(style.borderRightWidth);
        const widths = tiers.map((t) => Math.ceil(textWidth(font, t)) + chrome);
        el.style.flexBasis = `${widths[0]}px`;
        el.style.minWidth = `${widths[widths.length - 1]}px`;
        const available = el.getBoundingClientRect().width + 0.5;
        shown.val = tiers.find((t, i) => widths[i] <= available) ?? tiers[tiers.length - 1];
    });
    observer.observe(el);
    return el;
};

const listable = (values) => values.length <= MAX_LISTED;

// Each selector kind gets its own component: the noun, pluralisation, and
// tier ladder differ enough that sharing one generic builder obscures them.

export const spacesChip = (sel, {spaceNames, argNames} = {}) => {
    const name = (v) => positionValueName("spaces", v, spaceNames);
    const title = formatSelector(sel, "spaces", {spaceNames, argNames});
    let tiers;
    if (sel?.argumentId) {
        const arg = argNames?.get?.(Number(sel.argumentId)) || `arg_${sel.argumentId}`;
        tiers = ["${" + arg + "} spaces", "templated spaces", "limited spaces"];
    } else if (sel?.wildcard) {
        const excluded = sel.exclude || [];
        tiers = !excluded.length ? ["all spaces"] : [
            listable(excluded) && `all spaces except ${excluded.map(name).join(", ")}`,
            `all spaces except ${excluded.length}`,
            "most spaces",
        ];
    } else if ((sel?.include || []).length) {
        const included = sel.include;
        tiers = [
            listable(included) && `${included.map(name).join(", ")} ${included.length === 1 ? "space" : "spaces"}`,
            `${included.length} ${included.length === 1 ? "space" : "spaces"}`,
            "limited spaces",
        ];
    } else {
        tiers = ["no spaces"];
    }
    return responsiveChip(tiers.filter(Boolean), {title});
};

export const entityTypesChip = (sel, {spaceNames, argNames} = {}) => {
    const name = (v) => positionValueName("entityTypes", v, spaceNames);
    const title = formatSelector(sel, "entityTypes", {spaceNames, argNames});
    let tiers;
    if (sel?.argumentId) {
        const arg = argNames?.get?.(Number(sel.argumentId)) || `arg_${sel.argumentId}`;
        tiers = ["${" + arg + "} types", "templated types", "limited types"];
    } else if (sel?.wildcard) {
        const excluded = sel.exclude || [];
        tiers = !excluded.length ? ["all resource types", "all types"] : [
            listable(excluded) && `all types except ${excluded.map(name).join(", ")}`,
            `all types except ${excluded.length}`,
            "most types",
        ];
    } else if ((sel?.include || []).length) {
        const included = sel.include;
        tiers = [
            listable(included) && `${included.map(name).join(", ")} ${included.length === 1 ? "type" : "types"}`,
            `${included.length} resource ${included.length === 1 ? "type" : "types"}`,
            "limited types",
        ];
    } else {
        tiers = ["no types"];
    }
    return responsiveChip(tiers.filter(Boolean), {title});
};

export const entityRefsChip = (sel, {argNames} = {}) => {
    const ref = (v) => `#${v}`;
    const title = formatSelector(sel, "entityRefs", {argNames});
    let tiers;
    if (sel?.argumentId) {
        const arg = argNames?.get?.(Number(sel.argumentId)) || `arg_${sel.argumentId}`;
        tiers = ["${" + arg + "} resources", "templated resources", "limited resources"];
    } else if (sel?.wildcard) {
        const excluded = sel.exclude || [];
        tiers = !excluded.length ? ["all resources"] : [
            listable(excluded) && `all resources except ${excluded.map(ref).join(", ")}`,
            `all resources except ${excluded.length}`,
            "most resources",
        ];
    } else if ((sel?.include || []).length) {
        const included = sel.include;
        tiers = [
            listable(included) && `${included.length === 1 ? "resource" : "resources"} ${included.map(ref).join(", ")}`,
            `${included.length} ${included.length === 1 ? "resource" : "resources"}`,
            "limited resources",
        ];
    } else {
        tiers = ["no resources"];
    }
    return responsiveChip(tiers.filter(Boolean), {title});
};

export const permissionsChip = (sel, {argNames} = {}) => {
    const name = (v) => positionValueName("permissions", v);
    const title = formatSelector(sel, "permissions", {argNames});
    let tiers;
    if (sel?.argumentId) {
        const arg = argNames?.get?.(Number(sel.argumentId)) || `arg_${sel.argumentId}`;
        tiers = ["${" + arg + "} actions", "templated actions", "limited actions"];
    } else if (sel?.wildcard) {
        const excluded = sel.exclude || [];
        tiers = !excluded.length ? ["all actions"] : [
            listable(excluded) && `all actions except ${excluded.map(name).join(", ")}`,
            `all actions except ${excluded.length}`,
            "most actions",
        ];
    } else if ((sel?.include || []).length) {
        const included = sel.include;
        tiers = [
            listable(included) && included.map(name).join(", "),
            `${included.length} ${included.length === 1 ? "action" : "actions"}`,
            "limited actions",
        ];
    } else {
        tiers = ["no actions"];
    }
    return responsiveChip(tiers.filter(Boolean), {title});
};

export const delegationChip = (delegationAllowed) => responsiveChip(
    delegationAllowed
        ? ["agent delegable", "delegable", "agents ✓"]
        : ["not agent delegable", "not delegable", "agents ✗"],
    {
        title: `delegation ${delegationAllowed ? "allowed" : "not allowed"}`,
        chipClass: delegationAllowed
            ? "border-teal-800 bg-teal-950/40 text-teal-300"
            : "border-gray-700 bg-gray-950/40 text-gray-500",
    },
);

// On a global deny rule the flag means something different: it narrows when
// the rule fires (delegated agent sessions only) rather than what a grant
// allows, so it gets its own chip with "applies to" phrasing.
export const delegatedOnlyChip = (delegatedOnly) => responsiveChip(
    delegatedOnly
        ? ["delegated agent sessions only", "delegated agents only", "agents only"]
        : ["everyone"],
    {
        title: delegatedOnly ? "denies delegated agent sessions only" : "denies everyone",
        chipClass: delegatedOnly
            ? "border-teal-800 bg-teal-950/40 text-teal-300"
            : "border-gray-700 bg-gray-950/40 text-gray-500",
    },
);

const chipRow = (title, ...chips) => div({class: "flex min-w-0 items-center gap-1.5", title}, ...chips);

const selectorChips = (rule, opts) => [
    spacesChip(rule.spaces, opts),
    entityTypesChip(rule.entityTypes, opts),
    entityRefsChip(rule.entityRefs, opts),
    permissionsChip(rule.permissions, opts),
];

// ruleDisplay renders one authz rule as a row of human-readable chips in
// grammar order. The row never wraps; each chip independently steps down to
// a shorter phrasing as the row narrows. Hovering shows the raw grammar.
export const ruleDisplay = (rule, {spaceNames, argNames} = {}) => {
    if (!rule) return "";
    const opts = {spaceNames, argNames};
    return chipRow(formatRule(rule, opts),
        ...selectorChips(rule, opts),
        delegationChip(rule.delegationAllowed));
};

// globalRuleDisplay is the deny-rule variant: same selector chips, but the
// trailing chip states who the deny applies to instead of delegability.
export const globalRuleDisplay = (rule, {spaceNames, argNames} = {}) => {
    if (!rule) return "";
    const opts = {spaceNames, argNames};
    return chipRow(formatGlobalRule(rule, opts),
        ...selectorChips(rule, opts),
        delegatedOnlyChip(rule.delegatedOnly));
};
