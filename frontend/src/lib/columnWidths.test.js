import assert from "node:assert/strict";
import test from "node:test";
import {clampWidth, columnWidthVars, readColumnWidths, withoutColumn} from "./columnWidths.js";

const columns = [
    {key: "time"},
    {key: "by", min: 40, max: 200},
    {key: "change", flex: true},
];

test("clampWidth rounds and keeps a width inside its bounds", () => {
    assert.equal(clampWidth(85.4), 85);
    assert.equal(clampWidth(3), 28);
    assert.equal(clampWidth(5000), 800);
    assert.equal(clampWidth(10, 40, 200), 40);
    assert.equal(clampWidth(260, 40, 200), 200);
});

test("readColumnWidths keeps the saved widths of known fixed columns, clamped", () => {
    assert.deepEqual(readColumnWidths('{"time":120,"by":900}', columns), {time: 120, by: 200});
    assert.deepEqual(readColumnWidths('{"by":12.6}', columns), {by: 40});
});

test("readColumnWidths drops the flex column, unknown keys and values that are not widths", () => {
    assert.deepEqual(readColumnWidths('{"change":300,"gone":90,"time":"120","by":-4}', columns), {});
    assert.deepEqual(readColumnWidths('{"time":null,"by":0}', columns), {});
});

test("readColumnWidths answers nothing for a missing or damaged value", () => {
    for (const raw of [null, undefined, "", "not json", "[90,80]", "12", "null"]) {
        assert.deepEqual(readColumnWidths(raw, columns), {}, String(raw));
    }
});

test("withoutColumn returns the widths without one key and leaves the input alone", () => {
    const widths = {time: 120, by: 90};
    assert.deepEqual(withoutColumn(widths, "by"), {time: 120});
    assert.deepEqual(withoutColumn(widths, "change"), {time: 120, by: 90});
    assert.deepEqual(widths, {time: 120, by: 90});
});

test("columnWidthVars writes one custom property per column and nothing for none", () => {
    assert.equal(columnWidthVars({time: 120, by: 90}, "hist"), "--hist-time:120px;--hist-by:90px");
    assert.equal(columnWidthVars({}, "hist"), "");
});
