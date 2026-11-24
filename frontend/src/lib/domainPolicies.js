// Pure helpers for space domain policies: the rule's effect and its two
// selector positions, the summary text, domain parsing, the editor draft and
// its request payloads, and the decision the server makes for a hostname.
// No van or capi imports, for the reason given in authz.js.
import {allExcludingSelector, exactSelector, readSelector} from "./authz.js";

const SYSTEM_SPACE_ID = 0;
const WILDCARD = "*.";

export const allowEffect = () => ({value: {allow: {}}});
export const denyEffect = () => ({value: {deny: {}}});
export const isDeny = (rule) => Boolean(rule?.effect?.value?.deny);

export const exactDomains = (values) => ({exactDomains: {values: [...values]}});
export const allDomainsExcluding = (values = []) => ({allDomainsExcluding: {values: [...values]}});

// readDomains reads the domains position the way readSelector reads the
// authz ones: mode "all", "allExcept" (values are the exclusions), "list"
// or "none".
export function readDomains(sel) {
    const all = sel?.allDomainsExcluding;
    if (all) return {mode: all.values?.length ? "allExcept" : "all", values: [...(all.values || [])]};
    const exact = sel?.exactDomains?.values || [];
    return {mode: exact.length ? "list" : "none", values: [...exact]};
}

export const readSpaces = (sel) => readSelector(sel, "spaces");

// The spaces a policy can name: every space but _system.
export const userSpaces = (spaces) => (spaces || []).filter((s) => Number(s.id) !== SYSTEM_SPACE_ID);

// --- summaries ----------------------------------------------------------------

export function describeSpaces(sel, spaceNames) {
    const view = readSpaces(sel);
    const names = view.values.map((id) => spaceNames?.get?.(Number(id)) ?? `space #${id}`).join(", ");
    if (view.mode === "all") return "all spaces";
    if (view.mode === "allExcept") return `all spaces except ${names}`;
    return view.mode === "list" ? names : "no spaces";
}

export function describeDomains(sel) {
    const view = readDomains(sel);
    if (view.mode === "all") return "all domains";
    if (view.mode === "allExcept") return `all domains except ${view.values.join(", ")}`;
    return view.mode === "list" ? view.values.join(", ") : "no domains";
}

export const ruleSentence = (rule, spaceNames) =>
    `${describeSpaces(rule?.selector?.spaces, spaceNames)} ${isDeny(rule) ? "may not publish" : "may publish"} ${describeDomains(rule?.selector?.domains)}`;

// --- domains --------------------------------------------------------------------

const LABEL = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

// canonicalDomain returns a value as the server stores it (trimmed, lower
// case, no trailing dot), or null when it is neither a hostname nor "*." and
// a hostname. The 253 limit is on the hostname, as on the server.
export function canonicalDomain(raw) {
    const value = String(raw).trim().toLowerCase();
    const wildcard = value.startsWith(WILDCARD);
    const hostname = (wildcard ? value.slice(WILDCARD.length) : value).replace(/\.$/, "");
    if (!hostname || hostname.length > 253 || !hostname.split(".").every((label) => LABEL.test(label))) return null;
    return (wildcard ? WILDCARD : "") + hostname;
}

// parseDomains reads a typed list, one value per line or comma separated,
// into the canonical values without duplicates and the tokens that are not
// domains.
export function parseDomains(text) {
    const domains = [];
    const invalid = [];
    for (const token of String(text || "").split(/[\s,]+/).filter(Boolean)) {
        const domain = canonicalDomain(token);
        if (domain === null) invalid.push(token);
        else if (!domains.includes(domain)) domains.push(domain);
    }
    return {domains, invalid};
}

// --- decision -------------------------------------------------------------------

// A "*." pattern covers every name below its suffix, not the suffix itself.
export const patternCovers = (pattern, hostname) =>
    pattern.startsWith(WILDCARD) ? hostname.endsWith(pattern.slice(1)) : pattern === hostname;

const selects = (view, listed) => view.mode === "list" ? listed : view.mode !== "none" && !listed;

export function ruleCovers(rule, spaceId, hostname) {
    const spaces = readSpaces(rule?.selector?.spaces);
    const domains = readDomains(rule?.selector?.domains);
    return selects(spaces, spaces.values.map(Number).includes(Number(spaceId)))
        && selects(domains, domains.values.some((pattern) => patternCovers(pattern, hostname)));
}

// decide mirrors backend/lib/domainpolicy: an enabled deny that covers the
// space and hostname refuses it and is named in `policy`; otherwise some
// enabled allow has to cover it.
export function decide(policies, spaceId, hostname) {
    let allowed = false;
    for (const policy of policies || []) {
        if (policy.disabled || !ruleCovers(policy.rule, spaceId, hostname)) continue;
        if (isDeny(policy.rule)) return {allowed: false, policy: policy.name};
        allowed = true;
    }
    return {allowed, policy: ""};
}

// openToEverySpace is the condition of the tab's notice: an enabled allow
// covers every space and every domain while more than one space could use it.
export function openToEverySpace(policies, spaces) {
    const open = (policies || []).some((policy) => !policy.disabled && !isDeny(policy.rule)
        && readSpaces(policy.rule?.selector?.spaces).mode === "all"
        && readDomains(policy.rule?.selector?.domains).mode === "all");
    return open && userSpaces(spaces).length > 1;
}

// --- editor draft -----------------------------------------------------------------

export const MODE_EXACT = "exact";
export const MODE_EXCEPT = "except";

// A draft is the editor's form as plain values. A new one is the common
// addition to a cluster that still has its allow-everything default: a deny
// for some domains, in every space but the ones picked.
export const newDraft = () => ({
    name: "", deny: true, enabled: true,
    spacesMode: MODE_EXCEPT, spaceIds: [],
    domainsMode: MODE_EXACT, domainsText: "",
});

const draftMode = (view) => view.mode === "all" || view.mode === "allExcept" ? MODE_EXCEPT : MODE_EXACT;

export function draftFromPolicy(policy) {
    const spaces = readSpaces(policy.rule?.selector?.spaces);
    const domains = readDomains(policy.rule?.selector?.domains);
    return {
        name: policy.name, deny: isDeny(policy.rule), enabled: !policy.disabled,
        spacesMode: draftMode(spaces), spaceIds: spaces.values.map(Number),
        domainsMode: draftMode(domains), domainsText: domains.values.join("\n"),
    };
}

// draftProblems lists, in form order, what stops a draft from being saved.
export function draftProblems(draft) {
    const problems = [];
    if (!draft.name.trim()) problems.push("Name the policy.");
    if (draft.spacesMode === MODE_EXACT && !draft.spaceIds.length) problems.push("Pick at least one space.");
    const {domains, invalid} = parseDomains(draft.domainsText);
    if (invalid.length) problems.push(`Not a hostname or a "*." wildcard of one: ${invalid.join(", ")}`);
    else if (draft.domainsMode === MODE_EXACT && !domains.length) problems.push("List at least one domain.");
    return problems;
}

export function draftRule(draft) {
    const {domains} = parseDomains(draft.domainsText);
    return {
        effect: draft.deny ? denyEffect() : allowEffect(),
        selector: {
            spaces: draft.spacesMode === MODE_EXACT ? exactSelector("spaces", draft.spaceIds) : allExcludingSelector("spaces", draft.spaceIds),
            domains: draft.domainsMode === MODE_EXACT ? exactDomains(domains) : allDomainsExcluding(domains),
        },
    };
}

// --- request payloads -----------------------------------------------------------

export const createPayload = (draft) => ({name: draft.name.trim(), disabled: !draft.enabled, rule: draftRule(draft)});

// An update replaces the whole policy, so each one carries every field.
export const updatePayload = (policy, draft) => ({id: policy.id, expectedSeq: policy.seq, ...createPayload(draft)});

export const togglePayload = (policy) =>
    ({id: policy.id, expectedSeq: policy.seq, name: policy.name, disabled: !policy.disabled, rule: policy.rule});
