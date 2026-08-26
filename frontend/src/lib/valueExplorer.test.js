import test from "node:test";
import assert from "node:assert/strict";
import {
    buildRows,
    checkDrop,
    descendantDirIds,
    dirPathSegments,
    dirsById,
    dragSource,
    dropDestination,
    emptySpaceIds,
    flexColumnKey,
    folderOptions,
    itemPathSegments,
    makeItems,
} from "./valueExplorer.js";

const dir = (id, spaceId, name, parentId = 0) => ({id, spaceId, name, parentId});
const item = (kind, id, spaceId, name, directoryId = 0, extra = {}) => ({
    kind, id, spaceId, name, directoryId,
    version: extra.version ?? 1,
    createdAt: extra.createdAt ?? new Date(1000),
    value: extra.value ?? "",
});

const SPACES = [{id: 1, name: "default"}, {id: 2, name: "staging"}];
const DIRS = [dir(10, 1, "postgres"), dir(11, 1, "stripe"), dir(12, 2, "postgres")];
const ITEMS = [
    item("secret", 1, 1, "password", 10, {version: 4}),
    item("config", 2, 1, "host", 10, {value: "10.6.0.4"}),
    item("secret", 3, 1, "api-key", 11),
    item("config", 4, 1, "log-level", 0, {value: "info"}),
    item("secret", 5, 2, "password", 12),
];

const build = (overrides = {}) => buildRows({
    spaces: SPACES,
    dirs: DIRS,
    items: ITEMS,
    hiddenSpaceIds: new Set(),
    types: new Set(["secret", "config"]),
    query: "",
    expanded: new Set(),
    sort: {key: "name", dir: "asc"},
    ...overrides,
});

test("spaces render as roots with subtree counts; collapsed trees stay closed", () => {
    const {rows} = build();
    assert.deepEqual(rows.map((r) => r.key), ["space:1", "space:2"]);
    assert.equal(rows[0].count, 4);
    assert.equal(rows[1].count, 1);
});

test("hidden spaces drop whole roots", () => {
    const {rows} = build({hiddenSpaceIds: new Set([2])});
    assert.deepEqual(rows.map((r) => r.key), ["space:1"]);
});

test("expansion lists folders before items, both name-sorted", () => {
    const {rows} = build({expanded: new Set(["space:1", "dir:10"])});
    assert.deepEqual(rows.map((r) => r.key), [
        "space:1", "dir:10", "config:2", "secret:1", "dir:11", "config:4",
        "space:2",
    ]);
    const postgres = rows.find((r) => r.key === "dir:10");
    assert.equal(postgres.count, 2);
    assert.equal(postgres.depth, 1);
});

test("type filter drops folders that expand to nothing, but never spaces", () => {
    const {rows, hiddenByType} = build({
        types: new Set(["config"]),
        expanded: new Set(["space:1", "dir:10", "space:2"]),
    });
    // stripe holds only a secret, so it vanishes; staging stays with count 0.
    assert.deepEqual(rows.map((r) => r.key), ["space:1", "dir:10", "config:2", "config:4", "space:2"]);
    assert.equal(rows.find((r) => r.key === "space:2").count, 0);
    assert.equal(hiddenByType, 3);
});

test("hiddenByType ignores items in hidden spaces", () => {
    const {hiddenByType} = build({types: new Set(["config"]), hiddenSpaceIds: new Set([2])});
    assert.equal(hiddenByType, 2);
});

test("an empty folder shows at rest and disappears once the filter narrows", () => {
    const dirs = [...DIRS, dir(13, 1, "empty")];
    const atRest = build({dirs, expanded: new Set(["space:1"])});
    assert.ok(atRest.rows.some((r) => r.key === "dir:13"));
    assert.equal(atRest.rows.find((r) => r.key === "dir:13").count, 0);
    const narrowed = build({dirs, types: new Set(["secret"]), expanded: new Set(["space:1"])});
    assert.ok(!narrowed.rows.some((r) => r.key === "dir:13"));
});

test("search matches names and config values, and force-expands survivors", () => {
    const byName = build({query: "password"});
    assert.deepEqual(byName.rows.filter((r) => r.type === "item").map((r) => r.key), ["secret:1", "secret:5"]);
    assert.ok(byName.rows.find((r) => r.key === "dir:10").expanded);

    const byValue = build({query: "10.6"});
    assert.deepEqual(byValue.rows.filter((r) => r.type === "item").map((r) => r.key), ["config:2"]);
});

test("a folder whose own name matches keeps its whole subtree", () => {
    const {rows} = build({query: "stripe"});
    assert.deepEqual(rows.filter((r) => r.type === "item").map((r) => r.key), ["secret:3"]);
});

test("a folder whose own name matches survives on that alone, holding nothing", () => {
    const dirs = [...DIRS, dir(13, 1, "archive")];
    const {rows} = build({dirs, query: "archive"});
    assert.deepEqual(rows.map((r) => r.key), ["space:1", "dir:13", "space:2"]);
    assert.equal(rows.find((r) => r.key === "dir:13").count, 0);
});

test("a matching folder holds its unmatched ancestors open", () => {
    // stripe/archive matches; nothing under it does, and stripe itself does not.
    const dirs = [...DIRS, dir(13, 1, "archive", 11)];
    const {rows} = build({dirs, query: "archive"});
    assert.deepEqual(rows.map((r) => r.key), ["space:1", "dir:11", "dir:13", "space:2"]);
});

test("a name match outranks the type filter for the folder itself", () => {
    // stripe holds only a secret, so a config-only filter empties it — but the
    // folder is a search result in its own right, so it stays, showing 0.
    const {rows} = build({query: "stripe", types: new Set(["config"])});
    const stripe = rows.find((r) => r.key === "dir:11");
    assert.ok(stripe);
    assert.equal(stripe.count, 0);
    assert.ok(!rows.some((r) => r.type === "item"));
});

test("sorting orders siblings within each folder; folders reorder only under name", () => {
    const byVersion = build({sort: {key: "version", dir: "desc"}, expanded: new Set(["space:1", "dir:10"])});
    const inPostgres = byVersion.rows.filter((r) => r.depth === 2).map((r) => r.key);
    assert.deepEqual(inPostgres, ["secret:1", "config:2"]);
    // Folders keep name order under a version sort...
    assert.ok(byVersion.rows.findIndex((r) => r.key === "dir:10") < byVersion.rows.findIndex((r) => r.key === "dir:11"));
    // ...and flip only under a descending name sort.
    const byNameDesc = build({sort: {key: "name", dir: "desc"}, expanded: new Set(["space:1"])});
    assert.ok(byNameDesc.rows.findIndex((r) => r.key === "dir:11") < byNameDesc.rows.findIndex((r) => r.key === "dir:10"));
});

test("uses sort reads the provided counts", () => {
    const usesByKey = new Map([["config:2", 5], ["secret:1", 1]]);
    const {rows} = build({sort: {key: "uses", dir: "desc"}, expanded: new Set(["space:1", "dir:10"]), usesByKey});
    assert.deepEqual(rows.filter((r) => r.depth === 2).map((r) => r.key), ["config:2", "secret:1"]);
});

test("makeItems takes latest version facts and drops secrets while locked", () => {
    const secretMetas = [{id: 7, name: "token", spaceId: 1, valueDirectoryId: 10, versions: [
        {id: 72, version: 2, createdAt: new Date(2000)},
        {id: 71, version: 1, createdAt: new Date(1000)},
    ]}];
    const configMetas = [{id: 8, name: "level", spaceId: 1, valueDirectoryId: 0, valueVersions: [
        {id: 81, version: 3, value: "debug", createdAt: new Date(3000)},
    ]}];
    const unlocked = makeItems(secretMetas, configMetas, true);
    assert.equal(unlocked.length, 2);
    assert.equal(unlocked[0].version, 2);
    assert.equal(unlocked[0].directoryId, 10);
    assert.equal(unlocked[1].value, "debug");
    const locked = makeItems(secretMetas, configMetas, false);
    assert.deepEqual(locked.map((i) => i.kind), ["config"]);
});

test("a space is empty only when it holds neither a folder nor an item", () => {
    const spaces = [...SPACES, {id: 3, name: "empty"}, {id: 4, name: "folders-only"}];
    const dirs = [...DIRS, dir(13, 4, "unused")];
    assert.deepEqual([...emptySpaceIds(spaces, dirs, ITEMS)], [3]);
    // An item in a space nothing else references still counts as content.
    assert.deepEqual([...emptySpaceIds(spaces, dirs, [...ITEMS, item("secret", 9, 3, "lone")])], []);
});

test("nothing anywhere hides nothing: an unloaded tree must not filter itself away", () => {
    assert.deepEqual([...emptySpaceIds(SPACES, [], [])], []);
    assert.deepEqual([...emptySpaceIds([], [], [])], []);
    // Content only in the opendeploy space, which the pages never list: every
    // listed space is still empty, so the rule stands down rather than blanking
    // the tree.
    assert.deepEqual([...emptySpaceIds(SPACES, [], [item("secret", 1, 0, "internal")])], []);
});

test("dir paths survive cycles and dangling parents", () => {
    const byId = dirsById([dir(1, 1, "a", 2), dir(2, 1, "b", 1)]);
    assert.deepEqual(dirPathSegments(byId, 2), ["a", "b"]);
    assert.deepEqual(itemPathSegments(dirsById(DIRS), item("secret", 1, 1, "password", 10)), ["postgres", "password"]);
    assert.deepEqual(dirPathSegments(dirsById(DIRS), 999), []);
});

test("a self-parented directory cannot hang the walk", () => {
    const {rows} = build({dirs: [dir(20, 1, "loop", 20)], expanded: new Set(["space:1"])});
    assert.ok(rows.length > 0);
});

test("folderOptions lists the root first and excludes a moved folder's subtree", () => {
    const dirs = [dir(1, 1, "a"), dir(2, 1, "b", 1), dir(3, 1, "c", 2), dir(4, 1, "d"), dir(5, 2, "other")];
    assert.deepEqual(folderOptions(dirs, 1).map((o) => o.label), ["/", "a", "a/b", "a/b/c", "d"]);
    assert.deepEqual(folderOptions(dirs, 1, 2).map((o) => o.label), ["/", "a", "d"]);
    assert.deepEqual([...descendantDirIds(dirs, 1)].sort(), [1, 2, 3]);
});

test("the last visible non-fixed column absorbs the slack", () => {
    assert.equal(flexColumnKey(new Set(["name", "version", "created", "uses", "actions"])), "uses");
    assert.equal(flexColumnKey(new Set(["name", "version", "created", "uses", "value", "actions"])), "value");
    assert.equal(flexColumnKey(new Set(["name", "actions"])), "name");
});


const DND_DIRS = [dir(10, 1, "postgres"), dir(11, 1, "stripe", 10), dir(12, 2, "postgres")];
const DND_ITEMS = [
    item("secret", 1, 1, "password", 10),
    item("secret", 2, 1, "password", 0),
    item("secret", 3, 2, "password", 12),
];
const drop = (drag, destination) => checkDrop({dirs: DND_DIRS, items: DND_ITEMS, drag, destination});
const dragDir = (id) => dragSource({type: "dir", key: `dir:${id}`, dir: DND_DIRS.find((d) => d.id === id)});
const dragItem = (id) => dragSource({type: "item", key: `secret:${id}`, item: DND_ITEMS.find((i) => i.id === id)});

test("dropDestination resolves a row to the folder a drop lands in", () => {
    assert.deepEqual(dropDestination({type: "space", space: {id: 2}}), {spaceId: 2, directoryId: 0});
    assert.deepEqual(dropDestination({type: "dir", dir: DND_DIRS[0]}), {spaceId: 1, directoryId: 10});
    // An item resolves to its parent, not to itself.
    assert.deepEqual(dropDestination({type: "item", item: DND_ITEMS[0]}), {spaceId: 1, directoryId: 10});
    assert.equal(dropDestination(null), null);
});

test("a folder cannot be dropped into itself or its own subtree", () => {
    assert.equal(drop(dragDir(10), {spaceId: 1, directoryId: 10}).ok, false);
    assert.equal(drop(dragDir(10), {spaceId: 1, directoryId: 11}).ok, false);
    assert.match(drop(dragDir(10), {spaceId: 1, directoryId: 11}).reason, /inside itself/);
    // The other direction is fine: a child can come out to the root.
    assert.equal(drop(dragDir(11), {spaceId: 1, directoryId: 0}).ok, true);
});

test("a drop onto the row's current parent is refused without an explanation", () => {
    const result = drop(dragItem(1), {spaceId: 1, directoryId: 10});
    assert.equal(result.ok, false);
    assert.equal(result.reason, "");
});

test("a name already taken at the destination blocks the drop", () => {
    // secret 1 is "password" in dir 10; the root already holds a "password".
    const result = drop(dragItem(1), {spaceId: 1, directoryId: 0});
    assert.equal(result.ok, false);
    assert.match(result.reason, /already exists/);
    // Folders share the sibling namespace with items, so a folder named after
    // an existing sibling is blocked the same way.
    assert.equal(drop(dragDir(11), {spaceId: 1, directoryId: 0}).ok, true);
});

test("cross-space drops are allowed through for the server to answer", () => {
    // Space 2 already holds a "password" under dir 12, but the name check is
    // skipped across the boundary so the space error is the one reported.
    const result = drop(dragItem(1), {spaceId: 2, directoryId: 12});
    assert.equal(result.ok, true);
    assert.equal(result.crossSpace, true);
    assert.equal(result.reason, "");
    // A folder still cannot swallow itself, whichever space is targeted.
    assert.equal(drop(dragDir(10), {spaceId: 1, directoryId: 11}).crossSpace, false);
});

test("a drag with no source or no destination never drops", () => {
    assert.equal(drop(null, {spaceId: 1, directoryId: 0}).ok, false);
    assert.equal(drop(dragItem(1), null).ok, false);
    assert.equal(dragSource({type: "space", space: {id: 1}}), null);
});
