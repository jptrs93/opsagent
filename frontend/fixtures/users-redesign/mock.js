// In-memory backend for the users fixture. The datasets are written straight
// into the real state stores (usersMapS, spacesS, authzTemplatesS,
// authzGrantsS, authzGlobalRulesS), and installMockApi() replaces the seven
// /v1/access/* methods on the generated capi object with functions that
// mutate those stores, so the page and the real editor dialogs all run
// unchanged against the same data.
import {authzGlobalRulesS, authzGrantsS, authzTemplatesS, spacesS, usersMapS} from "/src/state/deployments.js";
import {
    allExcludingSelector,
    allowEffect,
    argumentBinding,
    argumentSelector,
    denyEffect,
    entityRef,
    exactSelector,
    grantTemplateId,
    readSelector,
    ruleEffect,
    ruleGrantSource,
    templateGrantSource,
    templateSelector,
} from "/src/lib/authz.js";

// --- rule grammar helpers ------------------------------------------------------

// Position builders take the position kind when the rule is assembled, so a
// dataset reads in grammar order without repeating the kind.
const all = () => (kind) => allExcludingSelector(kind);
const allExcept = (...ids) => (kind) => allExcludingSelector(kind, ids);
const list = (...ids) => (kind) => exactSelector(kind, ids);
const arg = (id) => () => argumentSelector(id);

// Verb and entity ids mirror AuthzVerb / AuthzEntityKind in api-contract/model/authz.proto.
const V = {create: 1, update: 2, delete: 3, view: 4, view_logs: 5, reveal: 6, use_host_mounts: 7, use_host_network: 8};
const E = {space: 1, deployment: 2, secret: 3, config: 4, asset: 5, node: 6, cluster: 7, user: 8, access: 9};
const SPACES_ARG_KIND = 2;
const REFS_ARG_KIND = 4;

const rule = (permissions, spaces, entityTypes, entityRefs, delegationAllowed = false) => ({
    effect: allowEffect(delegationAllowed),
    selector: {
        permissions: permissions("permissions"),
        spaces: spaces("spaces"),
        entityTypes: entityTypes("entityTypes"),
        entityRefs: entityRefs("entityRefs"),
    },
});
const deny = (permissions, spaces, entityTypes, entityRefs, delegatedOnly = false) =>
    ({...rule(permissions, spaces, entityTypes, entityRefs), effect: denyEffect(delegatedOnly)});
// A template rule wraps every position that is not an argument.
const templateRule = (...args) => {
    const r = rule(...args);
    return {...r, selector: Object.fromEntries(Object.entries(r.selector).map(([key, sel]) => [key, sel.value ? sel : templateSelector(sel)]))};
};

const day = (iso) => new Date(iso).getTime();

// --- builtin roles, as backend/app/primary/domain/authz/builtin.go seeds them ---

const SPACE_ADMIN_SPACES_ARG = 1; // the builtin's own argument id
// The editor assigns one fixed argument id per position kind (accessEditors.js).
const UI_ARG = {permissions: 1, spaces: 2, entityTypes: 3, entityRefs: 4};

const agentPerms = () => allExcept(V.use_host_mounts, V.use_host_network, V.view_logs);
const delegableRules = (spaces) => [
    templateRule(agentPerms(), spaces(), allExcept(E.secret), all(), true),
    templateRule(list(V.view, V.create), spaces(), list(E.secret), all(), true),
];

const builtinTemplates = () => [
    {
        id: 1, name: "cluster_admin", builtin: true, author: 0, createdAt: day("2026-03-02T09:00:00Z"),
        spec: {arguments: [], rules: [templateRule(all(), all(), all(), all(), false), ...delegableRules(() => allExcept(0))]},
    },
    {
        id: 2, name: "space_admin", builtin: true, author: 0, createdAt: day("2026-03-02T09:00:00Z"),
        spec: {
            arguments: [{id: SPACE_ADMIN_SPACES_ARG, name: "spaces", kind: SPACES_ARG_KIND}],
            rules: [
                templateRule(allExcept(V.use_host_mounts, V.use_host_network), arg(SPACE_ADMIN_SPACES_ARG), all(), all(), false),
                ...delegableRules(() => arg(SPACE_ADMIN_SPACES_ARG)),
            ],
        },
    },
];

const customTemplates = () => [
    {
        id: 3, name: "deployer", builtin: false, author: 1, createdAt: day("2026-04-14T10:20:00Z"),
        spec: {
            arguments: [{id: UI_ARG.spaces, name: "spaces", kind: SPACES_ARG_KIND}],
            rules: [
                templateRule(list(V.view, V.create, V.update), arg(UI_ARG.spaces), list(E.deployment, E.config, E.asset), all(), true),
                templateRule(list(V.view), arg(UI_ARG.spaces), list(E.secret), all(), true),
            ],
        },
    },
    {
        id: 4, name: "observer", builtin: false, author: 1, createdAt: day("2026-05-03T15:41:00Z"),
        spec: {arguments: [], rules: [templateRule(list(V.view, V.view_logs), allExcept(0), all(), all(), false)]},
    },
    {
        id: 5, name: "secrets_reader", builtin: false, author: 4, createdAt: day("2026-06-18T08:05:00Z"),
        spec: {
            arguments: [{id: UI_ARG.spaces, name: "spaces", kind: SPACES_ARG_KIND}],
            rules: [templateRule(list(V.view, V.reveal), arg(UI_ARG.spaces), list(E.secret), all(), false)],
        },
    },
    {
        id: 6, name: "node_operator", builtin: false, author: 1, createdAt: day("2026-07-22T11:30:00Z"),
        spec: {arguments: [], rules: [templateRule(list(V.view, V.update), list(0), list(E.node), all(), false)]},
    },
];

const spacesBinding = (argumentId, spaces) => argumentBinding(argumentId, "spaces", spaces);

const templateGrant = (id, userId, templateId, args = [], author = 1, createdAt = day("2026-06-01T09:00:00Z")) =>
    ({id, userId, author, createdAt, grant: templateGrantSource(templateId, args)});
const directGrant = (id, userId, r, author = 1, createdAt = day("2026-06-01T09:00:00Z")) =>
    ({id, userId, author, createdAt, grant: ruleGrantSource(r)});

const globalRule = (id, name, r, author = 1, createdAt = day("2026-03-02T09:00:00Z")) => ({id, name, author, createdAt, rule: r});

// --- datasets -----------------------------------------------------------------

const typical = () => ({
    spaces: [
        {id: 0, name: "_system"}, {id: 1, name: "global"}, {id: 2, name: "prod"}, {id: 3, name: "staging"}, {id: 4, name: "dev"},
    ],
    users: [
        {id: 1, name: "joss", createdAt: day("2026-03-02T09:04:00Z")},
        {id: 2, name: "alice", createdAt: day("2026-04-11T13:30:00Z")},
        {id: 3, name: "bob", createdAt: day("2026-05-20T10:15:00Z")},
        {id: 4, name: "priya", createdAt: day("2026-06-02T08:00:00Z")},
        {id: 5, name: "deploy-bot", createdAt: day("2026-07-15T12:00:00Z")},
        {id: 6, name: "sam", createdAt: day("2026-08-30T16:20:00Z")},
    ],
    templates: [...builtinTemplates(), ...customTemplates()],
    grants: [
        templateGrant(1, 1, 1, [], 0, day("2026-03-02T09:04:00Z")),
        templateGrant(2, 4, 1, [], 1, day("2026-06-02T08:00:00Z")),
        templateGrant(3, 2, 2, [spacesBinding(SPACE_ADMIN_SPACES_ARG, [2, 3])], 1, day("2026-04-11T13:40:00Z")),
        templateGrant(4, 3, 3, [spacesBinding(UI_ARG.spaces, [4])], 1, day("2026-05-20T10:30:00Z")),
        templateGrant(5, 3, 4, [], 1, day("2026-05-20T10:31:00Z")),
        directGrant(6, 5, rule(list(V.view, V.update), list(2), list(E.deployment), list(entityRef("deployment", 12), entityRef("deployment", 14)), true), 4, day("2026-07-15T12:05:00Z")),
        templateGrant(7, 6, 5, [spacesBinding(UI_ARG.spaces, [3])], 4, day("2026-08-30T16:25:00Z")),
        templateGrant(8, 6, 6, [], 1, day("2026-08-30T16:26:00Z")),
    ],
    globalRules: [
        globalRule(1, "default_user_visibility",
            rule(list(V.view), list(0), list(E.user), all(), true),
            0, day("2026-03-02T09:00:00Z")),
        globalRule(2, "no_agent_deletes_in_prod",
            deny(list(V.delete), list(2), list(E.deployment, E.secret, E.config), all(), true),
            1, day("2026-06-20T14:00:00Z")),
        globalRule(3, "no_host_network",
            deny(list(V.use_host_network), all(), list(E.deployment), all(), false),
            1, day("2026-07-01T09:30:00Z")),
    ],
});

// busy: enough users, grants and roles to test wrapping, truncation and the
// pill phrasing ladder in narrow cells.
const busy = () => {
    const base = typical();
    const names = ["maria", "chen", "olu", "ingrid", "tomas", "yuki", "fatima", "leo", "release-bot", "nadia"];
    const users = [...base.users];
    const grants = [...base.grants];
    let grantId = grants.length + 1;
    names.forEach((name, i) => {
        const id = users.length + 1;
        users.push({id, name, createdAt: day("2026-08-01T09:00:00Z") + i * 86400000 * 3});
        const roles = [[3, [4]], [4, []], [5, [2, 3]], [2, [4]], [6, []]];
        const count = 1 + (i % 4);
        for (let k = 0; k < count; k++) {
            const [templateId, spaces] = roles[(i + k) % roles.length];
            const argId = templateId === 2 ? SPACE_ADMIN_SPACES_ARG : UI_ARG.spaces;
            const hasArg = base.templates.find((t) => t.id === templateId).spec.arguments.length > 0;
            grants.push(templateGrant(grantId++, id, templateId, hasArg ? [spacesBinding(argId, spaces)] : [], 1));
        }
        if (i % 5 === 4) {
            grants.push(directGrant(grantId++, id, rule(list(V.view, V.view_logs), list(2, 3, 4), list(E.deployment), all(), true), 1));
        }
    });
    base.spaces.push({id: 5, name: "sandbox"}, {id: 6, name: "analytics"}, {id: 7, name: "customer-a"});
    base.templates.push(
        {
            id: 7, name: "release_manager", builtin: false, author: 1, createdAt: day("2026-08-04T09:00:00Z"),
            spec: {
                arguments: [{id: UI_ARG.spaces, name: "spaces", kind: SPACES_ARG_KIND}, {id: UI_ARG.entityRefs, name: "entity_refs", kind: REFS_ARG_KIND}],
                rules: [
                    templateRule(list(V.view, V.update), arg(UI_ARG.spaces), list(E.deployment), arg(UI_ARG.entityRefs), true),
                    templateRule(list(V.view), arg(UI_ARG.spaces), list(E.config, E.asset, E.secret), all(), true),
                ],
            },
        },
        {
            id: 8, name: "auditor", builtin: false, author: 4, createdAt: day("2026-08-12T09:00:00Z"),
            spec: {arguments: [], rules: [templateRule(list(V.view), all(), allExcept(E.secret), all(), false), templateRule(list(V.view_logs), allExcept(0, 2), list(E.deployment), all(), false)]},
        },
        {
            id: 9, name: "host_access_prod", builtin: false, author: 1, createdAt: day("2026-08-20T09:00:00Z"),
            spec: {arguments: [], rules: [templateRule(list(V.use_host_mounts, V.use_host_network), list(2), list(E.deployment), all(), false)]},
        },
    );
    base.globalRules.push(
        globalRule(4, "freeze_customer_a",
            deny(list(V.update, V.delete, V.create), list(7), all(), all(), false),
            1, day("2026-09-01T09:00:00Z")),
        globalRule(5, "everyone_sees_sandbox",
            rule(list(V.view, V.view_logs), list(5), allExcept(E.secret), all(), true),
            1, day("2026-09-03T09:00:00Z")),
    );
    return {...base, users, grants};
};

// empty: a fresh install a minute after bootstrap.
const empty = () => ({
    spaces: [{id: 0, name: "_system"}, {id: 1, name: "global"}],
    users: [{id: 1, name: "joss", createdAt: day("2026-09-14T08:00:00Z")}],
    templates: builtinTemplates(),
    grants: [templateGrant(1, 1, 1, [], 0, day("2026-09-14T08:00:00Z"))],
    globalRules: [
        globalRule(1, "default_user_visibility",
            rule(list(V.view), list(0), list(E.user), all(), true),
            0, day("2026-09-14T08:00:00Z")),
    ],
});

export const DATASETS = {typical, busy, empty};

// --- store population -----------------------------------------------------------

export function populate(datasetKey) {
    const data = (DATASETS[datasetKey] || typical)();
    spacesS.val = data.spaces;
    usersMapS.val = new Map(data.users.map((u) => [u.id, {id: u.id, name: u.name, createdAt: u.createdAt}]));
    authzTemplatesS.val = data.templates;
    authzGrantsS.val = data.grants;
    authzGlobalRulesS.val = data.globalRules;
    return data;
}

// --- capi replacement -----------------------------------------------------------

const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const fail = (status, code, message) => {
    const e = new Error(`${status} ${code}: ${message}`);
    e.status = status;
    return e;
};

// Only cluster_admin counts as access-managing here, which is narrower than
// the backend guard (any grant conferring create on access) but enough for the
// datasets above.
const managesAccess = (grant) => grantTemplateId(grant) === 1;

// A deny whose entity types cover access control is refused, as the backend does.
const denyTargetsAccess = (r) => {
    if (!ruleEffect(r).deny) return false;
    const types = readSelector(r.selector?.entityTypes, "entityTypes");
    const listed = types.values.map(Number).includes(E.access);
    return types.mode === "all" || (types.mode === "allExcept" && !listed) || (types.mode === "list" && listed);
};

// installMockApi(capi, {author, delayMs}) — `author()` is the signed-in user id
// for attribution on new records.
export function installMockApi(capi, {author = () => 1, delayMs = 300} = {}) {
    const nextId = (items) => items.reduce((max, item) => Math.max(max, Number(item.id) || 0), 0) + 1;
    Object.assign(capi, {
        async postV1AccessGrantTemplatesCreate({name, spec}) {
            await delay(delayMs);
            if (!name?.trim()) throw fail(400, "invalid_name", "a role needs a name");
            if (authzTemplatesS.val.some((t) => t.name === name)) throw fail(409, "duplicate_name", `a role named ${name} already exists`);
            const record = {id: nextId(authzTemplatesS.val), name, builtin: false, author: author(), createdAt: Date.now(), spec};
            authzTemplatesS.val = [...authzTemplatesS.val, record];
            return record;
        },
        async postV1AccessGrantTemplatesUpdate({id, name, spec}) {
            await delay(delayMs);
            const existing = authzTemplatesS.val.find((t) => Number(t.id) === Number(id));
            if (!existing) throw fail(404, "not_found", "no such role");
            if (existing.builtin) throw fail(409, "builtin", "built-in roles cannot be changed");
            const record = {...existing, name, spec};
            authzTemplatesS.val = authzTemplatesS.val.map((t) => t === existing ? record : t);
            return record;
        },
        async postV1AccessGrantTemplatesDelete({id}) {
            await delay(delayMs);
            const existing = authzTemplatesS.val.find((t) => Number(t.id) === Number(id));
            if (!existing) throw fail(404, "not_found", "no such role");
            if (existing.builtin) throw fail(409, "builtin", "built-in roles cannot be deleted");
            if (authzGrantsS.val.some((g) => grantTemplateId(g) === Number(id))) {
                throw fail(409, "template_in_use", "roles referenced by grants cannot be deleted");
            }
            authzTemplatesS.val = authzTemplatesS.val.filter((t) => t !== existing);
            return {};
        },
        async postV1AccessGrantsCreate({userId, grant}) {
            await delay(delayMs);
            if (!usersMapS.val.has(Number(userId))) throw fail(404, "not_found", "no such user");
            const templateId = Number(grant?.value?.template?.templateId || 0);
            if (templateId && !authzTemplatesS.val.some((t) => Number(t.id) === templateId)) throw fail(404, "not_found", "no such role");
            const record = {id: nextId(authzGrantsS.val), userId: Number(userId), author: author(), createdAt: Date.now(), grant};
            authzGrantsS.val = [...authzGrantsS.val, record];
            return record;
        },
        async postV1AccessGrantsDelete({id}) {
            await delay(delayMs);
            const existing = authzGrantsS.val.find((g) => Number(g.id) === Number(id));
            if (!existing) throw fail(404, "not_found", "no such grant");
            if (managesAccess(existing) && !authzGrantsS.val.some((g) => g !== existing && managesAccess(g))) {
                throw fail(409, "access_last_admin", "this is the last grant able to manage access");
            }
            authzGrantsS.val = authzGrantsS.val.filter((g) => g !== existing);
            return {};
        },
        async postV1AccessGlobalRulesCreate({name, rule: r}) {
            await delay(delayMs);
            if (!name?.trim()) throw fail(400, "invalid_name", "a rule needs a name");
            if (denyTargetsAccess(r)) throw fail(400, "deny_targets_access", "a deny rule cannot target access control");
            const record = {id: nextId(authzGlobalRulesS.val), name, author: author(), createdAt: Date.now(), rule: r};
            authzGlobalRulesS.val = [...authzGlobalRulesS.val, record];
            return record;
        },
        async postV1AccessGlobalRulesDelete({id}) {
            await delay(delayMs);
            const existing = authzGlobalRulesS.val.find((g) => Number(g.id) === Number(id));
            if (!existing) throw fail(404, "not_found", "no such rule");
            authzGlobalRulesS.val = authzGlobalRulesS.val.filter((g) => g !== existing);
            return {};
        },
    });
}
