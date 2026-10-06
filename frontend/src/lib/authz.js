// Pure helpers for rendering authz grant templates, grants, and global rules.
// Kept free of van and capi imports so they can be unit tested outside a
// browser. Verb, entity kind, and argument kind ids mirror the AuthzVerb /
// AuthzEntityKind / AuthzArgumentKind enums in api-contract/model/authz.proto;
// duplicated rather than imported because the generated capi module reaches
// for `window`.

export const VERBS = [
    {id: 1, name: "create"},
    {id: 2, name: "update"},
    {id: 3, name: "delete"},
    {id: 4, name: "view"},
    {id: 5, name: "view_logs"},
    {id: 6, name: "reveal"},
    {id: 7, name: "use_host_mounts"},
    {id: 8, name: "use_host_network"},
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

// AuthzArgumentKind by position key, and back.
export const ARGUMENT_KINDS = {permissions: 1, spaces: 2, entityTypes: 3, entityRefs: 4};
const positionOfArgumentKind = new Map(Object.entries(ARGUMENT_KINDS).map(([key, kind]) => [kind, key]));

// The two list fields of each selector position: the exact list, then the
// all-but list.
const SELECTOR_FIELDS = {
    permissions: ["exactVerbs", "allVerbsExcluding"],
    spaces: ["exactSpaces", "allSpacesExcluding"],
    entityTypes: ["exactEntityTypes", "allEntityTypesExcluding"],
    entityRefs: ["exactEntityRefs", "allEntityRefsExcluding"],
};

// --- entity refs ------------------------------------------------------------------

// The targets an AuthzEntityRef can name (the AuthzEntityRefTarget oneof),
// with the entity type each one belongs to and the short name the UI shows
// and accepts in typed input (`deployment#12`).
export const REF_TARGETS = [
    {key: "space", name: "space", entityType: 1},
    {key: "deployment", name: "deployment", entityType: 2},
    {key: "secret", name: "secret", entityType: 3},
    {key: "config", name: "config", entityType: 4},
    {key: "asset", name: "asset", entityType: 5},
    {key: "node", name: "node", entityType: 6},
    {key: "systemConfig", name: "cluster", entityType: 7},
    {key: "user", name: "user", entityType: 8},
    {key: "authzGrantTemplate", name: "role", entityType: 9},
    {key: "authzGrant", name: "grant", entityType: 9},
    {key: "authzGlobalRule", name: "global_rule", entityType: 9},
];

export const entityRef = (targetKey, id) => ({target: {value: {[targetKey]: Number(id)}}});

export function refParts(ref) {
    const value = ref?.target?.value || {};
    for (const target of REF_TARGETS) {
        if (value[target.key] != null) return {key: target.key, name: target.name, id: Number(value[target.key])};
    }
    return null;
}

export function formatRef(ref) {
    const parts = refParts(ref);
    return parts ? `${parts.name}#${parts.id}` : "?";
}

// refTargetForEntityType gives the one target an entity type's instances are
// named by, or null when the type has several (access) or none.
export function refTargetForEntityType(entityTypeId) {
    const targets = REF_TARGETS.filter((t) => t.entityType === Number(entityTypeId));
    return targets.length === 1 ? targets[0] : null;
}

// parseEntityRefs reads typed refs (`deployment#12`, `secret:7`); a bare id
// names an instance of `defaultTarget` when one is given and is dropped
// otherwise, as is anything else unreadable.
export function parseEntityRefs(text, defaultTarget = null) {
    const refs = [];
    for (const token of (text || "").split(/[\s,]+/).filter(Boolean)) {
        const match = /^(?:([a-z_]+)[#:])?(\d+)$/i.exec(token);
        if (!match) continue;
        const typed = match[1]?.toLowerCase();
        const target = typed
            ? REF_TARGETS.find((t) => t.name === typed || t.key.toLowerCase() === typed)
            : defaultTarget;
        const id = Number(match[2]);
        if (!target || !Number.isSafeInteger(id)) continue;
        refs.push(entityRef(target.key, id));
    }
    return refs;
}

// --- selectors ------------------------------------------------------------------------

export const exactSelector = (kind, values) => ({[SELECTOR_FIELDS[kind][0]]: {values: [...values]}});
export const allExcludingSelector = (kind, values = []) => ({[SELECTOR_FIELDS[kind][1]]: {values: [...values]}});
export const argumentSelector = (argumentId) => ({value: {argument: {argumentId: Number(argumentId)}}});
export const templateSelector = (selector) => ({value: {selector}});

// readSelector reads one selector position, plain (AuthzSelector) or template
// (AuthzTemplateSelector, an argument or a wrapped record), into
// {mode, values, argumentId}: mode is "all" (every value), "allExcept"
// (values are the exclusions), "list" (values are the members), "arg"
// (argumentId names the template argument) or "none" (nothing matches).
export function readSelector(sel, kind) {
    if (!sel) return {mode: "none", values: [], argumentId: 0};
    if (sel.value) {
        if (sel.value.argument) return {mode: "arg", values: [], argumentId: Number(sel.value.argument.argumentId || 0)};
        return readSelector(sel.value.selector, kind);
    }
    const [exactField, allField] = SELECTOR_FIELDS[kind];
    const all = sel[allField];
    if (all) return {mode: all.values?.length ? "allExcept" : "all", values: [...(all.values || [])], argumentId: 0};
    const exact = sel[exactField];
    if (exact) return {mode: exact.values?.length ? "list" : "none", values: [...(exact.values || [])], argumentId: 0};
    return {mode: "none", values: [], argumentId: 0};
}

// --- effects ------------------------------------------------------------------------------

export const allowEffect = (delegationAllowed) => ({value: {allow: {delegationAllowed: !!delegationAllowed}}});
export const denyEffect = (delegatedOnly) => ({value: {deny: {delegatedOnly: !!delegatedOnly}}});

// ruleEffect reads a rule's effect: an allow carries whether delegated (agent)
// sessions also receive it, a deny whether it fires for them only.
export function ruleEffect(rule) {
    const value = rule?.effect?.value || {};
    if (value.deny) return {deny: true, delegatedOnly: !!value.deny.delegatedOnly, delegationAllowed: false};
    return {deny: false, delegatedOnly: false, delegationAllowed: !!value.allow?.delegationAllowed};
}

// --- grants -------------------------------------------------------------------------------

export const grantTemplateId = (record) => Number(record?.grant?.value?.template?.templateId || 0);
export const grantRule = (record) => record?.grant?.value?.rule || null;

export const argumentBinding = (argumentId, kind, values) =>
    ({argumentId: Number(argumentId), values: {value: {[kind]: {values: [...values]}}}});

export function bindingValues(binding) {
    const value = binding?.values?.value || {};
    for (const key of Object.keys(ARGUMENT_KINDS)) {
        if (value[key]) return [...(value[key].values || [])];
    }
    return [];
}

export const templateGrantSource = (templateId, args) => ({value: {template: {templateId: Number(templateId), args}}});
export const ruleGrantSource = (rule) => ({value: {rule}});

// --- names ------------------------------------------------------------------------------------

export function positionValueName(kind, value, spaceNames) {
    if (kind === "entityRefs") return typeof value === "object" && value !== null ? formatRef(value) : String(value);
    const id = Number(value);
    if (kind === "permissions") return verbNames.get(id) || String(value);
    if (kind === "entityTypes") return entityNames.get(id) || String(value);
    if (kind === "spaces") {
        const named = spaceNames instanceof Map ? spaceNames.get(id) : undefined;
        return named !== undefined ? named : String(value);
    }
    return String(value);
}

// templateArguments lists a template's declared arguments with their position
// kind: the declared kind, or, for a record without one, the kind of the
// first selector that references it. Arguments with no kind either way are
// left out since nothing can be bound to them.
export function templateArguments(template) {
    const referenced = new Map();
    for (const rule of template?.rules || []) {
        for (const {key} of POSITIONS) {
            const view = readSelector(rule?.selector?.[key], key);
            if (view.mode === "arg" && !referenced.has(view.argumentId)) referenced.set(view.argumentId, key);
        }
    }
    return (template?.arguments || [])
        .map((a) => a && ({
            id: Number(a.id),
            name: a.name || `arg_${a.id}`,
            kind: positionOfArgumentKind.get(Number(a.kind)) || referenced.get(Number(a.id)),
        }))
        .filter((a) => a && a.kind);
}

// formatSelector renders one selector in the compact rule grammar: `*` for
// everything, `${name}` for an argument, comma-joined names for a list, with
// exclusions appended as `-name`. A selector that matches nothing renders ∅.
export function formatSelector(sel, kind, {spaceNames, argNames} = {}) {
    const view = readSelector(sel, kind);
    const name = (v) => positionValueName(kind, v, spaceNames);
    if (view.mode === "arg") {
        const argName = argNames instanceof Map ? argNames.get(view.argumentId) : undefined;
        return "${" + (argName || `arg_${view.argumentId}`) + "}";
    }
    if (view.mode === "all" || view.mode === "allExcept") return "*" + view.values.map((v) => `-${name(v)}`).join("");
    if (view.mode === "list") return view.values.map(name).join(",");
    return "∅";
}

// formatRule renders the grammar: the four positions and, on an allow rule,
// the delegation position. A deny has none: delegatedOnly narrows when the
// rule fires rather than what it matches, so callers render it separately.
export function formatRule(rule, opts = {}) {
    if (!rule) return "";
    const parts = POSITIONS.map(({key}) => formatSelector(rule.selector?.[key], key, opts));
    const effect = ruleEffect(rule);
    if (!effect.deny) parts.push(effect.delegationAllowed ? "true" : "false");
    return parts.join(":");
}

export function describeSelector(sel, kind, spaceNames) {
    const view = readSelector(sel, kind);
    const name = (v) => positionValueName(kind, v, spaceNames);
    if (view.mode === "all" || view.mode === "allExcept") {
        const except = view.values.length ? ` except ${view.values.map(name).join(", ")}` : "";
        return (kind === "spaces" ? "everywhere" : "everything") + except;
    }
    if (view.mode === "list") return view.values.map(name).join(", ");
    return "nothing";
}

// describeGrant produces the chip content for one grant record: template
// grants carry the template name with bound argument values filled in, direct
// grants a short natural reading of their single rule. `title` is always the
// raw rule grammar for hover.
export function describeGrant(record, templatesById, spaceNames) {
    const templateId = grantTemplateId(record);
    const template = templateId ? templatesById?.get?.(templateId) : null;
    if (templateId) {
        if (!template) {
            return {template: true, label: `role ${templateId}`, detail: "", title: "", delegable: false};
        }
        const args = templateArguments(template.spec);
        const bindings = new Map((record?.grant?.value?.template?.args || []).map((b) => [Number(b.argumentId), bindingValues(b)]));
        const detail = args
            .map((a) => (bindings.get(a.id) || []).map((v) => positionValueName(a.kind, v, spaceNames)).join(", "))
            .join("; ");
        const argNames = new Map(args.map((a) => [a.id, a.name]));
        const rules = template.spec?.rules || [];
        return {
            template: true,
            label: template.name,
            detail,
            title: rules.map((r) => formatRule(r, {spaceNames, argNames})).join(" · "),
            delegable: rules.some((r) => ruleEffect(r).delegationAllowed),
        };
    }
    const rule = grantRule(record);
    const selector = rule?.selector;
    return {
        template: false,
        label: describeSelector(selector?.permissions, "permissions", spaceNames),
        detail: `${describeSelector(selector?.entityTypes, "entityTypes", spaceNames)} · ${describeSelector(selector?.spaces, "spaces", spaceNames)}`,
        title: formatRule(rule, {spaceNames}),
        delegable: ruleEffect(rule).delegationAllowed,
    };
}

// The builtin role every user starts with. Its id is fixed by the backend
// (authz.ClusterAdminTemplateID); the name is the identity the UI trusts, with
// the id only as a fallback when the template record itself is not to hand.
export const CLUSTER_ADMIN_TEMPLATE_ID = 1;
const CLUSTER_ADMIN_NAME = "cluster_admin";

export function isClusterAdminGrant(grant, templatesById) {
    const templateId = grantTemplateId(grant);
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
