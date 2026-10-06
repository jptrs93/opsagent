import assert from "node:assert/strict";
import test from "node:test";
import {
    allExcludingSelector,
    allowEffect,
    argumentBinding,
    argumentSelector,
    bindingValues,
    denyEffect,
    describeGrant,
    describeSelector,
    entityRef,
    exactSelector,
    formatRef,
    formatRule,
    formatSelector,
    grantRevokeBlock,
    groupGrantsByUser,
    isClusterAdminGrant,
    parseEntityRefs,
    positionValueName,
    readSelector,
    refTargetForEntityType,
    ruleEffect,
    ruleGrantSource,
    templateArguments,
    templateGrantSource,
    templateSelector,
} from "./authz.js";

const SPACES = new Map([[0, "opendeploy"], [2, "default"], [3, "staging"]]);

const all = (kind) => allExcludingSelector(kind);
const allExcept = (kind, ...values) => allExcludingSelector(kind, values);
const list = (kind, ...values) => exactSelector(kind, values);
const rule = ({permissions, spaces, entityTypes, entityRefs}, effect = allowEffect(false)) =>
    ({effect, selector: {permissions, spaces, entityTypes, entityRefs}});
const templateRule = ({permissions, spaces, entityTypes, entityRefs}, effect = allowEffect(false)) => ({
    effect,
    selector: {
        permissions: permissions.value ? permissions : templateSelector(permissions),
        spaces: spaces.value ? spaces : templateSelector(spaces),
        entityTypes: entityTypes.value ? entityTypes : templateSelector(entityTypes),
        entityRefs: entityRefs.value ? entityRefs : templateSelector(entityRefs),
    },
});

const spaceAdminTemplate = {
    id: 2,
    name: "space_admin",
    builtin: true,
    spec: {
        arguments: [{id: 1, name: "spaces", kind: 2}],
        rules: [
            templateRule({permissions: all("permissions"), spaces: argumentSelector(1), entityTypes: all("entityTypes"), entityRefs: all("entityRefs")}),
            templateRule({
                permissions: allExcept("permissions", 6),
                spaces: argumentSelector(1),
                entityTypes: all("entityTypes"),
                entityRefs: all("entityRefs"),
            }, allowEffect(true)),
        ],
    },
};

test("readSelector reads plain and template positions", () => {
    assert.deepEqual(readSelector(all("spaces"), "spaces"), {mode: "all", values: [], argumentId: 0});
    assert.deepEqual(readSelector(allExcept("permissions", 6), "permissions"), {mode: "allExcept", values: [6], argumentId: 0});
    assert.deepEqual(readSelector(list("entityTypes", 2, 3), "entityTypes"), {mode: "list", values: [2, 3], argumentId: 0});
    assert.deepEqual(readSelector(list("spaces"), "spaces"), {mode: "none", values: [], argumentId: 0});
    assert.deepEqual(readSelector(argumentSelector(4), "spaces"), {mode: "arg", values: [], argumentId: 4});
    assert.deepEqual(readSelector(templateSelector(list("spaces", 2)), "spaces"), {mode: "list", values: [2], argumentId: 0});
    assert.deepEqual(readSelector(null, "spaces"), {mode: "none", values: [], argumentId: 0});
    assert.deepEqual(readSelector({exactSpaces: undefined, allSpacesExcluding: undefined}, "spaces"), {mode: "none", values: [], argumentId: 0});
});

test("ruleEffect reads allow and deny", () => {
    assert.deepEqual(ruleEffect({effect: allowEffect(true)}), {deny: false, delegatedOnly: false, delegationAllowed: true});
    assert.deepEqual(ruleEffect({effect: denyEffect(true)}), {deny: true, delegatedOnly: true, delegationAllowed: false});
    assert.deepEqual(ruleEffect({}), {deny: false, delegatedOnly: false, delegationAllowed: false});
});

test("entity refs format, parse, and resolve their target", () => {
    assert.equal(formatRef(entityRef("deployment", 12)), "deployment#12");
    assert.equal(formatRef(entityRef("systemConfig", 1)), "cluster#1");
    assert.equal(formatRef({target: {value: {}}}), "?");
    assert.deepEqual(parseEntityRefs("deployment#4, secret:7 node#x 9"), [entityRef("deployment", 4), entityRef("secret", 7)]);
    assert.deepEqual(parseEntityRefs("4 7", refTargetForEntityType(2)), [entityRef("deployment", 4), entityRef("deployment", 7)]);
    assert.equal(refTargetForEntityType(9), null);
    assert.equal(refTargetForEntityType(7).key, "systemConfig");
});

test("positionValueName resolves each vocabulary", () => {
    assert.equal(positionValueName("permissions", 6, SPACES), "reveal");
    assert.equal(positionValueName("permissions", 7, SPACES), "use_host_mounts");
    assert.equal(positionValueName("permissions", 8, SPACES), "use_host_network");
    assert.equal(positionValueName("entityTypes", 2, SPACES), "deployment");
    assert.equal(positionValueName("spaces", 3, SPACES), "staging");
    assert.equal(positionValueName("spaces", 9, SPACES), "9");
    assert.equal(positionValueName("entityRefs", entityRef("secret", 42), SPACES), "secret#42");
});

test("formatSelector covers everything, lists, arguments, and exclusions", () => {
    assert.equal(formatSelector(all("spaces"), "spaces", {spaceNames: SPACES}), "*");
    assert.equal(formatSelector(list("permissions", 4, 2), "permissions", {}), "view,update");
    assert.equal(formatSelector(list("permissions", 7, 8), "permissions", {}), "use_host_mounts,use_host_network");
    assert.equal(formatSelector(allExcept("permissions", 7, 8), "permissions", {}), "*-use_host_mounts-use_host_network");
    assert.equal(formatSelector(allExcept("permissions", 6), "permissions", {}), "*-reveal");
    assert.equal(formatSelector(argumentSelector(1), "spaces", {argNames: new Map([[1, "spaces"]])}), "${spaces}");
    assert.equal(formatSelector(argumentSelector(7), "spaces", {}), "${arg_7}");
    assert.equal(formatSelector(list("spaces"), "spaces", {}), "∅");
    assert.equal(formatSelector(null, "spaces", {}), "∅");
    assert.equal(formatSelector(list("entityRefs", entityRef("deployment", 4)), "entityRefs", {}), "deployment#4");
});

test("formatRule renders the five-position grammar on allows", () => {
    const r = rule({
        permissions: allExcept("permissions", 6),
        spaces: allExcept("spaces", 0),
        entityTypes: all("entityTypes"),
        entityRefs: all("entityRefs"),
    }, allowEffect(true));
    assert.equal(formatRule(r, {spaceNames: SPACES}), "*-opendeploy:*:*:*-reveal:true");
});

test("formatRule omits the delegation position on denies", () => {
    const r = rule({
        permissions: list("permissions", 6),
        spaces: list("spaces", 0),
        entityTypes: list("entityTypes", 3),
        entityRefs: all("entityRefs"),
    }, denyEffect(true));
    assert.equal(formatRule(r, {spaceNames: SPACES}), "opendeploy:secret:*:reveal");
});

test("describeSelector reads naturally", () => {
    assert.equal(describeSelector(all("permissions"), "permissions", SPACES), "everything");
    assert.equal(describeSelector(all("spaces"), "spaces", SPACES), "everywhere");
    assert.equal(describeSelector(allExcept("permissions", 6), "permissions", SPACES), "everything except reveal");
    assert.equal(describeSelector(list("spaces", 2, 3), "spaces", SPACES), "default, staging");
    assert.equal(describeSelector(null, "spaces", SPACES), "nothing");
});

test("templateArguments uses the declared kind, falling back to the references", () => {
    assert.deepEqual(templateArguments(spaceAdminTemplate.spec), [
        {id: 1, name: "spaces", kind: "spaces"},
    ]);
    assert.deepEqual(templateArguments({arguments: [{id: 1, name: "unused"}], rules: []}), []);
    assert.deepEqual(templateArguments({
        arguments: [{id: 3, name: "where"}],
        rules: [templateRule({permissions: all("permissions"), spaces: argumentSelector(3), entityTypes: all("entityTypes"), entityRefs: all("entityRefs")})],
    }), [{id: 3, name: "where", kind: "spaces"}]);
    assert.deepEqual(templateArguments(null), []);
});

test("bindings round-trip through their oneof", () => {
    const binding = argumentBinding(1, "spaces", [2, 3]);
    assert.deepEqual(binding, {argumentId: 1, values: {value: {spaces: {values: [2, 3]}}}});
    assert.deepEqual(bindingValues(binding), [2, 3]);
    assert.deepEqual(bindingValues(argumentBinding(2, "entityRefs", [entityRef("deployment", 4)])), [entityRef("deployment", 4)]);
    assert.deepEqual(bindingValues({argumentId: 1}), []);
});

test("describeGrant fills template arguments with bound values", () => {
    const templates = new Map([[2, spaceAdminTemplate]]);
    const chip = describeGrant({
        id: 10,
        userId: 7,
        grant: templateGrantSource(2, [argumentBinding(1, "spaces", [2, 3])]),
    }, templates, SPACES);
    assert.equal(chip.template, true);
    assert.equal(chip.label, "space_admin");
    assert.equal(chip.detail, "default, staging");
    assert.equal(chip.delegable, true);
    assert.match(chip.title, /\$\{spaces\}/);
});

test("describeGrant renders a direct rule naturally", () => {
    const chip = describeGrant({
        id: 11,
        userId: 7,
        grant: ruleGrantSource(rule({
            permissions: list("permissions", 4),
            spaces: list("spaces", 3),
            entityTypes: all("entityTypes"),
            entityRefs: all("entityRefs"),
        })),
    }, new Map(), SPACES);
    assert.equal(chip.template, false);
    assert.equal(chip.label, "view");
    assert.equal(chip.detail, "everything · staging");
    assert.equal(chip.title, "staging:*:*:view:false");
    assert.equal(chip.delegable, false);
});

test("describeGrant survives a missing template", () => {
    const chip = describeGrant({id: 12, userId: 7, grant: templateGrantSource(99, [])}, new Map(), SPACES);
    assert.equal(chip.label, "role 99");
});

const clusterAdminTemplate = {id: 1, name: "cluster_admin", builtin: true, spec: {rules: []}};
const TEMPLATES = new Map([[1, clusterAdminTemplate], [2, spaceAdminTemplate]]);
const adminGrant = (id, userId) => ({id, userId, grant: templateGrantSource(1, [])});

test("isClusterAdminGrant identifies the builtin role, id-only when unresolved", () => {
    assert.equal(isClusterAdminGrant(adminGrant(1, 7), TEMPLATES), true);
    assert.equal(isClusterAdminGrant({id: 2, userId: 7, grant: templateGrantSource(2, [])}, TEMPLATES), false);
    assert.equal(isClusterAdminGrant({id: 3, userId: 7, grant: ruleGrantSource(rule({}))}, TEMPLATES), false);
    assert.equal(isClusterAdminGrant(adminGrant(4, 7), new Map()), true);
    // A non-builtin role that merely borrows the name is not the real thing.
    const impostor = new Map([[5, {id: 5, name: "cluster_admin", builtin: false}]]);
    assert.equal(isClusterAdminGrant({id: 6, userId: 7, grant: templateGrantSource(5, [])}, impostor), false);
});

test("grantRevokeBlock protects your own and the last cluster_admin", () => {
    const grants = [adminGrant(1, 7), adminGrant(2, 8)];
    const opts = {grants, templatesById: TEMPLATES, selfUserId: 7};
    assert.match(grantRevokeBlock(grants[0], opts), /your own/);
    assert.equal(grantRevokeBlock(grants[1], opts), null);
    assert.match(grantRevokeBlock(grants[1], {...opts, grants: [grants[1]]}), /last cluster_admin/);
    // Other roles are never blocked, even as the only grant a user holds.
    const other = {id: 3, userId: 8, grant: templateGrantSource(2, [])};
    assert.equal(grantRevokeBlock(other, {grants: [other], templatesById: TEMPLATES, selfUserId: 8}), null);
});

test("groupGrantsByUser partitions by userId", () => {
    const grants = [
        {id: 1, userId: 7},
        {id: 2, userId: 8},
        {id: 3, userId: 7},
    ];
    const byUser = groupGrantsByUser(grants);
    assert.deepEqual([...byUser.keys()], [7, 8]);
    assert.equal(byUser.get(7).length, 2);
    assert.equal(groupGrantsByUser(null).size, 0);
});
