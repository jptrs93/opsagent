// The explanation model behind the rule explainer (components/ruleExplainer.js):
// resolves each position of an authz rule against its universe for the
// definition pane, and folds one or more rules into one action × resource
// grid per space for the access table, with the pairs the API actually
// checks. No van and no DOM; unit tested in authzExplain.test.js.
import {ENTITY_TYPES, VERBS, positionValueName, templateArguments} from "./authz.js";

// What each verb lets a caller do, in the words the popover uses.
export const VERB_GLOSS = {
    create: "make new ones",
    update: "change existing ones; for deployments that includes versions, start and stop",
    delete: "remove them",
    view: "see that they exist and read their details",
    view_logs: "read deployment logs, which can echo secret values",
    reveal: "read secret values in plain text",
    use_host_mounts: "mount host paths into a deployment",
    use_host_network: "run a deployment on the host network",
};

export const ENTITY_GLOSS = {
    space: "spaces themselves: creating, renaming and deleting a space",
    deployment: "deployments",
    secret: "secrets; reading a value needs reveal",
    config: "configs",
    asset: "assets",
    node: "nodes",
    cluster: "cluster settings, backup and export",
    user: "the user roster",
    access: "roles, grants and global rules",
};

// PAIR_GLOSS says, per applicable action and resource type, what holding
// that permission lets someone do, taken from the handlers that check the
// pair (webuihandler/*.go). Keyed "type:verb".
export const PAIR_GLOSS = {
    "space:create": "make a new space",
    "space:update": "rename a space, change its settings and its network policies",
    "space:delete": "delete a space",
    "space:view": "see a space and what it contains",
    "deployment:create": "make new deployments",
    "deployment:update": "edit a deployment's spec, deploy versions, start, stop and restart it",
    "deployment:delete": "delete a deployment",
    "deployment:view": "see a deployment, its versions and its history",
    "deployment:view_logs": "read a deployment's logs, build output, metrics and run reports",
    "deployment:use_host_mounts": "save a deployment spec that mounts host paths",
    "deployment:use_host_network": "save a deployment spec that runs on the host network",
    "secret:create": "make new secrets, typed in or generated",
    "secret:update": "set a new value for a secret, rename or move it",
    "secret:delete": "delete a secret",
    "secret:view": "see that a secret exists, not its value",
    "secret:reveal": "read a secret's value in plain text",
    "config:create": "make new configs",
    "config:update": "set a config's value, rename or move it",
    "config:delete": "delete a config",
    "config:view": "see a config and its value",
    "asset:create": "upload assets and make directories",
    "asset:update": "rename or move assets and directories",
    "asset:delete": "delete assets and directories",
    "asset:view": "list and download assets",
    "node:create": "accept a node's enrolment into the cluster",
    "node:update": "rename a node, choose which spaces may run on it, and drain it",
    "node:delete": "evict a node from the cluster",
    "node:view": "see nodes and pending enrolments",
    "cluster:update": "change cluster settings and the master password, unlock secrets, reset Nix stores",
    "cluster:view": "read cluster settings and the exported config",
    "user:view": "see the user roster",
    "access:create": "make roles, grants and global rules",
    "access:update": "edit roles",
    "access:delete": "delete roles, grants and global rules",
    "access:view": "list roles, grants and global rules",
};

// Requests on these entity types are checked in the _system space (0), not
// in the space of any item, so a rule that leaves space 0 out never covers them.
export const CLUSTER_LEVEL = new Set(["node", "cluster", "user", "access"]);
const SYSTEM_SPACE_ID = 0;

const typePlural = (name) => name === "access" ? name : `${name}s`;

// resolveSelector reads one selector into a mode, the listed values, and, when
// the position has a finite universe (verbs, entity types, live spaces), one
// token per universe value saying whether the rule covers it. A template
// argument reads as its bound values when bindings are supplied (explaining a
// grant) and as an open argument otherwise (explaining the role itself).
export function resolveSelector(sel, kind, {names, universe = null, argNames, bindings} = {}) {
    const item = (v) => ({id: Number(v), name: names(v)});
    const res = {mode: "none", include: [], exclude: [], arg: null, bound: false, tokens: null};
    if (sel?.argumentId) {
        const id = Number(sel.argumentId);
        res.arg = {id, name: argNames?.get?.(id) || `arg_${id}`};
        const values = bindings?.get?.(id);
        if (values) {
            res.mode = values.length ? "list" : "none";
            res.bound = true;
            res.include = values.map(item);
        } else {
            res.mode = "arg";
        }
    } else if (sel?.wildcard) {
        res.exclude = (sel.exclude || []).map(item);
        res.mode = res.exclude.length ? "allExcept" : "all";
    } else if ((sel?.include || []).length) {
        const excluded = new Set((sel.exclude || []).map(Number));
        res.include = sel.include.map(item).filter((i) => !excluded.has(i.id));
        res.mode = res.include.length ? "list" : "none";
    }
    if (universe) {
        const on = new Set(res.include.map((i) => i.id));
        const off = new Set(res.exclude.map((i) => i.id));
        res.tokens = universe.map((u) => ({
            id: Number(u.id),
            name: u.name,
            state: res.mode === "all" ? "on"
                : res.mode === "allExcept" ? (off.has(Number(u.id)) ? "excluded" : "on")
                    : res.mode === "list" ? (on.has(Number(u.id)) ? "on" : "off")
                        : "off",
        }));
    }
    return res;
}

// ruleDefinition resolves each position of a rule for the definition pane:
// the effect, the four selectors against their universes (bound values
// substituted when explaining a grant), and the sessions the rule reaches.
export function ruleDefinition(rule, {spaceNames, spaces = [], argNames, bindings} = {}) {
    const opts = {argNames, bindings};
    return {
        deny: !!rule.deny,
        actions: resolveSelector(rule.permissions, "permissions", {...opts, names: (v) => positionValueName("permissions", v), universe: VERBS}),
        types: resolveSelector(rule.entityTypes, "entityTypes", {...opts, names: (v) => positionValueName("entityTypes", v), universe: ENTITY_TYPES}),
        refs: resolveSelector(rule.entityRefs, "entityRefs", {...opts, names: (v) => String(v)}),
        spaces: resolveSelector(rule.spaces, "spaces", {...opts, names: (v) => positionValueName("spaces", v, spaceNames), universe: spaces}),
        users: rule.deny ? !rule.delegatedOnly : true,
        agents: rule.deny ? true : !!rule.delegationAllowed,
    };
}

// grantSubject describes one grant record for the explainer: the role's raw
// rules with the bindings to substitute (so a matrix can tell a bound value
// from an open argument), or the direct rule alone.
export function grantSubject(record, {templatesById, spaceNames, spaces}) {
    const base = {spaceNames, spaces};
    const templateId = Number(record?.templateId || 0);
    if (templateId) {
        const template = templatesById?.get?.(templateId);
        if (!template) return {...base, subtitle: "This role no longer exists.", rules: []};
        const args = templateArguments(template.template);
        const bindings = new Map((record?.grant?.args || []).map((b) => [Number(b.argumentId), (b.values || []).map(Number)]));
        const argNames = new Map(args.map((a) => [a.id, a.name]));
        const bound = args.map((a) => `\${${a.name}} = ${(bindings.get(a.id) || []).map((v) => positionValueName(a.kind, v, spaceNames)).join(", ") || "nothing"}`);
        return {
            ...base,
            subtitle: bound.length ? `Role ${template.name} with ${bound.join("; ")}` : `Role ${template.name}`,
            rules: template.template?.rules || [],
            bindings,
            argNames,
        };
    }
    const rule = record?.grant?.rule;
    return {
        ...base,
        subtitle: "A single rule granted directly, not through a role.",
        rules: rule ? [rule] : [],
    };
}

// --- matrix ---------------------------------------------------------------------

// APPLICABLE lists, per entity type, the verbs the API ever checks against
// it (the access checks in webuihandler plus the host permissions checked on
// deployment specs). Any other cell is one no rule can light up, so the
// matrix greys it out instead of showing a cross.
export const APPLICABLE = {
    space: ["create", "update", "delete", "view"],
    deployment: ["create", "update", "delete", "view", "view_logs", "use_host_mounts", "use_host_network"],
    secret: ["create", "update", "delete", "view", "reveal"],
    config: ["create", "update", "delete", "view"],
    asset: ["create", "update", "delete", "view"],
    node: ["create", "update", "delete", "view"],
    cluster: ["update", "view"],
    user: ["view"],
    access: ["create", "update", "delete", "view"],
};

// SYSTEM_APPLICABLE is the part of that checked in the _system space (0):
// the cluster-level types live only there, a new space is checked there
// because it has no id yet, and the per-node system deployments (opendeploy
// itself and netproxy) live there: they can be viewed, their logs read, their
// workload updated and a stale one deleted, but a user never creates them.
// Updating a deployment re-checks the host permissions its saved spec uses,
// and the system deployments use host networking and host mounts, so those
// two apply there as well. Secrets, configs and assets never live in space 0
// (the API folds it into the default space) and _system itself cannot be
// renamed or deleted.
export const SYSTEM_APPLICABLE = {
    space: ["create"],
    deployment: ["update", "delete", "view", "view_logs", "use_host_mounts", "use_host_network"],
    node: ["create", "update", "delete", "view"],
    cluster: ["update", "view"],
    user: ["view"],
    access: ["create", "update", "delete", "view"],
};

// SPACE_APPLICABLE is the part checked in a real space.
export const SPACE_APPLICABLE = {
    space: ["update", "delete", "view"],
    deployment: APPLICABLE.deployment,
    secret: APPLICABLE.secret,
    config: APPLICABLE.config,
    asset: APPLICABLE.asset,
};

// Column and row labels short enough for a cell; the full name and gloss go
// in the title.
export const VERB_SHORT = {
    create: "create", update: "update", delete: "delete", view: "view",
    view_logs: "logs", reveal: "reveal", use_host_mounts: "host mounts", use_host_network: "host net",
};
export const ENTITY_SHORT = {
    space: "space", deployment: "deploy", secret: "secret", config: "config", asset: "asset",
    node: "node", cluster: "cluster", user: "user", access: "access",
};

const NA_BY_TYPE = {
    user: "users come into existence at sign-in; the roster can only be viewed",
    cluster: "cluster settings can only be viewed or updated",
};

// notApplicableReason(verb, type, system) says why a cell is greyed: the verb
// never applies to the type, or not in this kind of space (system says which).
export const notApplicableReason = (verb, type, system = null) => {
    if (!APPLICABLE[type].includes(verb)) {
        if (verb === "reveal") return "reveal applies to secrets only";
        if (verb === "view_logs") return "view_logs applies to deployments only";
        if (verb.startsWith("use_host")) return `${verb} applies to deployments only`;
        return NA_BY_TYPE[type] || `${verb} does not apply to ${typePlural(type)}`;
    }
    if (system) {
        if (type === "deployment") return "deployments in _system are the per-node system deployments, created by opendeploy itself";
        if (type === "space") return "the _system space cannot be renamed or deleted";
        return `${typePlural(type)} live in real spaces, never in _system`;
    }
    if (type === "space") return "creating a space is checked in the _system space, since the new space has no id yet";
    return `${verb} ${type} is checked in the _system space, not here`;
};

// covers answers whether a resolved selector includes an id: true, false, or
// null when that depends on an argument bound later.
const covers = (res, id) => {
    if (res.mode === "all") return true;
    if (res.mode === "allExcept") return !res.exclude.some((i) => i.id === id);
    if (res.mode === "list") return res.include.some((i) => i.id === id);
    if (res.mode === "arg") return null;
    return false;
};
const nonEmpty = (res) => res.mode === "none" ? false : res.mode === "arg" ? null : true;

// contributes says whether a rule reaches a block: every allow rule reaches
// the user in person and, when delegable, agent sessions; a deny reaches
// agents always and people only when it is not agent-only.
const contributes = (rule, who) => rule.deny
    ? (who === "agents" || !rule.delegatedOnly)
    : (who === "user" || !!rule.delegationAllowed);

const refsScope = (refs) => {
    if (refs.mode === "list") return `only ${refs.include.map((i) => `#${i.id}`).join(", ")}`;
    if (refs.mode === "allExcept") return `except ${refs.exclude.map((i) => `#${i.id}`).join(", ")}`;
    if (refs.mode === "arg") return `only the items chosen when the role is granted`;
    return "";
};

// SYSTEM_DESCRIPTION heads the System tab.
export const SYSTEM_DESCRIPTION = "System is a built-in space that owns special resources, such as users, nodes and access grants, which are cluster-wide and not part of standard spaces. The deployments powering the cluster itself also live in this space. No one can create user-defined workload resources, such as deployments or configs, in this space.";

// explainMatrix(rules, opts) folds one or more rules (a role's, a grant's
// bound ones, or a single global rule) into one verb × entity grid per space,
// each grid two blocks: the user in person and their agent sessions. Spaces
// whose grids are identical share a tab ("Any space" when that is every real
// space); System gets its own tab when any cluster-level cell is covered,
// since that is where nodes, users, settings and access are checked; spaces
// the rules never reach get no tab. A rule whose spaces are
// an open argument gets a tab named after it.
export function explainMatrix(rules, {spaceNames, spaces = [], argNames, bindings} = {}) {
    const opts = {argNames, bindings};
    const resolved = rules.map((rule) => ({
        rule,
        actions: resolveSelector(rule.permissions, "permissions", {...opts, names: (v) => positionValueName("permissions", v), universe: VERBS}),
        types: resolveSelector(rule.entityTypes, "entityTypes", {...opts, names: (v) => positionValueName("entityTypes", v), universe: ENTITY_TYPES}),
        refs: resolveSelector(rule.entityRefs, "entityRefs", {...opts, names: (v) => String(v)}),
        spacesRes: resolveSelector(rule.spaces, "spaces", {...opts, names: (v) => positionValueName("spaces", v, spaceNames), universe: spaces}),
    }));
    const deny = rules.some((r) => r.deny);

    // inSpace says whether a rule applies in the tab's space: a concrete id,
    // or "arg" for the open-argument tab, where wildcard rules apply too.
    const inSpace = (r, spaceId) => spaceId === "arg"
        ? (r.spacesRes.mode === "arg" || r.spacesRes.mode === "all" || r.spacesRes.mode === "allExcept")
        : covers(r.spacesRes, spaceId) === true;

    // gridFor(spaceId, system) builds a tab's grid: spaceId is a space id or
    // "arg" for the open-argument tabs; system selects the cluster-level view
    // (what is checked in _system) over the space-scoped one.
    const gridFor = (spaceId, system = spaceId === SYSTEM_SPACE_ID) => {
        const block = (who) => {
            const active = resolved.filter((r) => contributes(r.rule, who) && inSpace(r, spaceId));
            const cells = {};
            for (const type of ENTITY_TYPES) {
                for (const verb of VERBS) {
                    const key = `${type.name}:${verb.name}`;
                    if (!(system ? SYSTEM_APPLICABLE : SPACE_APPLICABLE)[type.name]?.includes(verb.name)) {
                        cells[key] = {state: "na", title: notApplicableReason(verb.name, type.name, system)};
                        continue;
                    }
                    const scopes = [];
                    let unrestricted = false;
                    let argDependent = null;
                    for (const r of active) {
                        const v = covers(r.actions, verb.id);
                        const t = covers(r.types, type.id);
                        if (v === false || t === false || nonEmpty(r.refs) === false) continue;
                        if (v === null || t === null) {
                            argDependent = argDependent || (v === null ? r.actions.arg.name : r.types.arg.name);
                            continue;
                        }
                        const scope = refsScope(r.refs);
                        if (scope) scopes.push(scope); else unrestricted = true;
                    }
                    if (unrestricted || scopes.length) {
                        cells[key] = {state: deny ? "deny" : "yes", scope: unrestricted ? "" : [...new Set(scopes)].join("; or ")};
                    } else if (argDependent) {
                        cells[key] = {state: "arg", title: `Depends on \${${argDependent}}, chosen when the role is granted.`};
                    } else {
                        cells[key] = {state: "no"};
                    }
                }
            }
            return {who, cells, active: active.length};
        };
        const user = block("user");
        const agents = block("agents");
        // Markers are numbered across both blocks so a legend entry means the
        // same thing wherever it appears in the tab.
        const distinct = [...new Set([...Object.values(user.cells), ...Object.values(agents.cells)].map((c) => c.scope).filter(Boolean))];
        for (const cell of [...Object.values(user.cells), ...Object.values(agents.cells)]) {
            if (cell.scope) cell.marker = distinct.indexOf(cell.scope) + 1;
        }
        const covered = (b) => Object.values(b.cells).some((c) => c.state === "yes" || c.state === "deny" || c.state === "arg");
        // The axes a tab shows: only the types and verbs with a cell that can
        // apply there, so System lists the cluster-level types and the other
        // tabs the space-scoped ones (greyed cells are the same in both blocks).
        const applies = (type, verb) => user.cells[`${type.name}:${verb.name}`].state !== "na";
        const types = ENTITY_TYPES.filter((t) => VERBS.some((v) => applies(t, v)));
        const verbs = VERBS.filter((v) => ENTITY_TYPES.some((t) => applies(t, v)));
        const key = JSON.stringify([user.cells, agents.cells].map((cells) => Object.values(cells).map((c) => `${c.state}${c.marker || ""}`)));
        return {
            user, agents, types, verbs,
            legend: distinct.map((text, i) => ({marker: i + 1, text})),
            any: covered(user) || covered(agents),
            agentsSame: JSON.stringify(Object.values(user.cells)) === JSON.stringify(Object.values(agents.cells)) && covered(user),
            agentsNone: !covered(agents),
            key,
        };
    };

    const name = (id) => spaceNames?.get?.(id) || `space ${id}`;
    const realIds = spaces.map((s) => Number(s.id)).filter((id) => id !== SYSTEM_SPACE_ID);
    const groups = new Map();
    for (const id of realIds) {
        const grid = gridFor(id);
        if (!groups.has(grid.key)) groups.set(grid.key, {ids: [], grid});
        groups.get(grid.key).ids.push(id);
    }
    const tabs = [];
    for (const {ids, grid} of [...groups.values()].sort((a, b) => b.ids.length - a.ids.length || a.ids[0] - b.ids[0])) {
        if (!grid.any) continue;
        const names = ids.map(name);
        tabs.push({
            key: ids.join("-"),
            label: ids.length === realIds.length ? "Any space" : names.length > 3 ? `${names.slice(0, 2).join(", ")} +${names.length - 2}` : names.join(", "),
            subtitle: ids.length === realIds.length ? `every space except System: ${names.join(", ")}` : `in ${names.join(", ")}`,
            grid,
        });
    }
    // An open spaces argument gets its own pair of tabs: the space-scoped view
    // and, when the rules reach cluster-level cells, the view that applies if
    // the chosen spaces include _system.
    const argRule = resolved.find((r) => r.spacesRes.mode === "arg");
    if (argRule) {
        const argName = argRule.spacesRes.arg.name;
        tabs.push({key: "arg", label: "${" + argName + "}", subtitle: "in the spaces chosen when the role is granted", grid: gridFor("arg", false)});
        const argSystem = gridFor("arg", true);
        if (argSystem.any) tabs.unshift({key: "argSystem", label: "System", system: true, lead: `Only applies when _system is one of the \${${argName}} arguments.`, leadArg: argName, subtitle: SYSTEM_DESCRIPTION, grid: argSystem});
    }
    const system = gridFor(SYSTEM_SPACE_ID);
    // System always leads: cluster-level checks come before the per-space ones.
    if (system.any) tabs.unshift({key: "system", label: "System", system: true, subtitle: SYSTEM_DESCRIPTION, grid: system});
    // Spaces the rules never reach get no tab; tabs is empty when the rules
    // cover nothing anywhere.
    return {deny, tabs};
}

// mergeGrid folds a tab's two blocks into one cell per verb × type for the
// single-table rendering, which shows who a cell reaches instead of one row
// per block. For allow rules agent sessions never exceed the person (a
// delegable rule also applies in person), so a cell is "both", "user" or
// "none"; for deny rules an agent-only deny reaches agents and not the
// person, so a cell is "both", "agents" or "none".
export function mergeGrid(grid) {
    const hit = (c) => c.state === "yes" || c.state === "deny";
    const cells = {};
    for (const [key, u] of Object.entries(grid.user.cells)) {
        const a = grid.agents.cells[key];
        if (u.state === "na") { cells[key] = {state: "na", title: u.title}; continue; }
        if (u.state === "arg" || (a.state === "arg" && !hit(u))) { cells[key] = {state: "arg", title: u.state === "arg" ? u.title : a.title}; continue; }
        const state = hit(u) && hit(a) ? "both" : hit(u) ? "user" : hit(a) ? "agents" : "none";
        cells[key] = {
            state,
            markers: [...new Set([u.marker, a.marker].filter(Boolean))],
            scope: u.scope || a.scope || "",
            agentsArg: a.state === "arg" ? a.title : "",
        };
    }
    return {cells, legend: grid.legend};
}
