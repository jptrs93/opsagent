import test from "node:test";
import assert from "node:assert/strict";
import {ASSET_COLUMNS, assetDirsAsNamed, fmtSize, makeAssetItems} from "./assetExplorer.js";
import {buildRows, flexColumnKey} from "./valueExplorer.js";

// Fixtures use the view-model shape state/deployments.js derives from the
// wire Asset: key/spaceId/directoryId flattened, contentVersions newest first.
const meta = (id, key, spaceId, directoryId, refs) => ({id, key, spaceId, directoryId, contentVersions: refs});
const ref = (id, version, extra = {}) => ({id, version, createdAt: extra.createdAt ?? new Date(1000),
    sizeBytes: extra.sizeBytes ?? 10});

test("makeAssetItems takes latest version facts and skips versionless metas", () => {
    const items = makeAssetItems([
        meta(1, "app.yaml", 1, 10, [ref(12, 3, {sizeBytes: 42, createdAt: new Date(3000)}), ref(11, 2)]),
        meta(2, "big.bin", 1, 0, [ref(21, 1, {sizeBytes: 11 * 1024 * 1024})]),
        meta(3, "pending.bin", 1, 0, []),
    ]);
    assert.deepEqual(items.map((i) => i.name), ["app.yaml", "big.bin"]);
    assert.equal(items[0].kind, "asset");
    assert.equal(items[0].version, 3);
    assert.equal(items[0].sizeBytes, 42);
    assert.equal(items[0].directoryId, 10);
    assert.equal(items[0].createdAt.getTime(), 3000);
    assert.equal(items[0].large, false);
    assert.equal(items[1].large, true);
});

test("asset directories map key onto the name the shared helpers expect", () => {
    const named = assetDirsAsNamed([{id: 5, spaceId: 1, key: "bundles", parentId: 0}]);
    assert.equal(named[0].name, "bundles");
    assert.equal(named[0].key, "bundles");
});

test("buildRows without a type filter treats only the query as narrowing", () => {
    const spaces = [{id: 1, name: "default"}];
    const dirs = assetDirsAsNamed([
        {id: 10, spaceId: 1, key: "nginx", parentId: 0},
        {id: 11, spaceId: 1, key: "empty", parentId: 0},
    ]);
    const items = makeAssetItems([
        meta(1, "site.conf", 1, 10, [ref(12, 1)]),
        meta(2, "root.txt", 1, 0, [ref(21, 1)]),
    ]);
    const build = (overrides = {}) => buildRows({
        spaces, dirs, items,
        hiddenSpaceIds: new Set(),
        query: "",
        expanded: new Set(["space:1"]),
        sort: {key: "name", dir: "asc"},
        ...overrides,
    });

    // At rest the empty folder is visible with a count of 0 and nothing is
    // reported as type-hidden.
    const atRest = build();
    assert.deepEqual(atRest.rows.map((r) => r.key), ["space:1", "dir:11", "dir:10", "asset:2"]);
    assert.equal(atRest.rows.find((r) => r.key === "dir:11").count, 0);
    assert.equal(atRest.hiddenByType, 0);

    // A query narrows: folders that expand to nothing disappear, survivors
    // force-expand.
    const searched = build({query: "site"});
    assert.deepEqual(searched.rows.map((r) => r.key), ["space:1", "dir:10", "asset:1"]);
    assert.ok(searched.rows.find((r) => r.key === "dir:10").expanded);
});

test("size sort orders siblings by latest version size", () => {
    const spaces = [{id: 1, name: "default"}];
    const items = makeAssetItems([
        meta(1, "small.txt", 1, 0, [ref(11, 1, {sizeBytes: 5})]),
        meta(2, "large.bin", 1, 0, [ref(21, 1, {sizeBytes: 500})]),
    ]);
    const {rows} = buildRows({
        spaces, dirs: [], items,
        hiddenSpaceIds: new Set(),
        query: "",
        expanded: new Set(["space:1"]),
        sort: {key: "size", dir: "desc"},
    });
    assert.deepEqual(rows.filter((r) => r.type === "item").map((r) => r.key), ["asset:2", "asset:1"]);
});

test("fmtSize matches the thresholds the old assets table used", () => {
    assert.equal(fmtSize(0), "0 B");
    assert.equal(fmtSize(999), "999 B");
    assert.equal(fmtSize(1500), "1.5 KB");
    assert.equal(fmtSize(2_500_000), "2.50 MB");
});

test("the asset column catalogue flexes its last visible non-fixed column", () => {
    assert.equal(flexColumnKey(new Set(["name", "version", "created", "uses", "size", "actions"]), ASSET_COLUMNS), "size");
    assert.equal(flexColumnKey(new Set(["name", "actions"]), ASSET_COLUMNS), "name");
});
