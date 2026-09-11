# Log storage

Node-local storage and query of workload and system logs: the per-deployment
WAL, the collector that commits it to parquet, the sqlite catalog, the
maintenance loop that rewrites and expires archive files, and the query
engine. Code lives in `backend/lib/log` (`v2` frame format, `logmanager`
collector, maintenance and query, `logconsumer` raw capture). The design
rationale is in [log-compaction.md](../future-work/log-compaction.md); the
remaining open items are in
[logmanager-implementation-plan.md](../future-work/logmanager-implementation-plan.md).

## Layout

```
<LogWALDir>/<deployment_id>/<YYYYMMDD_HHMM>.wal
<LogArchiveDir>/<deployment_id>/<YYYYMMDD>/L<level>_<minUnixMs>-<maxUnixMs>_n<node>_<seq>.parquet
<LogArchiveDir>/logdb.sqlite
```

- WAL buckets are 30-minute UTC windows. Every container run of a deployment
  on the node appends to the same bucket file; deployment `0` is the system
  log. A record is routed to the bucket its own timestamp belongs to, so a
  record never lands in a bucket file outside its time window.
- A WAL frame is `magic 0xfd | len | payload | crc32c | len`. The payload
  carries a format-version byte, the record metadata (version, run, stream,
  node, instance ordinal), the timestamp, a per-(run, stream) monotonic
  `seq`, and the line bytes. Records are self-describing; nothing is
  inferred from the file name except the bucket. Legacy `.logbin` files are
  ignored.
- Archive files never cross a UTC day. `seq` is a random 63-bit number per
  file and is the file's identity in the catalog. `level` is a processing
  ladder; each level implies everything below it, and every rewrite
  re-shreds from `raw_message`.

| Level | Meaning | Written by |
| --- | --- | --- |
| 1 | Batch output, shredded | Every commit |
| 2 | Node day roll-up, shredded over the merged batch | Roll-up |

Every archive file is shredded; there is no unshredded level. The number
starts at 1 because a pre-shredding level 0 existed once and was migrated
away.

## Shredder

`shred.go` and `lineview.go` hold the one parse used by the commit path,
the rewrite paths and the query path. The line scanner walks the JSON in
place without allocating and falls back to a `json.Decoder` walk for lines
it declines; a property test pins both paths to the same keys, types and
values.

- The column type is the JSON value type, never coerced. `2` is int64;
  `2.0` and `1e3` are float64; integer text outside int64 goes to float. A
  key with mixed types has one variant per type: int, float, bool, str.
- Nested objects flatten to dotted keys up to depth 8; deeper objects stay
  JSON text in the str variant. Arrays stay JSON text and match per
  element. `null` is absent, and a later `null` hides an earlier value.
  Duplicate keys: last wins. Keys longer than 256 bytes are dropped.
- Top-level `level`, `msg` and `message` are lifted into the fixed columns
  and are not fields. `level` is upper-cased; a null `msg` falls back to
  `message`.
- The display map returned by the API keeps the original number text, so
  `2.0` renders as `2.0`.

## Collector and commit

`logmanager.Manager` runs in both the primary and the secondary agent. It
owns one `LogStreamCollector` per deployment, started when a scheduled
instance of the deployment is starting, running or crashed on the node, or
when a WAL directory exists for it.

The collector streams WAL records forward from the commit marker. While a
producer is alive it follows the tail, polling the current bucket file each
second; a bucket counts as closed one minute after its end. Each record
feeds the live spool, which tracks the uncommitted byte ranges per day and
per-minute level counts for the tail query path.

A commit is triggered when the oldest spooled day is past its deadline (day
end plus one minute), when a spooled range reaches 64MB of raw WAL bytes,
or when the last producer exits. The check runs per record and on a
one-minute tick so a silent workload still commits. The size trigger bounds
the WAL tail that filtered queries scan raw.

Commit sequence for one range:

| Step | Action |
| --- | --- |
| 1 | Tally pass: re-read the range from the WAL, shred each line, count rows per key and type; at most 4,096 distinct keys are counted, later keys spill |
| 2 | Fix the schema: rank keys by row presence, admit a key only if all its variants fit within the 400 dense leaf budget |
| 3 | Write pass: re-read the range sorted by record key, shred, write `<seq>.parquet.tmp` (zstd, 128k-row groups, `sorted=1`); resort through a sorting writer if the sliding-window sort overflowed |
| 4 | One transaction: `INSERT log_files` at level 1, `INSERT log_file_keys` per variant, upsert the commit marker with the final file name |
| 5 | Rename to the final name, fsync the day directory |
| 6 | Delete WAL buckets entirely before the marker, and the marker's bucket once it is fully consumed and past its grace |
| 7 | Nudge the maintenance loop |

The transaction is the commit point. If the process dies between steps 4
and 5, the next start finds the marker's file name missing on disk and
finishes the rename; if the file has since been rewritten by maintenance
(no catalog row for that `seq`), the marker is left as is. Leftover `.tmp`
files older than the rewrite grace are deleted at collector start.

## Parquet schema

Every archive file has the fixed columns `time`, `version`, `run`, `node`,
`instance_ordinal`, `stream`, `seq`, `level`, `msg` and `raw_message`.
Rows are sorted by `(time, node, instance_ordinal, run, stream, seq)`.
`raw_message` is the original line bytes and is always present.

Every file adds one optional leaf `f_<key>__<t>` per dense variant (`t` is
`i`, `f`, `b` or `s`) and four maps `spill_int`, `spill_float`,
`spill_bool`, `spill_str` keyed by field name for every other variant.
Readers open files with the page
index and bloom filters skipped; scans load a column index on demand for
pruning, and the row fetch loads each chunk's offset index so seeking to a
row is a page lookup rather than a decompressing walk from the chunk start.

## Catalog

`logdb.sqlite` is opened through `storage/sqlitedb` and holds three tables.

- `log_files`: one row per archive file. `deployment_id`, `day`, `seq`
  identify the file; `level`, `node`, `min_time`, `max_time` (unix nanos),
  `row_count`, `byte_size`, `created_at`. The scan index is
  `(deployment_id, max_time DESC, min_time)`.
- `log_file_keys`: one row per `(file, key, type)` variant present in a
  level 1 or 2 file with its placement (dense or spill) and row count.
  Cascades on file deletion.
- `log_stream_commit_marker`: one row per deployment. `day`, `bucket`,
  `record_time`, `byte_offset` are the WAL position of the last committed
  record; `file` is the final name of the last archive file, kept for the
  rename recovery above.

WAL files have no catalog. Their identity is the file name, and the marker
is the only durable fact about them.

## Maintenance

`Manager` runs one maintenance goroutine per process. It wakes on a commit
nudge or every minute and runs one roll-up job per wake;
every hour it first reconciles the catalog with the disk. Jobs select from
the catalog alone, so the loop has no state across restarts.

Every rewrite follows one protocol: check free space for twice the input
bytes, write provisional outputs and rename them to final names, fsync the
day directory, verify the row count and time bounds against the input rows
(on mismatch remove the outputs, log, and back off that input for an
hour), then one transaction inserting the output rows and keys and
deleting the input rows and keys. The input files are unlinked one minute
later. The grace exists because a query snapshots the catalog and opens
files by path afterwards. A crash before the transaction leaves rowless
output files; a crash after leaves rowless input files; reconciliation
removes both.

- **Roll-up (level 1 to 2).** Per `(deployment, day)`: when the level 1
  files sum to at least 256MB of parquet, or the day ended more than 15
  minutes ago and at least two level 1 files remain, they k-way-merge by
  record key into level 2 output, split so each file is about 256MB. A lone
  level 1 file is left alone.
- **Reconciliation.** Final-named archive files without a catalog row and
  catalog rows without a file are removed, each only once older than the
  grace and never while a rewrite still holds the file for its deferred
  unlink; `.tmp` files past the grace are removed. Then retention deletes
  archive days older than 30 days (rows first, then the directory) and WAL
  buckets and legacy `.logbin` files older than the cutoff.

## Query

`POST /v1/deployments/log-query` is served by `Manager.Query`. The primary
answers for its own node and forwards a query with a `target_node_id` to
the owning secondary over the cluster session.

- Filters AND together. Ops: `eq`, `neq`, `in`, `exists`, `not_exists`,
  `contains`, `not_contains`, `gt`, `gte`, `lt`, `lte`. A range op with a
  non-numeric value is rejected. An empty field name addresses the message
  text; `level` and `msg` address the derived columns; `version`, `node`,
  `run`, `instance` and `stream` address record metadata and shadow JSON
  keys of the same name; any other field is a parsed JSON key.
- A literal is parsed as int, float, bool and text at once, and the same
  rules apply to the WAL tail and to columns. `text: true` on a
  filter forces text comparison; the query bar sets it for quoted values.

| Op | int / float | str | bool |
| --- | --- | --- | --- |
| `eq`, `neq`, `in` | numeric compare when the literal is numeric | case-insensitive text | literal `true`/`false` |
| `gt`, `gte`, `lt`, `lte` | numeric compare | never matches | never matches |
| `contains`, `not_contains` | textual rendering | text, per element for arrays | textual rendering |
| `exists`, `not_exists` | any variant present | | |

- Defaults and caps: window 12h, limit 5,000 (also the cap), histogram at
  most 300 buckets, field stats over the newest 5,000 matched records with
  at most 200 field names and the top 10 values each.
- The engine snapshots the commit marker, the file list and the catalog
  keys for the filtered fields in one read transaction. The WAL tail is
  answered from the spool's minute aggregates and scanned raw only where
  they do not suffice. Per archive file each field filter binds to the
  dense columns or spill maps holding the key's variants, or is resolved
  as absent: the file is skipped when an absent key cannot match, and the
  filter is dropped when it matches every row. Row groups are pruned on
  the time index and, for numeric filters against dense numeric columns,
  on the column index.
- Files are scanned in parallel through an errgroup capped at
  `scanParallelism` workers (half the cores, at most eight, so a query does
  not starve the collectors or a roll-up). Each worker owns its histogram
  counters, its bounded retain heap and its scanner scratch; the results
  are merged in file order afterwards, so the response and the trace are
  independent of scheduling. The retained rows are fetched after the merge.
  `forceFullScan` reads every raw line instead and is the test oracle for
  the column path.

## Not yet present

The field sidebar from `log_file_keys`, the display map from columns
instead of `raw_message`, a level 3 cross-node merge, and S3 upload. See
the implementation plan.
