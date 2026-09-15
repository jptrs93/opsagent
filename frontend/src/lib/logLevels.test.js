import {test} from "node:test";
import assert from "node:assert/strict";
import {LOG_LEVELS, logLevelBucket, logLevelFilters} from "./logLevels.js";

const selection = (...on) => Object.fromEntries(LOG_LEVELS.map(l => [l, on.includes(l)]));
const neq = (value) => ({field: 'level', op: 'neq', value});

test("logLevelBucket folds unknown levels into OTHER and blank into NONE", () => {
    assert.equal(logLevelBucket('ERROR'), 'ERROR');
    assert.equal(logLevelBucket('TRACE'), 'OTHER');
    assert.equal(logLevelBucket('FATAL'), 'OTHER');
    assert.equal(logLevelBucket(''), '');
    assert.equal(logLevelBucket(undefined), '');
});

test("logLevelFilters: everything on needs no filter", () => {
    assert.deepEqual(logLevelFilters(selection(...LOG_LEVELS)), []);
});

test("logLevelFilters: named levels and NONE become one in filter", () => {
    assert.deepEqual(logLevelFilters(selection('ERROR', 'WARN')), [{field: 'level', op: 'in', values: ['ERROR', 'WARN']}]);
    assert.deepEqual(logLevelFilters(selection('ERROR', '')), [{field: 'level', op: 'in', values: ['ERROR', '']}]);
    assert.deepEqual(logLevelFilters(selection()), [{field: 'level', op: 'in', values: []}]);
});

test("logLevelFilters: OTHER is written as exclusions", () => {
    assert.deepEqual(logLevelFilters(selection('OTHER')), [neq('ERROR'), neq('WARN'), neq('INFO'), neq('DEBUG'), {field: 'level', op: 'exists'}]);
    assert.deepEqual(logLevelFilters(selection('OTHER', '')), [neq('ERROR'), neq('WARN'), neq('INFO'), neq('DEBUG')]);
    assert.deepEqual(logLevelFilters(selection('ERROR', 'WARN', 'INFO', 'DEBUG', 'OTHER')), [{field: 'level', op: 'exists'}]);
    assert.deepEqual(logLevelFilters(selection('ERROR', 'OTHER')), [neq('WARN'), neq('INFO'), neq('DEBUG'), {field: 'level', op: 'exists'}]);
});
