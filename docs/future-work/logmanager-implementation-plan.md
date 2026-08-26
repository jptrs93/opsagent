# logmanager implementation plan

## Purpose and scope

`logmanager` is the node-local log storage backend: it compacts sealed WAL
files into parquet, owns the sqlite catalog that describes them, answers log
queries by routing between parquet and the live WAL tail, and applies
retention. One instance per node, in-process in the primary and secondary
agents.

This is the concrete implementation plan for the design in
[log-compaction.md](log-compaction.md). Where the two disagree, this document
wins; the divergences are called out in "Revisions to the design doc" at the
end.

### Relationship to the ingestion-boundary direction

The end-state direction is that the WAL is a standard ingestion boundary and
downstream backends (ours or third-party) consume it through a tailing layer
that tracks a consumed index per file and calls `consume(line)`. We are not
building that interface now. The native backend special-cases it: the WAL is
already our live store, so `logmanager` reads WAL files directly for both
compaction input and the tail query path, with no intermediate abstraction.

What we do preserve is the *shape* that makes the general form reachable: the
`wal_progress` table below is exactly the consumed-index state a generic
tailing ingestor needs, and the record decode/shred function is already a
standalone unit. Promoting the native path to a shared interface later is
mechanical rather than a rewrite.

## Package layout

```
backend/lib/log/logmanager/
  manager.go     Manager type, Run/RunOnce pass loop, config, path helpers
  compact.go     sealed WAL bucket -> parquet, commit ordering
  source.go      forward WAL scan over a byte range, torn-record resync
  parquetio.go   row type, writer (tmp/rename/fsync), descending reader
  catalog.go     sqlite open/schema/queries/commit transactions
  catalog.sql    embedded schema
  reconcile.go   startup and periodic filesystem/catalog reconciliation
  query.go       query planning: WAL tail segments + parquet set
  retention.go   day-dir expiry and WAL sweep
  shred.go       (milestone 3) raw line -> StructuredLogLine
```

The catalog opens sqlite directly rather than through `storage/sqlitedb`,
whose `MustOpen`/`ApplySchema` panic on failure. A corrupt or unwritable log
catalog must degrade to "compaction disabled", never take down the agent.

`lib/log/logreader` stays as the low-level WAL record reader (backward reader,
frame validation) and is reused by `logmanager`; the public `StreamLogs` entry
point moves to `logmanager.Query` once the query path lands, and the two
existing call sites (`app/primary/webuihandler/deployments.go:551`,
`app/secondary/logs.go:149`) switch over.

## Runtime placement

- Static dir `cfg.LogArchiveDir = dataDir + "-log-archive"` —
  `/var/lib/opendeploy-log-archive` in production — created `0o750` alongside
  the other roots in `ainit.ensureStaticDirs`. Parquet lives outside the WAL
  tree so the WAL dir listing stays cheap and single-purpose, and so
  retention can operate on whole day dirs.
- **Nothing here is configurable.** Paths come from `ainit.StaticConfig`, and
  the grace, pass interval, retention window, and reconcile interval are
  package-level values in `manager.go` (declared `var` only so tests can
  override them, matching `retentionInterval` in `app/secondary/retention.go`).
  `New` takes the node id and nothing else.
- Layout: `<LogArchiveDir>/<deployment_id>/<YYYYMMDD>/<name>.parquet`, catalog
  at `<LogArchiveDir>/catalog.db`. Deployment `0` is the system log and needs
  no special case.
- Started from both `app/primary/run.go` and `app/secondary/secondary.go`,
  next to the existing `runRuntimeInputRetention` goroutine — both roles run
  workloads on their own node and therefore own WAL files. The manager takes
  the node id (`primaryNode.ID` / `cfg.NodeID`).
- The catalog opens through `storage/sqlitedb.MustOpen` (WAL journal,
  5s busy timeout) and applies `catalog.sql` via `ApplySchema`. It is *not*
  litestream-replicated: it is node-local derived state.
- Single writer. Nothing else touches the catalog or the archive; the raw log
  consumer subprocesses only ever append to WAL files.

## Invariants

1. **The sqlite transaction is the commit point.** A parquet file is part of
   the store when its `log_files` row is committed, not when it is renamed
   into place.
2. **A file on disk with no live catalog row is garbage** and is deleted by
   reconciliation. Never adopted. (Exception: the explicit catalog-rebuild
   path, below.)
3. **Inputs are destroyed only after the output is committed.** WAL files are
   unlinked after the transaction that records their consumption; roll-up
   input parquets are unlinked after the transaction that inserts their
   replacement and deletes their rows.
4. **Consumption is a byte range, never a whole file.** Every WAL is consumed
   as `[0, end)` where `end` is the offset after the last fully-validated
   record. This one rule serves sealed-bucket compaction, incremental
   compaction of an active bucket, and straggler re-appends uniformly.
5. **Query coverage is union, never subtraction of guesses.** The tail is
   "every WAL byte above its recorded `consumed_bytes`"; history is "every
   live catalog row overlapping the range". These are disjoint by
   construction, so no dedup pass is needed.

## Compaction pass

The loop runs every `passInterval` (default 60s), and per pass iterates
deployment dirs under `LogWALDir` sequentially. Sequential is deliberate:
compaction is I/O-bound background work with no latency requirement, and a
single worker makes the "one writer" invariant trivially true.

### Eligibility

For each deployment:

1. `os.ReadDir` the WAL dir; parse `<YYYYMMDD_HHMM>.wal` names (reuse
   `logreader.parseWalFileName`, promoted to exported).
2. A bucket is **sealed** when `bucket + 30min + grace <= now`, grace = 2
   minutes. Sealing is inferred from the wall clock and never observed —
   see the design doc for why close-detection is unusable.
3. Load `wal_progress` rows for the deployment. For each sealed WAL compute
   the start offset:
   - `size >= progress.consumed_bytes` → `start = consumed_bytes` (the file
     is the same file, possibly extended by a straggler after a crash-window
     unlink failure).
   - `size < consumed_bytes` → `start = 0` (the file was unlinked and
     recreated by a straggler; it holds only new records).
   - no progress row → `start = 0`.
4. Skip files where `start >= size`. Process the remainder strictly
   oldest-first.

Active (unsealed) buckets are ignored in phase 1. Phase 3 extends the same
machinery to consume them incrementally; see "Live tail shortening".

### One output file per bucket

Each eligible WAL bucket produces exactly one parquet file. This fell out of
two facts and removed a lot of machinery that the earlier draft of this plan
carried:

- A bucket can never cross a UTC day. Buckets are 30-minute aligned and a
  record's bucket is chosen from its own timestamp, so every record in
  `20260615_2330` has a timestamp in `[23:30, 00:00)`. The day-cut logic is
  unnecessary.
- Records stream straight into the parquet writer, which flushes row groups
  as it goes, so memory is bounded by the row group rather than by the
  bucket. The batch-size caps are unnecessary.

The output filename embeds the exact min/max record timestamps, which aren't
known until the scan finishes — but the file is written under a temporary
name and only named at rename time, so this costs nothing. A bucket that
yields no valid records produces no file at all: its WAL is simply unlinked.

Multi-bucket batching remains available later if per-file overhead ever
matters, but the day roll-up addresses file count more directly.

Shipped since: every record carries a per-(run, stream) monotonic `seq`
stamped by the consumer's appender, making
`(time, node, instance_ordinal, run, stream, seq)` a globally unique sort key.
The current WAL frame uses magic `0xfd` with a leading payload format-version
byte (so future layout changes are non-breaking); legacy `0xfe` frames (no
seq) remain readable and report seq 0. Parquet files gained a `seq` column,
rows are strictly key-sorted (the commit writer detects sliding-window
overflow disorder and resorts via a SortingWriter before rename), and sorted
files carry a `sorted=1` key-value metadata entry that gates early-break and
row-group pruning on the query path.

Forward scanning (`source.go`) validates every frame: magic, length bounds,
CRC, trailer. On a bad frame it scans forward for the next magic and
revalidates — the mirror of the existing backward reader's resync. `end` is
the offset after the last record that fully validated, so trailing torn bytes
are never counted as consumed... except that they are: a torn record mid-file
is skipped permanently and `end` advances past it. That is correct — the torn
bytes are unrecoverable by construction, and leaving them unconsumed would
stall the file forever.

### Output file naming

```
L0_<minUnixMs>-<maxUnixMs>_n<node>_<seq>.parquet   batch output
L1_<minUnixMs>-<maxUnixMs>_n<node>_<seq>.parquet   node day roll-up
```

`seq` is `max(existing seq in the day dir) + 1`, read from the directory
listing; a single serialized writer per node makes this race-free without a
durable counter. min/max are the exact record timestamps in the file. A batch
never spans a UTC day: if the eligible ranges cross midnight, the batch is cut
at the boundary and the second part becomes the next output file.

L2 (cross-node merge) stays deferred; see the design doc.

### Write and rename

1. Create `<daydir>/.tmp-<finalname>` (dot-prefixed; listings and globs skip
   dotfiles, and same-dir rename is atomic).
2. Write row groups sorted time-ascending, ~128k rows per group so per-group
   time stats give intra-file pruning. Footer KV metadata records
   `sources`, `min_time`, `max_time`, `rows`, `node`, `level`, and the key
   inventory.
3. `f.Sync()`, `f.Close()`.
4. `os.Rename(tmp, final)`.
5. `fsync` the day directory.

The footer metadata is not load-bearing for normal operation (invariant 2 says
rowless files die), but it makes the archive self-describing for external
tools and is what the catalog-rebuild path reads.

Sorting is established at L0 commit time, not deferred to roll-up
(implemented: `sortedByTime` in logmanager). The WAL appender routes each
record to the bucket its own timestamp belongs to, so a stable per-bucket sort
of the commit stream yields a fully time-sorted file with memory bounded by
one bucket. The sort is skipped when records arrive already ordered (tracked
by a per-append comparison — the single-writer happy case), and a bucket
exceeding `sortBufBytesThresh` (8MB) degrades to a sliding-window
approximate sort (sort, yield the bottom half, keep buffering). Day roll-up
therefore merges already-sorted inputs and only reorders across the seams of
mid-bucket commit splits.

## sqlite schema

```sql
CREATE TABLE IF NOT EXISTS log_files (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  deployment_id  INTEGER NOT NULL,
  day            TEXT    NOT NULL,  -- 'YYYYMMDD' UTC
  name           TEXT    NOT NULL,  -- basename within <archive>/<dep>/<day>/
  level          INTEGER NOT NULL,  -- 0 = batch, 1 = node day roll-up
  node           INTEGER NOT NULL,
  seq            INTEGER NOT NULL,
  min_time       INTEGER NOT NULL,  -- unix nanos, exact
  max_time       INTEGER NOT NULL,
  row_count      INTEGER NOT NULL,
  byte_size      INTEGER NOT NULL,
  schema_hash    TEXT    NOT NULL,
  created_at     INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS log_files_path
  ON log_files(deployment_id, day, name);
CREATE INDEX IF NOT EXISTS log_files_scan
  ON log_files(deployment_id, max_time DESC, min_time);

CREATE TABLE IF NOT EXISTS wal_progress (
  deployment_id  INTEGER NOT NULL,
  bucket         TEXT    NOT NULL,  -- WAL basename minus '.wal'
  consumed_bytes INTEGER NOT NULL,
  file_id        INTEGER,           -- output that consumed the last range
  updated_at     INTEGER NOT NULL,
  PRIMARY KEY (deployment_id, bucket)
) WITHOUT ROWID;
```

Notes on what is deliberately absent:

- **No watermark table.** "Compacted through T" is derivable from
  `wal_progress` and would be a second source of truth that can disagree with
  it. The planner computes the tail from per-file offsets directly.
- **No superseded/tombstone column.** Roll-up deletes input rows in the same
  transaction that inserts the replacement; the resulting rowless files are
  cleaned by reconciliation. A tombstone state would only exist to support
  adopting orphans, which invariant 2 rules out.
- **No `log_file_keys` table yet.** It arrives with shredding (milestone 3),
  carrying `(file_id, key, type, disposition)` where disposition is dense
  column / spill map / elided. Until then the parquet schema is fixed and
  there is no per-file key inventory to record.

## Commit ordering

### WAL batch

| Step | Action |
| --- | --- |
| 1 | Plan ranges from directory listing + `wal_progress` |
| 2 | Scan, decode, shred into the in-memory batch |
| 3 | Write tmp parquet, fsync, rename, fsync day dir |
| 4 | **Transaction**: `INSERT log_files` → id; `INSERT log_file_keys`; `INSERT ... ON CONFLICT DO UPDATE` each `wal_progress` row with the new `consumed_bytes` and `file_id`; `COMMIT` |
| 5 | Unlink every fully-consumed sealed WAL |
| 6 | `DELETE FROM wal_progress` for the unlinked buckets |

Crash windows:

- **Before 4** — parquet exists, no row. Reconciliation deletes it;
  `wal_progress` is unchanged, so the WALs are recompacted. No loss, no
  duplication.
- **Between 4 and 5** — parquet committed, WAL still present. `consumed_bytes
  == size`, so the planner contributes no tail bytes for it and the next pass
  computes an empty range. The next pass's unlink step finishes the job.
- **Between 5 and 6** — WAL gone, stale progress row. The planner sees no
  file; the next pass's listing finds no bucket and deletes the row. Harmless.

Step 6 must follow step 5, not precede it: a progress row deleted while the
file still exists would make the whole WAL look unconsumed and duplicate its
records into a second parquet.

### Day roll-up

Trigger: day `D` is eligible once `D_end + rollupGrace` has passed
(`rollupGrace` = 15 minutes, comfortably past the last bucket's own grace).
Inputs: every live L0 row for `(deployment, day, node)`. Output: one L1 per
`maxFileBytes` (~256MB), still time-sorted, still day-bounded.

| Step | Action |
| --- | --- |
| 1 | Read all input parquets (or a size-capped subset), merge time-sorted |
| 2 | Write tmp, fsync, rename, fsync day dir |
| 3 | **Transaction**: `INSERT` the L1 row(s) + keys; `DELETE FROM log_files WHERE id IN (inputs)`; cascade-delete their `log_file_keys`; `COMMIT` |
| 4 | Unlink the input files |

Crash before 3 → rowless L1, deleted, redone. Crash between 3 and 4 → rowless
L0 inputs, deleted by reconciliation, and the L1 already covers their range.

This is the answer to "merging with other files for the same day". Merging
incrementally on every pass — rewriting the day's tail file each time a bucket
seals — was rejected: it rewrites the same bytes up to 48 times a day for a
2× file-count improvement. Deferring to a once-per-day roll-up gives exactly
2× write amplification and settles each finished day to one or two files,
while the current day's ~48 files cost nothing but catalog rows.

## Query planning

`logmanager.Query(deploymentID, configVersion, since, till)` returns lines
newest-first, matching the current `logreader.StreamLogs` contract.

1. **History**: `SELECT ... FROM log_files WHERE deployment_id = ? AND
   max_time >= since AND (till IS NULL OR min_time <= till) ORDER BY max_time
   DESC`.
2. **Tail**: list the WAL dir, join against `wal_progress`, and for each file
   take the segment `[consumed_bytes, size)` (whole file when there is no
   row). Prune by bucket time against `since`/`till` as the current querier
   already does.
3. **Order**: drain the tail newest-first, then the parquet files newest-first.
   No k-way merge. The two sets are disjoint in bytes and near-disjoint in
   time; parquet files are internally time-sorted at commit, while the WAL
   tail stays near-sorted (concurrent writers stamp then write), so ordering
   is exact within committed files and approximate only in the tail and at
   file boundaries.
4. Filtering on version/run/stream and the time range happens per row in both
   readers; parquet additionally prunes row groups by time stats.

The tail is bounded at roughly one bucket + grace (~32 min) per deployment in
phase 1, and by `liveTailBytes` once phase 3 lands.

**The parquet files keep the raw line bytes as a column.** Compaction must not
change what the existing UI renders, and reconstructing a line from shredded
fields would not be byte-identical. Dropping the raw column for lines that
parse cleanly is a later size optimisation, gated on the UI rendering from
fields.

## Record identity and pagination (deferred)

The structured search API below is one-shot (newest 10k matches, no cursor),
so nothing currently needs a per-record identity. If pagination, tail-follow,
or cross-node merge ever need a resume point, the problem is that timestamps
alone are not unique: batch appends stamp many records with one clock read,
so a `(ts, deployment)` watermark either skips or duplicates the boundary
run, and a run longer than a page cannot make progress at all. Options, in
decreasing preference:

- **Appender nudge.** The appender already compares each record's timestamp
  against the previous one (`sortedByTime`); bump equal stamps by 1ns so `ts`
  is strictly monotonic per stream and is itself the identity. Drift is
  bounded by the burst length in nanoseconds and self-heals on the next real
  clock read — far below NTP accuracy. Order becomes a property of the data:
  any correct sort reproduces it, compaction included.
- **Per-writer seq column.** Keep true stamps and add a small counter column
  assigned at append (per-bucket appends are already serialized), carried
  verbatim into parquet. Cursor = `(ts, writer, seq)`. Purist version of the
  same thing at the cost of a column.
- **Seen-count cursor.** No write-time change: cursor = boundary tuple
  `(ts, node, deployment, seen)` where `seen` counts equal-ts records already
  delivered, and the executor skips that many on resume. Correct only while
  every path that orders rows — L0 stable sort including the 8MB
  sliding-window degraded mode, day roll-up tie-breaking by file seq, rebuild
  and replay — reproduces the identical tie order forever, and it breaks
  silently if compaction ever drops a record inside a same-ts run. The
  invariant lives in the executor, not the data.

Context views ("show surroundings of this row") do not need row addressing
either: estimate local record frequency, run a widened time-range query
centred on the row's timestamp, and trim client-side. Exact ±N semantics, if
ever wanted, fall out of the same trick by over-fetching and discarding.

## Search API sketch

Shipped: this replaced the old streaming raw-line `postV1DeploymentsLogSearch`
endpoint outright (`logquery.go` in logmanager, `/v1/deployments/log-query` on
`ApiServer`, one-shot request/response frames over the cluster session instead
of the log-search stream). The endpoint returns structured records plus the
aggregates the redesigned logs page needs in one round trip. Bodies are
protobuf like the rest of the API; the JSON below is illustrative only. The
implementation also accepts an `order` field ("desc" default / "asc") so a
context view can fetch the oldest-N records after an anchor timestamp, and an
empty string inside an `in` filter's values matches records missing the field
(used for the unleveled series toggle). The separate lazy `logfieldstats`
endpoint sketched below was folded into the query response before release:
per-field top-10 values + coverage + an other bucket, computed over the newest
5k matched records during the same scan (`fields` replaces `fieldNames`;
`stats.sampledRows` reports the sample size).

`POST /v1/deployments/logquery` — request:

```json
{
  "deploymentId": 12,
  "targetNodeId": 3,
  "configVersion": 0,
  "timeStart": "2026-08-23T04:52:00Z",
  "timeEnd": "2026-08-23T08:13:00Z",
  "filters": [
    {"field": "level", "op": "in", "values": ["ERROR", "WARN"]},
    {"field": "service", "op": "eq", "value": "api"},
    {"field": "host", "op": "neq", "value": "node-2"},
    {"field": "err", "op": "exists"},
    {"op": "contains", "value": "pool exhausted"}
  ],
  "limit": 10000,
  "histogramBuckets": 90,
  "includeRaw": false
}
```

- A filter with no `field` matches against the message text. Ops:
  `eq`/`neq`/`in`/`exists`/`notExists`/`contains`/`notContains`, all
  case-insensitive string semantics in v1. Numeric comparisons
  (`gt`/`gte`/`lt`/`lte` on shredded numeric columns) are a later addition
  and the reason `value` is not restricted to strings. Filters AND together;
  the query-bar grammar is a frontend concern — the wire format is already
  parsed.
- `timeEnd` omitted means "now", pinned server-side and echoed back.
- `limit` is capped server-side (10k); `limit: 0` is legal and returns
  aggregates only.
- `histogramBuckets` is a hint from the client's pixel width; the server may
  round the interval and widen the range to bucket-align.

Response:

```json
{
  "stats": {
    "timeStart": "2026-08-23T04:52:00Z",
    "timeEnd": "2026-08-23T08:13:00Z",
    "scannedRows": 1294200,
    "matchedRows": 78750,
    "returnedRows": 10000,
    "truncated": true,
    "tookMs": 143
  },
  "histogram": {
    "bucketMs": 134000,
    "startTime": "2026-08-23T04:52:00Z",
    "series": [
      {"level": "ERROR", "counts": [3, 0, 812, 940, 7]},
      {"level": "WARN",  "counts": [11, 9, 403, 380, 12]},
      {"level": "INFO",  "counts": [0, 0, 0, 0, 0]},
      {"level": "DEBUG", "counts": [0, 0, 0, 0, 0]}
    ]
  },
  "fieldNames": ["level", "service", "host", "version", "logger", "method",
                 "path", "status", "duration_ms", "trace_id", "err"],
  "records": [
    {
      "ts": "2026-08-23T08:13:47.342123456Z",
      "level": "ERROR",
      "msg": "failed to acquire db connection: pool exhausted (32 in use)",
      "fields": {
        "service": "api", "host": "node-1", "version": "v0.0.507",
        "logger": "db", "status": "500", "err": "pool_exhausted",
        "trace_id": "9f2c11aab8d04e21"
      }
    }
  ],
  "warnings": []
}
```

- `records` are the newest `limit` matches, newest-first (display order).
  `matchedRows` is the full-range count; `truncated: true` drives the
  "showing 10,000 of 78,750 — refine or zoom" UI. The histogram always
  covers the whole requested range regardless of `limit`.
- `ts` is int64 unix nanos in proto. It must never become a JSON/JS number:
  2^53 ns is ~104 days, so nano timestamps only survive as int64 or string.
- `fields` is a flat string map in v1 (matches threshold-hybrid shredding);
  typed values ride with the numeric-comparison work. `includeRaw: true`
  adds the raw line bytes per record for byte-exact display.
- `fieldNames` (now `fields`, with sampled per-field stats attached) is built
  from the parsed field keys seen during the scan itself.
- `warnings` carries partial-result notes (e.g. a WAL segment that failed
  CRC and was skipped) so degraded answers are visible rather than silent.

`POST /v1/deployments/logfieldstats` — as noted above this was folded into
the query response rather than shipped as a lazy endpoint; the shape below
survives per-field inside `LogQueryResponse.fields`. Original sketch — same
scope and `filters` as above plus:

```json
{"field": "service", "topN": 5, "sampleLimit": 6000}
```

```json
{
  "sampled": 6000,
  "coverage": 1.0,
  "distinct": 6,
  "top": [
    {"value": "api", "count": 2711},
    {"value": "ingress", "count": 1121}
  ]
}
```

Sampling newest-first is acceptable and keeps the stats cheap; `coverage`
is the fraction of sampled records carrying the field. Live tail is out of
scope and would be a separate streaming mode.

## Reconciliation and rebuild

Runs at startup and then every `reconcileInterval` (~1h):

- **Rowless file → delete.** Includes leftover `.tmp-*` files, which are
  deleted unconditionally.
- **Row with no file → delete the row, log at warn.** This should be
  unreachable given invariant 3; if it fires, something outside the manager
  is deleting archive files.
- **Progress row whose bucket has no WAL file → delete the row.**

**Catalog rebuild is a separate, explicit path.** If `catalog.db` is missing
or empty at startup while the archive is non-empty, the routine "rowless file
→ delete" rule would destroy the entire archive. That case instead enters
rebuild mode: walk the archive, read each footer, reinsert rows and key
inventories, and seed `wal_progress` from the footers' `sources` lists. The
distinction is made once, at open time, and logged loudly.

## Retention

There is currently **no retention on run logs at all** — the WAL tree grows
without bound. Compaction's WAL unlink is the first reclamation this system
has ever had, and archive retention is new work rather than a port.

- Archive: delete whole day dirs older than `retentionDays` (default 30) and
  their rows, in that order (unlink then rows would leave the catalog
  pointing at nothing; rows then unlink leaves rowless files that
  reconciliation cleans — so delete rows first, then the dir).
- WAL safety sweep: unlink `.wal` files older than `retentionDays` that no
  longer belong to a live deployment dir, covering deployments deleted while
  their logs were uncompacted.
- Legacy `.logbin` files: deleted by the same age sweep. They are never
  compacted and are already invisible to the querier.

## Parquet schema and shredding

Library: `github.com/parquet-go/parquet-go` — pure Go (no cgo, consistent with
`modernc.org/sqlite`), supports dynamic schemas via `parquet.Group`, footer KV
metadata, per-row-group stats, and zstd through the already-vendored
`klauspost/compress`.

The shipped schema is fixed and carries no parsed fields at all:
`time`, `version`, `run`, `stream`, `raw_message`. Shredding is a TODO in
`parquetio.go`.

Decisions taken for when it lands:

- **JSON is the only format parsed.** It is what modern applications emit;
  logfmt is declining, has no reliable signature, and its unquoted values
  force lossy type guesses (`id=007` → 7, `version=1.10` → 1.1). Anything
  that doesn't parse as JSON is simply a `raw_message` line.
- **`raw_message` always holds the original line**, including for lines that
  parse cleanly. Shredded fields are a derived index over it, never a
  replacement. This makes shredding a pure indexing decision that is always
  reversible: a parser bug, a detection misfire, or a later improvement can
  be fixed by rewriting files, because the source bytes are still there.
  Given that the WAL is deleted ~32 minutes after a line is written, the
  parquet file is the only remaining copy, so this is the difference between
  a fixable mistake and a permanent one.
- **`raw_message` is `bytes`, not `string`**, both in the proto and in the
  parquet column. Container stdout is an arbitrary byte stream: workloads
  print binary blobs, latin-1 text, and truncated buffers, so invalid UTF-8
  arrives in ordinary lines and no amount of care at our end prevents it.
  A proto3 `string` requires valid UTF-8, and parquet's STRING logical
  annotation claims it, so both would be lying. Sanitising to U+FFFD was
  rejected — it contradicts the fidelity-copy purpose of the field.
  (Separately, the consumer no longer splits oversized lines mid-rune; that
  was a real defect worth fixing, but it was never the dominant source of
  invalid UTF-8 and does not change this decision.)
- Storage cost is real but modest: within a column chunk the repeated JSON
  keys and punctuation compress away, so the marginal cost is roughly the
  values a second time. The current state stores 100% raw bytes uncompressed
  in the WAL, so this is still a large net win.
- **Verify-then-drop is the escape hatch if the archive ever gets expensive**:
  reserialize from the parsed fields, compare byte-for-byte, drop
  `raw_message` only on an exact match. An exact round-trip is proof that the
  fields losslessly encode the line, so the reversibility argument above
  survives. It needs key order stored, which is cheap.

Shredding itself then follows the design doc's threshold hybrid: keys become
real columns up to N (~256–512, ranked by row presence), the tail spills into
four typed map columns, and column names encode the type
(`f_<key>__i/__f/__b/__s`) so the same key with two types never collides.
`shred.go` will be the single parse function shared by the compactor and the
tail reader, so a key-filtered query returns the same result for a line
before and after compaction.

## Milestones

1. **Skeleton + catalog.** *Done.* Package, sqlite catalog, reconciliation,
   manager pass loop.
2. **Raw compaction.** *Done except wiring.* Sealed-WAL buckets → parquet
   with the fixed schema, full commit ordering, WAL unlink, retention, and a
   `Query` that reads tail-then-archive newest-first. `LogArchiveDir` is in
   `ainit`. What remains is runtime wiring: starting `Manager.Run` from
   `app/primary/run.go` and `app/secondary/secondary.go`, and switching the
   two `logreader.StreamLogs` call sites to
   `Manager.Query`. **Those must land together** — starting the compactor
   without switching the querier deletes WAL files the UI still reads from,
   and switching the querier without the compactor returns nothing from an
   empty archive. Worth a full e2e run before release.
3. **Shredding.** `shred.go`, dynamic schema, `log_file_keys`, key-filtered
   queries.
4. **Day roll-up.** L1 merge pass.
5. **Live tail shortening (optional).** Incremental consumption of the active
   bucket: same range machinery, gated on `size - consumed >= liveTailBytes`
   (~8MB) so quiet deployments don't produce dribble files, and the WAL is
   never unlinked while unsealed. Note that a late writer can append an
   earlier timestamp above a consumed offset, so output ranges may overlap —
   the planner already tolerates that since it prunes by range rather than
   assuming disjointness.
6. **S3 upload.** Identical keys under a bucket prefix; `seq` gaps drive
   reconciliation. Cross-node L2 merge stays deferred.

## Revisions to the design doc

- **Commit point moved from the filename to the sqlite transaction.** The doc
  said "presence of a well-formed name *is* the commit" with sqlite as
  derivable state. That cannot hold once WAL consumption is tracked by byte
  offset: a filename cannot express "consumed bucket X through byte 4,193,280",
  and without that the crash window between committing a parquet and unlinking
  its WAL either loses records or duplicates them. The catalog is still
  rebuildable — from footers, not from names.
- **Watermark routing replaced by per-file consumed offsets.** Same purpose,
  one source of truth, and it generalises to partially-consumed active WALs.
- **Level numbering shifted**: L0 = batch, L1 = node day roll-up, L2 =
  cross-node. The doc used L1 for the cross-node merge.
- The doc's "reader overlap handling" section is superseded by invariant 5:
  overlap is impossible because the tail is defined as the unconsumed byte
  range, not as "files that still exist".
