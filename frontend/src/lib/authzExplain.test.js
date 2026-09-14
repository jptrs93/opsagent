import assert from "node:assert/strict";
import test from "node:test";
import {ENTITY_TYPES, VERBS} from "./authz.js";
import {
    APPLICABLE,
    PAIR_GLOSS,
    SPACE_APPLICABLE,
    SYSTEM_APPLICABLE,
    explainMatrix,
    grantSubject,
    mergeGrid,
    notApplicableReason,
    resolveSelector,
    ruleDefinition,
} from "./authzExplain.js";

const V = Object.fromEntries(VERBS.map((v) => [v.name, v.id]));
const E = Object.fromEntries(ENTITY_TYPES.map((e) => [e.name, e.id]));

const all = () => ({wildcard: true, argumentId: 0, include: [], exclude: []});
const allExcept = (...ids) => ({wildcard: true, argumentId: 0, include: [], exclude: ids});
const list = (...ids) => ({wildcard: false, argumentId: 0, include: ids, exclude: []});
const arg = (id) => ({wildcard: false, argumentId: id, include: [], exclude: []});
const rule = (permissions, spaces, entityTypes, entityRefs, extra = {}) =>
    ({permissions, spaces, entityTypes, entityRefs, delegationAllowed: false, ...extra});

const SPACES = [{id: 0, name: "_system"}, {id: 1, name: "global"}, {id: 2, name: "prod"}, {id: 3, name: "staging"}];
const spaceNames = new Map(SPACES.map((s) => [s.id, s.name]));
const opts = {spaceNames, spaces: SPACES};

const tabKeys = (m) => m.tabs.map((t) => t.key);
const tab = (m, key) => m.tabs.find((t) => t.key === key);
const cellsOf = (t) => mergeGrid(t.grid).cells;

test("every applicable pair has a gloss and every gloss names an applicable pair", () => {
    const applicable = new Set();
    for (const [type, verbs] of Object.entries(APPLICABLE)) for (const verb of verbs) applicable.add(`${type}:${verb}`);
    for (const key of applicable) assert.ok(PAIR_GLOSS[key], `${key} has no gloss`);
    for (const key of Object.keys(PAIR_GLOSS)) assert.ok(applicable.has(key), `${key} is glossed but never checked`);
});

test("the system and space tables partition the applicable pairs", () => {
    for (const [type, verbs] of Object.entries(APPLICABLE)) {
        const checked = new Set([...(SYSTEM_APPLICABLE[type] || []), ...(SPACE_APPLICABLE[type] || [])]);
        assert.deepEqual([...checked].sort(), [...verbs].sort(), `${type} is checked somewhere for each verb`);
    }
    for (const [type, verbs] of Object.entries(SYSTEM_APPLICABLE)) for (const verb of verbs) assert.ok(APPLICABLE[type].includes(verb), `${type}:${verb}`);
    for (const [type, verbs] of Object.entries(SPACE_APPLICABLE)) for (const verb of verbs) assert.ok(APPLICABLE[type].includes(verb), `${type}:${verb}`);
});

test("node logs are carried by the system deployments, not the node", () => {
    assert.ok(!APPLICABLE.node.includes("view_logs"));
    assert.ok(SYSTEM_APPLICABLE.deployment.includes("view_logs"));
    assert.equal(notApplicableReason("view_logs", "node"), "view_logs applies to deployments only");
});

test("resolveSelector reads each mode and marks the universe tokens", () => {
    const names = (v) => VERBS.find((x) => x.id === Number(v))?.name;
    const universe = VERBS;
    assert.equal(resolveSelector(all(), "permissions", {names, universe}).mode, "all");
    assert.ok(resolveSelector(all(), "permissions", {names, universe}).tokens.every((t) => t.state === "on"));

    const except = resolveSelector(allExcept(V.reveal), "permissions", {names, universe});
    assert.equal(except.mode, "allExcept");
    assert.equal(except.tokens.find((t) => t.name === "reveal").state, "excluded");
    assert.equal(except.tokens.find((t) => t.name === "view").state, "on");

    const listed = resolveSelector(list(V.view), "permissions", {names, universe});
    assert.equal(listed.mode, "list");
    assert.equal(listed.tokens.find((t) => t.name === "view").state, "on");
    assert.equal(listed.tokens.find((t) => t.name === "create").state, "off");

    const open = resolveSelector(arg(4), "permissions", {names, universe, argNames: new Map([[4, "actions"]])});
    assert.equal(open.mode, "arg");
    assert.equal(open.arg.name, "actions");

    const bound = resolveSelector(arg(4), "permissions", {names, universe, bindings: new Map([[4, [V.view]]])});
    assert.equal(bound.mode, "list");
    assert.ok(bound.bound);
    assert.deepEqual(bound.include.map((i) => i.name), ["view"]);
});

test("ruleDefinition says which sessions a rule reaches", () => {
    const sessions = (r) => { const d = ruleDefinition(r, opts); return [d.users, d.agents]; };
    assert.deepEqual(sessions(rule(all(), all(), all(), all())), [true, false]);
    assert.deepEqual(sessions(rule(all(), all(), all(), all(), {delegationAllowed: true})), [true, true]);
    assert.deepEqual(sessions(rule(all(), all(), all(), all(), {deny: true})), [true, true]);
    assert.deepEqual(sessions(rule(all(), all(), all(), all(), {deny: true, delegatedOnly: true})), [false, true]);
});

test("a rule over every space gets a System tab first and one Any space tab", () => {
    const m = explainMatrix([rule(all(), all(), all(), all())], opts);
    assert.equal(m.deny, false);
    assert.deepEqual(tabKeys(m), ["system", "1-2-3"]);
    assert.deepEqual(m.tabs.map((t) => t.label), ["System", "Any space"]);
    assert.ok(tab(m, "system").system);

    const system = cellsOf(tab(m, "system"));
    assert.equal(system["node:view"].state, "user");
    assert.equal(system["node:view_logs"].state, "na");
    assert.equal(system["deployment:create"].state, "na");
    assert.equal(system["deployment:update"].state, "user");
    assert.equal(system["deployment:use_host_network"].state, "user");
    assert.equal(system["secret:view"].state, "na");
    assert.equal(system["space:create"].state, "user");
    assert.equal(system["space:update"].state, "na");
    assert.deepEqual(tab(m, "system").grid.types.map((t) => t.name), ["space", "deployment", "node", "cluster", "user", "access"]);

    const any = cellsOf(tab(m, "1-2-3"));
    assert.equal(any["space:create"].state, "na");
    assert.equal(any["deployment:create"].state, "user");
    assert.equal(any["node:view"].state, "na");
});

test("a delegable rule reaches both sessions and a scoped one leaves the rest out", () => {
    const m = explainMatrix([rule(list(V.view, V.update), list(2), list(E.deployment), list(12, 14), {delegationAllowed: true})], opts);
    assert.deepEqual(tabKeys(m), ["2"]);
    assert.equal(tab(m, "2").label, "prod");
    const cells = cellsOf(tab(m, "2"));
    assert.equal(cells["deployment:view"].state, "both");
    assert.equal(cells["deployment:delete"].state, "none");
    assert.equal(cells["secret:view"].state, "none");
    assert.deepEqual(cells["deployment:view"].markers, [1]);
    assert.deepEqual(tab(m, "2").grid.legend, [{marker: 1, text: "only #12, #14"}]);
});

test("an open spaces argument gets its own tab and a System tab led by the argument", () => {
    const argNames = new Map([[1, "spaces"]]);
    const rules = [rule(allExcept(V.use_host_mounts, V.use_host_network), arg(1), all(), all())];
    const m = explainMatrix(rules, {...opts, argNames});
    assert.deepEqual(tabKeys(m), ["argSystem", "arg"]);
    assert.deepEqual(m.tabs.map((t) => t.label), ["System", "${spaces}"]);
    assert.equal(tab(m, "argSystem").lead, "Only applies when _system is one of the ${spaces} arguments.");
    assert.equal(tab(m, "argSystem").leadArg, "spaces");
    const cells = cellsOf(tab(m, "arg"));
    assert.equal(cells["deployment:update"].state, "user");
    assert.equal(cells["deployment:use_host_mounts"].state, "none");

    const bound = explainMatrix(rules, {...opts, argNames, bindings: new Map([[1, [2, 3]]])});
    assert.deepEqual(tabKeys(bound), ["2-3"]);
    assert.equal(tab(bound, "2-3").label, "prod, staging");
    assert.equal(tab(bound, "2-3").subtitle, "in prod, staging");
});

test("an argument in another position shows as depending on it", () => {
    const argNames = new Map([[2, "actions"]]);
    const m = explainMatrix([rule(arg(2), list(2), list(E.deployment), all())], {...opts, argNames});
    const cells = cellsOf(tab(m, "2"));
    assert.equal(cells["deployment:view"].state, "arg");
    assert.match(cells["deployment:view"].title, /\$\{actions\}/);
});

test("an agent-only deny reaches agents and not the person", () => {
    const m = explainMatrix([rule(list(V.delete), list(2), list(E.secret, E.config), all(), {deny: true, delegatedOnly: true})], opts);
    assert.equal(m.deny, true);
    assert.deepEqual(tabKeys(m), ["2"]);
    const cells = cellsOf(tab(m, "2"));
    assert.equal(cells["secret:delete"].state, "agents");
    assert.equal(cells["config:delete"].state, "agents");
    assert.equal(cells["deployment:delete"].state, "none");
});

test("a rule that covers nothing gets no tabs", () => {
    assert.deepEqual(explainMatrix([rule(list(), all(), all(), all())], opts).tabs, []);
    assert.deepEqual(explainMatrix([rule(all(), list(9), all(), all())], opts).tabs, []);
});

test("a role's rules fold into one table per space", () => {
    const rules = [
        rule(all(), all(), all(), all()),
        rule(allExcept(V.view_logs, V.reveal), allExcept(0), allExcept(E.secret), all(), {delegationAllowed: true}),
    ];
    const m = explainMatrix(rules, opts);
    assert.deepEqual(tabKeys(m), ["system", "1-2-3"]);
    const any = cellsOf(tab(m, "1-2-3"));
    assert.equal(any["deployment:view"].state, "both");
    assert.equal(any["deployment:view_logs"].state, "user");
    assert.equal(any["secret:view"].state, "user");
    assert.equal(cellsOf(tab(m, "system"))["node:view"].state, "user");
});

test("grantSubject binds a role grant and names a direct rule", () => {
    const template = {
        id: 2, name: "space_admin",
        template: {arguments: [{id: 1, name: "spaces", kind: "spaces"}], rules: [rule(all(), arg(1), all(), all())]},
    };
    const templatesById = new Map([[2, template]]);
    const bound = grantSubject({templateId: 2, grant: {args: [{argumentId: 1, values: [2]}]}}, {templatesById, spaceNames, spaces: SPACES});
    assert.equal(bound.subtitle, "Role space_admin with ${spaces} = prod");
    assert.deepEqual([...bound.bindings.entries()], [[1, [2]]]);
    assert.equal(bound.argNames.get(1), "spaces");
    assert.equal(bound.rules.length, 1);

    const direct = grantSubject({templateId: 0, grant: {rule: rule(list(V.view), all(), all(), all())}}, {templatesById, spaceNames, spaces: SPACES});
    assert.equal(direct.subtitle, "A single rule granted directly, not through a role.");
    assert.equal(direct.rules.length, 1);

    const gone = grantSubject({templateId: 9, grant: {}}, {templatesById, spaceNames, spaces: SPACES});
    assert.equal(gone.rules.length, 0);
});
