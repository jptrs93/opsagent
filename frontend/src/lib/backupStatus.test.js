import assert from "node:assert/strict";
import {test} from "node:test";
import {databaseSize, databaseSizeHint, databaseSizeText} from "./backupStatus.js";

test("the database size is the file plus its write-ahead log", () => {
    assert.deepEqual(databaseSize({dbSizeBytes: 5_877_760, walSizeBytes: 1_870_512}), {db: 5_877_760, wal: 1_870_512, total: 7_748_272});
    assert.equal(databaseSizeText({dbSizeBytes: 5_877_760, walSizeBytes: 1_870_512}), "7.75 MB (1.87 MB WAL)");
    assert.equal(databaseSizeHint({dbSizeBytes: 5_877_760, walSizeBytes: 1_870_512}), "primary.db 5.88 MB + write-ahead log 1.87 MB");
});

test("a database with no write-ahead log still shows the bracket", () => {
    assert.equal(databaseSizeText({dbSizeBytes: 4096}), "4.1 KB (0 B WAL)");
});

test("a status with no size reads as unknown", () => {
    assert.equal(databaseSizeText(undefined), "unknown");
    assert.equal(databaseSizeText({configured: true}), "unknown");
    assert.equal(databaseSizeHint(null), "");
});

test("a large database is shown in gigabytes", () => {
    assert.equal(databaseSizeText({dbSizeBytes: 3_200_000_000, walSizeBytes: 4_000_000}), "3.20 GB (4.00 MB WAL)");
});
