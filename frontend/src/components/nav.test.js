import test from "node:test";
import assert from "node:assert/strict";
import {NAV_GROUPS, groupOfPage, itemOfPage} from "./nav.js";

test("page keys are unique across groups", () => {
    const keys = NAV_GROUPS.flatMap(group => group.items.map(item => item.key));
    assert.equal(new Set(keys).size, keys.length);
});

test("every group has between three and six items", () => {
    for (const group of NAV_GROUPS) {
        assert.ok(group.items.length >= 3 && group.items.length <= 6, `${group.key} has ${group.items.length} items`);
    }
});

test("groupOfPage and itemOfPage resolve the tree", () => {
    assert.equal(groupOfPage("status").key, "workloads");
    assert.equal(groupOfPage("settings").key, "cluster");
    assert.equal(groupOfPage("missing"), undefined);
    assert.equal(itemOfPage("users").label, "Users & roles");
    assert.equal(itemOfPage("missing"), null);
});
