import test from "node:test";
import assert from "node:assert/strict";
import {
    cleanPresetName, createPresetStore, findPresetByName, matchItems, matchingPreset, parsePresets,
    presetSelection, removePreset, serializePresets, upsertPreset,
} from "./selectionPresets.js";

const memoryStorage = (initial = {}) => {
    const data = new Map(Object.entries(initial));
    return {
        getItem: (key) => data.has(key) ? data.get(key) : null,
        setItem: (key, value) => { data.set(key, String(value)); },
        data,
    };
};

const counter = () => {
    let n = 0;
    return () => `p_${++n}`;
};

test("a preset name is trimmed, collapsed and capped", () => {
    assert.equal(cleanPresetName("  prod   and\tstaging "), "prod and staging");
    assert.equal(cleanPresetName("x".repeat(60)).length, 40);
    assert.equal(cleanPresetName(null), "");
});

test("saving a new name appends and saving an existing name replaces its members", () => {
    const first = upsertPreset([], "Prod", [1, 2], "p_1");
    assert.deepEqual(first.list, [{id: "p_1", name: "Prod", ids: [1, 2]}]);

    const second = upsertPreset(first.list, "Staging", [3], "p_2");
    const replaced = upsertPreset(second.list, " prod ", [2, 4, 4], "p_3");

    assert.deepEqual(replaced.list, [
        {id: "p_1", name: "prod", ids: [2, 4]},
        {id: "p_2", name: "Staging", ids: [3]},
    ]);
    assert.equal(replaced.preset.id, "p_1");
});

test("a preset needs a name and at least one member", () => {
    assert.equal(upsertPreset([], "   ", [1], "p_1"), null);
    assert.equal(upsertPreset([], "Empty", [], "p_1"), null);
});

test("removing a preset leaves the others in order", () => {
    const list = [{id: "a", name: "A", ids: [1]}, {id: "b", name: "B", ids: [2]}, {id: "c", name: "C", ids: [3]}];
    assert.deepEqual(removePreset(list, "b").map((p) => p.id), ["a", "c"]);
});

test("a preset applies only the members that still exist", () => {
    const preset = {id: "a", name: "A", ids: [1, 2, 9]};
    assert.deepEqual([...presetSelection(preset, new Set([1, 2, 3]))], [1, 2]);
});

test("the matching preset is the one whose surviving members equal the selection", () => {
    const list = [
        {id: "a", name: "Prod", ids: [1, 9]},
        {id: "b", name: "Both", ids: [1, 2]},
    ];
    const known = new Set([1, 2, 3]);

    assert.equal(matchingPreset(list, new Set([1]), known).id, "a");
    assert.equal(matchingPreset(list, new Set([2, 1]), known).id, "b");
    assert.equal(matchingPreset(list, new Set([1, 2, 3]), known), null);
});

test("an empty selection matches no preset, even one whose members are all gone", () => {
    const list = [{id: "a", name: "Gone", ids: [9]}];
    assert.equal(matchingPreset(list, new Set(), new Set([1, 2])), null);
});

test("a search matches every word against the label and the keywords", () => {
    const items = [
        {id: 1, label: "prod-eu"},
        {id: 2, label: "prod-us", keywords: "america"},
        {id: 3, label: "staging"},
    ];

    assert.deepEqual(matchItems(items, "PROD").map((i) => i.id), [1, 2]);
    assert.deepEqual(matchItems(items, "prod america").map((i) => i.id), [2]);
    assert.deepEqual(matchItems(items, "  ").map((i) => i.id), [1, 2, 3]);
    assert.deepEqual(matchItems(items, "nothing"), []);
});

test("stored presets survive a round trip and bad entries are dropped", () => {
    const list = [{id: "a", name: "Prod", ids: [1, "x"]}];
    assert.deepEqual(parsePresets(serializePresets(list)), list);

    const messy = JSON.stringify({version: 1, presets: [
        {id: "a", name: "Prod", ids: [1, 1, null, {}]},
        {id: "b", name: "prod", ids: [2]},
        {id: "a", name: "Other", ids: [3]},
        {id: "c", name: "", ids: [4]},
        {name: "No id", ids: [5]},
        {id: "d", name: "No members"},
    ]});
    assert.deepEqual(parsePresets(messy), [{id: "a", name: "Prod", ids: [1]}]);
    assert.deepEqual(parsePresets("not json"), []);
    assert.deepEqual(parsePresets("{}"), []);
});

test("a name is found whatever its case", () => {
    const list = [{id: "a", name: "Prod", ids: [1]}];
    assert.equal(findPresetByName(list, "PROD ").id, "a");
    assert.equal(findPresetByName(list, "staging"), null);
    assert.equal(findPresetByName(list, ""), null);
});

test("a store writes through to its storage and reads it back", () => {
    const storage = memoryStorage();
    const store = createPresetStore({key: "k", storage, makeId: counter()});

    store.save("Prod", [1, 2]);
    store.save("Staging", [3]);
    store.remove("p_1");

    assert.deepEqual(store.list(), [{id: "p_2", name: "Staging", ids: [3]}]);
    const reopened = createPresetStore({key: "k", storage});
    assert.deepEqual(reopened.list(), [{id: "p_2", name: "Staging", ids: [3]}]);
});

test("a store picks up what another tab wrote on reload", () => {
    const storage = memoryStorage();
    const store = createPresetStore({key: "k", storage, makeId: counter()});
    storage.setItem("k", serializePresets([{id: "z", name: "Elsewhere", ids: [7]}]));

    assert.deepEqual(store.list(), []);
    assert.deepEqual(store.reload(), [{id: "z", name: "Elsewhere", ids: [7]}]);
});

test("a store keeps working when the storage throws or is missing", () => {
    const broken = {
        getItem: () => { throw new Error("denied"); },
        setItem: () => { throw new Error("quota"); },
    };
    for (const storage of [broken, undefined]) {
        const store = createPresetStore({key: "k", storage, makeId: counter()});
        assert.deepEqual(store.list(), []);
        assert.equal(store.save("Prod", [1]).name, "Prod");
        assert.deepEqual(store.list(), [{id: "p_1", name: "Prod", ids: [1]}]);
    }
});

test("a store refuses an unnamed or empty preset and leaves the list alone", () => {
    const storage = memoryStorage();
    const store = createPresetStore({key: "k", storage, makeId: counter()});

    assert.equal(store.save("", [1]), null);
    assert.equal(store.save("Prod", []), null);
    assert.deepEqual(store.list(), []);
    assert.equal(storage.data.has("k"), false);
});
