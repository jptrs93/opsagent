import assert from "node:assert/strict";
import test from "node:test";
import {
    describeGrant,
    describeSelector,
    formatGlobalRule,
    formatRule,
    formatSelector,
    groupGrantsByUser,
    positionValueName,
    templateArguments,
} from "./authz.js";

const SPACES = new Map([[0, "opendeploy"], [2, "default"], [3, "staging"]]);

const wildcard = () => ({wildcard: true, argumentId: 0, include: [], exclude: []});
const include = (...values) => ({wildcard: false, argumentId: 0, include: values, exclude: []});
const argument = (id) => ({wildcard: false, argumentId: id, include: [], exclude: []});

const spaceAdminTemplate = {
    id: 2,
    name: "space_admin",
    builtin: true,
    template: {
        arguments: [{id: 1, name: "spaces"}],
        rules: [
            {permissions: wildcard(), spaces: argument(1), entityTypes: wildcard(), entityRefs: wildcard(), delegationAllowed: false},
            {
                permissions: {wildcard: true, argumentId: 0, include: [], exclude: [3]},
                spaces: argument(1),
                entityTypes: wildcard(),
                entityRefs: wildcard(),
                delegationAllowed: true,
            },
        ],
    },
};

test("positionValueName resolves each vocabulary", () => {
    assert.equal(positionValueName("permissions", 3, SPACES), "reveal");
    assert.equal(positionValueName("entityTypes", 2, SPACES), "deployment");
    assert.equal(positionValueName("spaces", 3, SPACES), "staging");
    assert.equal(positionValueName("spaces", 9, SPACES), "9");
    assert.equal(positionValueName("entityRefs", 42, SPACES), "42");
});

test("formatSelector covers wildcard, lists, arguments, and exclusions", () => {
    assert.equal(formatSelector(wildcard(), "spaces", {spaceNames: SPACES}), "*");
    assert.equal(formatSelector(include(1, 4), "permissions", {}), "view,edit");
    assert.equal(
        formatSelector({wildcard: true, argumentId: 0, include: [], exclude: [3]}, "permissions", {}),
        "*-reveal");
    assert.equal(
        formatSelector(argument(1), "spaces", {argNames: new Map([[1, "spaces"]])}),
        "${spaces}");
    assert.equal(formatSelector(argument(7), "spaces", {}), "${arg_7}");
    assert.equal(formatSelector({wildcard: false, argumentId: 0, include: [], exclude: []}, "spaces", {}), "∅");
    assert.equal(formatSelector(null, "spaces", {}), "∅");
});

test("formatRule renders the five-position grammar", () => {
    const rule = {
        permissions: {wildcard: true, argumentId: 0, include: [], exclude: [3]},
        spaces: {wildcard: true, argumentId: 0, include: [], exclude: [0]},
        entityTypes: wildcard(),
        entityRefs: wildcard(),
        delegationAllowed: true,
    };
    assert.equal(formatRule(rule, {spaceNames: SPACES}), "*-opendeploy:*:*:*-reveal:true");
});

test("formatGlobalRule omits the delegation position", () => {
    const rule = {
        permissions: include(3),
        spaces: include(0),
        entityTypes: include(3),
        entityRefs: wildcard(),
        delegatedOnly: true,
    };
    assert.equal(formatGlobalRule(rule, {spaceNames: SPACES}), "opendeploy:secret:*:reveal");
});

test("describeSelector reads naturally", () => {
    assert.equal(describeSelector(wildcard(), "permissions", SPACES), "everything");
    assert.equal(describeSelector(wildcard(), "spaces", SPACES), "everywhere");
    assert.equal(
        describeSelector({wildcard: true, argumentId: 0, include: [], exclude: [3]}, "permissions", SPACES),
        "everything except reveal");
    assert.equal(describeSelector(include(2, 3), "spaces", SPACES), "default, staging");
    assert.equal(describeSelector(null, "spaces", SPACES), "nothing");
});

test("templateArguments derives position kinds from selector references", () => {
    assert.deepEqual(templateArguments(spaceAdminTemplate.template), [
        {id: 1, name: "spaces", kind: "spaces"},
    ]);
    assert.deepEqual(templateArguments({arguments: [{id: 1, name: "unused"}], rules: []}), []);
    assert.deepEqual(templateArguments(null), []);
});

test("describeGrant fills template arguments with bound values", () => {
    const templates = new Map([[2, spaceAdminTemplate]]);
    const chip = describeGrant({
        id: 10,
        userId: 7,
        templateId: 2,
        grant: {args: [{argumentId: 1, values: [2, 3]}], rule: null},
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
        templateId: 0,
        grant: {
            args: [],
            rule: {
                permissions: include(1),
                spaces: include(3),
                entityTypes: wildcard(),
                entityRefs: wildcard(),
                delegationAllowed: false,
            },
        },
    }, new Map(), SPACES);
    assert.equal(chip.template, false);
    assert.equal(chip.label, "view");
    assert.equal(chip.detail, "everything · staging");
    assert.equal(chip.title, "staging:*:*:view:false");
    assert.equal(chip.delegable, false);
});

test("describeGrant survives a missing template", () => {
    const chip = describeGrant({id: 12, userId: 7, templateId: 99, grant: {args: [], rule: null}}, new Map(), SPACES);
    assert.equal(chip.label, "template 99");
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
