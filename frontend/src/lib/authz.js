// Pure helpers for rendering authz rule templates, grants, and global rules.
// Kept free of van and capi imports so they can be unit tested outside a
// browser. Verb and entity ids mirror the AuthzVerb / AuthzEntity enums in
// api-contract/model.proto; duplicated rather than imported because the
// generated capi module reaches for `window`.

export const VERBS = [
    {id: 1, name: "create"},
    {id: 2, name: "update"},
    {id: 3, name: "delete"},
    {id: 4, name: "view"},
    {id: 5, name: "view_logs"},
    {id: 6, name: "reveal"},
];

export const ENTITY_TYPES = [
    {id: 1, name: "space"},
    {id: 2, name: "deployment"},
    {id: 3, name: "secret"},
    {id: 4, name: "config"},
    {id: 5, name: "asset"},
    {id: 6, name: "node"},
    {id: 7, name: "cluster"},
    {id: 8, name: "user"},
    {id: 9, name: "access"},
];

const verbNames = new Map(VERBS.map((v) => [v.id, v.name]));
const entityNames = new Map(ENTITY_TYPES.map((e) => [e.id, e.name]));

// The four selector positions of a rule, in grammar order
// (spaces : entity types : entity refs : permissions).
export const POSITIONS = [
    {key: "spaces", label: "Spaces"},
    {key: "entityTypes", label: "Entity types"},
    {key: "entityRefs", label: "Entity refs"},
    {key: "permissions", label: "Permissions"},
];

export function positionValueName(kind, value, spaceNames) {
    const id = Number(value);
    if (kind === "permissions") return verbNames.get(id) || String(value);
    if (kind === "entityTypes") return entityNames.get(id) || String(value);
    if (kind === "spaces") {
        const named = spaceNames instanceof Map ? spaceNames.get(id) : undefined;
        return named !== undefined ? named : String(value);
    }
    return String(value);
}

// templateArguments derives each declared argument's position kind from the
// selector that references it. Arguments are template-level and each may only
// appear in one position kind, so the first reference wins.
export function templateArguments(template) {
    const kinds = new Map();
    for (const rule of template?.rules || []) {
        for (const {key} of POSITIONS) {
            const sel = rule?.[key];
            if (sel?.argumentId) {
                if (!kinds.has(Number(sel.argumentId))) kinds.set(Number(sel.argumentId), key);
            }
        }
    }
    return (template?.arguments || [])
        .filter((a) => a && kinds.has(Number(a.id)))
        .map((a) => ({id: Number(a.id), name: a.name || `arg_${a.id}`, kind: kinds.get(Number(a.id))}));
}

// formatSelector renders one selector in the compact rule grammar: `*` for
// wildcard, `${name}` for an argument, comma-joined names for a list, with
// exclusions appended as `-name`. A selector that matches nothing renders ∅.
export function formatSelector(sel, kind, {spaceNames, argNames} = {}) {
    if (!sel) return "∅";
    const name = (v) => positionValueName(kind, v, spaceNames);
    let base;
    if (sel.wildcard) {
        base = "*";
    } else if (sel.argumentId) {
        const argName = argNames instanceof Map ? argNames.get(Number(sel.argumentId)) : undefined;
        base = "${" + (argName || `arg_${sel.argumentId}`) + "}";
    } else if ((sel.include || []).length) {
        base = sel.include.map(name).join(",");
    } else {
        return "∅";
    }
    return base + (sel.exclude || []).map((v) => `-${name(v)}`).join("");
}

export function formatRule(rule, opts = {}) {
    if (!rule) return "";
    const parts = POSITIONS.map(({key}) => formatSelector(rule[key], key, opts));
    parts.push(rule.delegationAllowed ? "true" : "false");
    return parts.join(":");
}

// Deny-mode global rules have no delegation position: delegatedOnly narrows
// when the rule fires rather than what it matches, so callers render it
// separately. Allow-mode rules behave like grants everyone holds, so their
// grammar carries the delegation position like an ordinary rule.
export function formatGlobalRule(rule, opts = {}) {
    if (!rule) return "";
    const parts = POSITIONS.map(({key}) => formatSelector(rule[key], key, opts));
    if (!rule.deny) parts.push(rule.delegationAllowed ? "true" : "false");
    return parts.join(":");
}

export function describeSelector(sel, kind, spaceNames) {
    if (!sel) return "nothing";
    const name = (v) => positionValueName(kind, v, spaceNames);
    const except = (sel.exclude || []).length ? ` except ${sel.exclude.map(name).join(", ")}` : "";
    if (sel.wildcard) return (kind === "spaces" ? "everywhere" : "everything") + except;
    if ((sel.include || []).length) return sel.include.map(name).join(", ") + except;
    return "nothing";
}

// describeGrant produces the chip content for one grant record: template
// grants carry the template name with bound argument values filled in, direct
// grants a short natural reading of their single rule. `title` is always the
// raw rule grammar for hover.
export function describeGrant(record, templatesById, spaceNames) {
    const templateId = Number(record?.templateId || 0);
    const template = templateId ? templatesById?.get?.(templateId) : null;
    if (templateId) {
        if (!template) {
            return {template: true, label: `role ${templateId}`, detail: "", title: "", delegable: false};
        }
        const args = templateArguments(template.template);
        const bindings = new Map((record?.grant?.args || []).map((b) => [Number(b.argumentId), b.values || []]));
        const detail = args
            .map((a) => (bindings.get(a.id) || []).map((v) => positionValueName(a.kind, v, spaceNames)).join(", "))
            .join("; ");
        const argNames = new Map(args.map((a) => [a.id, a.name]));
        const rules = template.template?.rules || [];
        return {
            template: true,
            label: template.name,
            detail,
            title: rules.map((r) => formatRule(r, {spaceNames, argNames})).join(" · "),
            delegable: rules.some((r) => r?.delegationAllowed),
        };
    }
    const rule = record?.grant?.rule;
    return {
        template: false,
        label: describeSelector(rule?.permissions, "permissions", spaceNames),
        detail: `${describeSelector(rule?.entityTypes, "entityTypes", spaceNames)} · ${describeSelector(rule?.spaces, "spaces", spaceNames)}`,
        title: formatRule(rule, {spaceNames}),
        delegable: !!rule?.delegationAllowed,
    };
}

// The builtin role every user starts with. Its id is fixed by the backend
// (authz.ClusterAdminTemplateID); the name is the identity the UI trusts, with
// the id only as a fallback when the template record itself is not to hand.
export const CLUSTER_ADMIN_TEMPLATE_ID = 1;
const CLUSTER_ADMIN_NAME = "cluster_admin";

export function isClusterAdminGrant(grant, templatesById) {
    const templateId = Number(grant?.templateId || 0);
    if (!templateId) return false;
    const template = templatesById?.get?.(templateId);
    if (!template) return templateId === CLUSTER_ADMIN_TEMPLATE_ID;
    return Boolean(template.builtin) && template.name === CLUSTER_ADMIN_NAME;
}

// grantRevokeBlock returns why a grant may not be revoked, or null when it may.
// Only cluster_admin is protected, in the two cases that lock somebody out:
// taking it off yourself, and taking the last one off the cluster. The backend
// guard behind this is broader — it refuses to delete the last grant that can
// manage access, whatever role it came from — so a custom access-managing role
// does not unlock the last cluster_admin here.
export function grantRevokeBlock(grant, {grants, templatesById, selfUserId} = {}) {
    if (!isClusterAdminGrant(grant, templatesById)) return null;
    if (selfUserId && Number(grant?.userId || 0) === Number(selfUserId)) {
        return "You cannot remove your own cluster_admin role.";
    }
    const others = (grants || []).filter((g) =>
        Number(g?.id || 0) !== Number(grant?.id || 0) && isClusterAdminGrant(g, templatesById));
    if (!others.length) return "This is the last cluster_admin role and cannot be removed.";
    return null;
}

export function groupGrantsByUser(grants) {
    const byUser = new Map();
    for (const grant of grants || []) {
        const userId = Number(grant?.userId || 0);
        if (!byUser.has(userId)) byUser.set(userId, []);
        byUser.get(userId).push(grant);
    }
    return byUser;
}
