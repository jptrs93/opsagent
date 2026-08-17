import assert from "node:assert/strict";
import {test} from "node:test";
import {envPrefix, groupEnvRows, isBooleanRow, isTruthyEnvValue} from "./envVarGrouping.js";

test("envPrefix takes the key up to the first underscore", () => {
    assert.equal(envPrefix("DATABASE_URL"), "DATABASE");
    assert.equal(envPrefix("DEBUG"), "DEBUG");
    assert.equal(envPrefix("_HIDDEN"), "_HIDDEN");
    assert.equal(envPrefix("  CACHE_HOST "), "CACHE");
    assert.equal(envPrefix(""), "");
    assert.equal(envPrefix(undefined), "");
});

test("groupEnvRows forms groups of two or more, singletons share No group last", () => {
    const rows = [
        {id: 1, key: "OTEL_EXPORTER"},
        {id: 2, key: "DATABASE_URL"},
        {id: 3, key: "PORT"},
        {id: 4, key: "DATABASE_POOL_SIZE"},
        {id: 5, key: "OTEL_SERVICE_NAME"},
        {id: 6, key: "METRICS_ENABLED"},
        {id: 7, key: ""},
    ];
    const groups = groupEnvRows(rows);
    assert.deepEqual(groups.map(g => g.prefix), ["DATABASE", "OTEL", ""]);
    assert.deepEqual(groups[0].rows.map(r => r.id), [2, 4]);
    assert.deepEqual(groups[1].rows.map(r => r.id), [1, 5]);
    assert.deepEqual(groups[2].rows.map(r => r.id), [3, 6, 7]);
});

test("an underscore-free key groups with its underscored siblings", () => {
    const groups = groupEnvRows([{id: 1, key: "DEBUG"}, {id: 2, key: "DEBUG_MODE"}]);
    assert.deepEqual(groups.map(g => g.prefix), ["DEBUG"]);
});

test("all-singleton input yields only the No group bucket in original order", () => {
    const groups = groupEnvRows([{id: 1, key: "PORT"}, {id: 2, key: "DEBUG"}]);
    assert.deepEqual(groups.map(g => g.prefix), [""]);
    assert.deepEqual(groups[0].rows.map(r => r.id), [1, 2]);
});

test("empty input yields no groups", () => {
    assert.deepEqual(groupEnvRows([]), []);
    assert.deepEqual(groupEnvRows(undefined), []);
});

test("isBooleanRow requires an ENABLED suffix and a plain value type", () => {
    assert.equal(isBooleanRow({key: "METRICS_ENABLED", type: "value"}), true);
    assert.equal(isBooleanRow({key: "tls_enabled"}), true);
    assert.equal(isBooleanRow({key: "ENABLED"}), true);
    assert.equal(isBooleanRow({key: "FEATURE_DISABLED"}), false);
    assert.equal(isBooleanRow({key: "ENABLED_FEATURES"}), false);
    assert.equal(isBooleanRow({key: "CACHE_ENABLED", type: "secret"}), false);
});

test("isTruthyEnvValue reads leniently", () => {
    for (const on of ["true", "TRUE", "1", "yes", "on"]) assert.equal(isTruthyEnvValue(on), true, on);
    for (const off of ["false", "0", "", "no", "off", undefined, null, "random"]) {
        assert.equal(isTruthyEnvValue(off), false, String(off));
    }
});
