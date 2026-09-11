# Log compaction and parquet storage design

## Purpose

Design for compacting raw `.logbin` deployment logs into parquet files for
structured querying, covering the on-disk file/directory scheme, column
shredding, file sizing, query routing for fresh data, and the node-level and
cross-node compaction passes. Files are stored locally on each node first; an
S3-backed location comes later and uses the identical key scheme, so every
naming decision here must hold without cross-node coordination.

What has shipped is described in
[log-storage.md](../engineering/log-storage.md); where this document and the
shipped behaviour differ (the commit point is the sqlite transaction, not the
file name; the tail is the range above the commit marker, not a watermark;
files are committed by raw size rather than per bucket), the engineering
doc describes what exists. The next steps are in
[logmanager-implementation-plan.md](logmanager-implementation-plan.md).

## Existing substrate

- Raw logs are written by the per-container log consumer into per-deployment
  dirs as `<bucket>_<version>_<run>.logbin`, bucketed into 30-minute UTC
  windows (`logBucket` in `backend/lib/log/record.go`). Writes are unbuffered
  per line, so a logbin is durable to the last line the consumer drained.
- Each logbin has a single writer process. Record timestamps are stamped with
  `time.Now()` inside the consumer at pipe-read time, and the bucket a record
  lands in is chosen from that timestamp — including reopening an older
  bucket file (`O_CREATE|O_APPEND`) for a late-stamped record.
- The structured form compaction produces is the parquet schema itself: the
  identity columns (time, version, run, node, instance ordinal, stream, seq),
  the parsed `level` and `msg`, the original line as `raw_message`, and the
  shredded key columns described below. There is no proto message for it;
  the query API returns `LogRecord`.

## WAL v2: shared per-deployment bucket files

Supersedes the per-(version, run) logbin naming. Shipped and wired in: the
container runner spawns the v2 consumer (`backend/app/logconsumer/v2`,
writing via `backend/lib/log/v2`), the system log writer appends v2 records,
and the querier (`logreader`) reads `.wal` files only via a validating
backward reader. Legacy v1 `.logbin` files are deliberately ignored by the
querier — pre-upgrade logs are not visible and age out with retention; the
v1 consumer and writer code remain in-tree for reference. Where the rest of
this document says "logbin", read "the deployment WAL".

- One WAL file per (deployment, bucket): `<bucket>.wal` in the deployment
  log dir, appended to by every active container run of that deployment on
  the node. Single-`write` `O_APPEND` appends on a local filesystem are
  kernel-serialized, so concurrent writers interleave whole records with no
  userspace locking. With one container active (the common case) the file
  has a single writer and behaves like a v1 logbin; the multi-writer
  machinery is exercised only during rollover overlap, scale-out, and crash
  loops (which previously minted one file per run per bucket).
- The consumer writes each stream directly to the WAL: no stdout/stderr
  merge channel. A line is durable the moment it is read from the pipe;
  there is no in-memory backlog to drain or lose at shutdown.
- Record format v2: `magic(4) | len(4) | payload | crc32c(4) | len(4)`,
  payload = `time(8) | version(4) | run(4) | stream(1) | line`. Magic and
  CRC exist because atomic appends are not all-or-nothing — a short write
  (the ENOSPC/quota boundary case) tears a record that can now sit mid-file
  ahead of other writers' good records — and because length-prefix framing
  alone cannot resync from an untrusted offset. The trailing length is kept
  for backward walking: until parquet ships, the WAL is the only store, the
  querier streams newest-first, and 4 bytes/record keeps tail reads
  proportional to data returned rather than file size. The backward reader
  validates every step (magic, CRC, trailer); on a bad record it scans
  backward for the nearest fully-validating record, so a torn region costs
  only the torn record.
- Drop-and-resume on write failure: a failed append never latches or kills
  the stream. The appender tracks the failure window (count, first/last
  time, last error) and on the next successful write first emits a
  synthetic marker line ("opendeploy: dropped N log lines between T1 and
  T2: err") on the same stream; a final marker attempt happens at close.
  Blocking/retrying was rejected — backpressure would stall the workload's
  stdout, converting a logging degradation into an availability incident.
- Reader caveats: timestamps within a file are only near-sorted (writers
  stamp then write, and those steps race across writers), so readers must
  not binary-search or early-terminate on time; version/run/stream
  filtering moves from filenames to record fields.

## Direction: the WAL is an ingestion boundary

The end-state framing is that the per-deployment WAL is a standard ingestion
boundary, and everything downstream of it is a *log ingestor backend* —
ours or someone else's. A shared per-node ingestor would tail the deployment
WALs, track a consumed index per file, and call `consume(line)` (or a batch
variant) as a standardised interface; our compaction pipeline is then just
one implementation of that interface, and shipping lines to an external
system (Loki, OpenObserve, S3 firehose) is another.

For our own native backend we deliberately special-case: we do **not**
introduce the interface or a tailing layer. The native backend already has
the WAL as its live store, so it reads WAL files directly for both the tail
query path and compaction input. The interface abstraction is only worth its
cost when a second, external consumer shows up. What we preserve of the
general direction is the *shape*: a consumed-index per WAL file (see
`wal_progress` below) is exactly the state a generic tailing ingestor needs,
so promoting the native path to the standard interface later is mechanical.

## File and directory layout

```
logs/<deployment_id>/<YYYYMMDD>/L<level>_<minUnixMs>-<maxUnixMs>_n<node>_<seq>.parquet
```

- **Directory levels are query partitions**: deployment id, then UTC day.
  Files never cross a day boundary. Day is the retention unit (delete whole
  day dirs) and the coarsest span a compacted file may cover. Hour dirs
  (OpenObserve-style) were rejected: they cap compacted file spans and buy
  prefix pruning we don't need because sqlite metadata is the query planner.
- **Filenames carry writer identity, exact range, and level.** Node is in
  the *name*, not a directory: it makes file production and S3 upload
  coordination-free (globally unique names, single writer per file,
  idempotent re-upload) without multiplying the file count per partition the
  way a node dir level would. Node/instance remain parquet columns for
  filtering — a constant column costs single-digit bytes after
  dictionary+RLE.
- **Exact min/max record timestamps (unix millis) in the name**, not bucket
  bounds. This makes the metadata layer rebuildable from a directory listing
  alone — no parquet footer reads on recovery — and keeps sqlite an index,
  not a source of truth.
- **`seq` is a random 63-bit number drawn per file.** It gives unconditional
  name uniqueness with no durable counter and no cross-node agreement, and
  it is the only part of the name that changes when a file is rewritten with
  the same rows, so the old and new file coexist during a swap.
- **The level tag is a processing ladder, not a compaction tier.** Each
  level implies everything below it: `L1` = shredded batch output, `L2` =
  node day roll-up, `L3` = cross-node merge (`L0`, unshredded batch output,
  existed before shredding shipped and was migrated away). Every
  rewrite re-runs shredding from `raw_message`, so a file's level says
  exactly what its columns are. The maintenance loop rewrites any file below
  the target level for its stage; the implementation plan has the protocol.
- Instance ordinal is deliberately *not* in the path or name: it changes
  across restarts within a bucket, it is a cheap column, and merging
  would have to erase it anyway.
- S3 later: identical keys under a bucket prefix; nodes upload their own
  files with zero coordination.
- Reserve a namespace for system logs now (`logs/system/...` or a sentinel
  deployment id) so the scheme covers them without migration.

Every identity field is also a column inside the file, including those
duplicated in the path — self-describing files work with external
tools (DuckDB) without path-parsing conventions and survive moves/merges.

## Column shredding: threshold hybrid

Per-file schema: every parsed key becomes a real parquet column until the
file's leaf-column budget is spent. The budget is **400 leaf columns**,
counted over key columns only; identity columns and the spill maps sit
outside it. Over budget, the densest keys get columns, ranked by row
presence within the batch (deterministic, so consecutive files from the same
workload converge to the same schema), and the tail spills into four typed
MAP columns (`spill_int`, `spill_float`, `spill_bool`, `spill_str`).
Precedent: ClickHouse's JSON type (typed subcolumns up to
`max_dynamic_paths`, overflow to a shared column) and the Parquet VARIANT
shredding spec in Iceberg v3.

Rationale over the alternatives:

- *All keys as columns, always*: fine to a few hundred columns, degrades
  badly (footer bloat, writer memory, tiny pages, schema-union churn) when
  workloads emit generated key names, which we don't control.
- *Fully adaptive per file*: handles everything but makes every deployment's
  schema dynamic; well-behaved users pay the complexity tax for misbehaving
  ones.
- *Threshold hybrid*: teams that keep keys bounded get stable, fully-shredded
  schemas and never interact with the adaptive machinery; hostile keyspaces
  degrade gracefully instead of blowing up the writer.

### Types

- **The JSON value type is the column type, never coerced.** A key that
  arrives as `"39"`, `31` and `38.1` in one batch yields three columns,
  `f_foo__s`, `f_foo__i` and `f_foo__f`. Column names encode the type
  (`f_<key>__i/__f/__b/__s`) so variants never collide, and the query
  resolves across them (below). This is what the columnar systems do
  (ClickHouse Dynamic, Parquet VARIANT, Snowflake). Coercing at ingest
  (Elasticsearch, Honeycomb) is where mapping conflicts and silent truncation
  come from.
- **Int versus float is decided by the number's text.** `2` is int64; `2.0`
  and `1e3` are float64. JSON has one number type, so the text is the only
  signal, and producers differ (JavaScript and Go print a whole-valued float
  as `2`, Python as `2.0`), so one logical field can land in both columns.
  Accepted: the query reads both, at the cost of one extra column for the
  affected keys. Integer text outside the int64 range goes to the float
  column.
- **A key's variants are placed together.** Either every type variant of a
  key is a dense column or every one is in the spill maps. Ranking is per
  key, and a key is admitted only if all its variants fit the remaining
  budget, so "where is `foo` in this file" is one lookup.
- **Nested objects flatten to dotted keys** (`obj.subfield`), depth-capped.
  A literal key containing a dot collides with the nested path; accepted, as
  in ClickHouse and VictoriaLogs. Arrays stay JSON text in the string
  variant, matched per element. JSON `null` is absent: no variant, and
  `exists` is false. Duplicate keys in one object: last wins. `level`, `msg`
  and `message` lift into the fixed columns; `version`, `node`, `run`,
  `instance` and `stream` are query names for identity columns and shadow
  JSON keys of the same name.

### Query resolution

Filters compile to typed literals once, then bind per file to whichever
variants the catalog says exist there. A literal that parses as a number
compares numerically against the int and float variants and textually
against the string variant, so `foo = 68` matches `68`, `68.0` and `"68"`.
Range operators (`gt`/`gte`/`lt`/`lte`) touch only the numeric variants.
`contains` runs over the textual rendering of any variant. The WAL tail
applies the same rules over the line scanner's typed spans through the
shared shred function, so a line answers identically before and after
compaction. A key absent from a file short-circuits without opening a
column.

### Writing

The compactor writes from a complete sealed bucket, so the schema is fixed
by a tally pass before the write pass: the line scanner walks every record
counting rows per key and the kinds seen, without materialising values. The
tally is bounded to a few thousand distinct keys per batch; keys first seen
after the bound are uncounted and spill. Dense keys appear in the first rows
of any batch, so the bound only costs a hostile keyspace. Rare keys under
budget are promoted too: a near-empty optional column is a few hundred bytes
per row group and far cheaper to filter than the spill map.

The per-file cost that scales with columns is the footer: one column-chunk
metadata entry per column per row group, decoded on every open. At 400
leaves and 128k-row groups that is about 1MB for a busy file. Open files
with page indexes and bloom filters skipped and load the time column's index
on demand, and watch the untruncated ColumnMetaData min/max on string
variants.

## File sizing

Roll at `min(target size, partition boundary)` — never time-only (2KB files
for quiet deployments, multi-GB for chatty ones) and never size-only (files
straddling retention cutoffs, unbounded staleness).

- **Batch files (L1)**: one parquet per 64MB of raw WAL bytes, or per day
  for deployments that never reach it. Size, not time, is what bounds the
  raw tail scan.
- **Roll-up (L2)**: merge within a day once the day's batch files reach
  256MB of parquet, and sweep the remainder at day end; each byte is
  written twice.
- Rows sorted time-major; row groups sized so per-group min/max time stats
  give intra-file pruning. This matters more for query latency than the
  file-level scheme.

Resist shrinking buckets or compacting eagerly for freshness: small parquet
files are the disease; the logbin tail (below) is the cure.

## Liveness: logbin serves the tail

Parquet is never in the freshness path. Parquet is a sealed-batch format
(readable only once the footer is written); continuously flushing a working
file means tiny-file explosion, O(n²) rewrites, or torn reads. Every
comparable system (OpenObserve, Loki, InfluxDB IOx, VictoriaLogs) queries a
WAL/ingester buffer for recent data and columnar storage for history. Our
logbin is that WAL, with better durability than most (unbuffered per-line
writes).

- **Routing is by watermark, not "the active bucket"**: per (deployment,
  node), "compacted through T". Sealed range → parquet (planned via sqlite);
  everything above the watermark → scan-and-parse the logbin tail. Compactor
  lag or crash is then a performance event, never a data-visibility event.
- Invariant: a logbin is deleted only after its parquet is committed, so
  union(parquet, remaining logbins) always covers everything.
- **The parse/shred function is shared** between the compactor and the tail
  reader — one function, two callers — so a key-filtered query returns
  identical results for a line before and after compaction.
- Tail cost is bounded: roughly one bucket + grace (~35 min) per deployment
  per node.
- Follow mode (later, if wanted) bypasses this entirely: tail the logbin
  file and push lines (the Datadog/Loki-tail pattern); never build it on the
  parquet path.
- S3-phase consequence: history comes from parquet/S3, but live-tail queries
  must always reach the node owning the logbin. The query planner's
  "parquet set + tail endpoints" split is a permanent, first-class concept.

## Node-level compactor (WAL → L1)

- **Input**: all eligible WAL files for a deployment (`<bucket>.wal`),
  processed strictly oldest-first. The filename gives the bucket time, so
  eligibility is decidable from the name alone; version/run/stream come from
  the records. Legacy `.logbin` files are never compacted — retention
  deletes them.
- **Trigger: wall clock only.** A bucket is sealed when
  `bucket_end + grace < now`, grace = **2 minutes** to start. Sealing is
  inferred, never observed: writers are separate processes (no IPC), close
  may come arbitrarily late (a quiet workload holds the last bucket file
  open until the next line rolls it) or not at all, and closed ≠ sealed
  (`O_APPEND` reopen is legitimate — the system log writer reopens the same
  bucket file across agent restarts). Because timestamps are stamped at
  write time and buckets are chosen from timestamps, a passed wall clock
  makes new records for the bucket impossible.
- The grace budget covers: normal pipeline lag (ms), cross-stream stamp
  reorder (ms), NTP steps (seconds), and — the dominant term — backpressure
  or I/O stalls where stamped lines sit in the consumer's channel for a
  while. Note the correlation: stamp-to-disk lag blows up exactly when the
  node is unhealthy, which is when sealing early would be worst.
- **Note — richer seal signals later.** Additional evidence can prove "no
  more writes" earlier than the grace: a later bucket WAL exists for the
  deployment, or the agent knows every container run of the deployment on
  the node has exited. These are
  accelerators only — each has gaps on its own (silence produces no
  successor file; reopen-after-close is legal) — so the wall-clock grace
  remains the correctness backstop. Start with the 2-minute grace alone;
  add accelerators only if compaction latency ever matters.
- **List-driven, oldest-first.** The compactor rediscovers work by listing
  files each pass, never by remembering "bucket done". This makes grace
  violations degrade instead of cliff: a straggler record recreates its
  (already compacted and deleted) bucket file, and the next pass compacts it
  into a second batch file for the same range — which the `seq` naming permits. Cost
  is a few minutes of invisibility for the late lines, not loss.
- **Commit sequence**: write `*.tmp` → fsync → rename → insert sqlite row →
  advance watermark → delete source logbin. Presence of a well-formed name
  *is* the commit; sqlite rows are derivable state, rebuildable by listing.
- Reader overlap handling: in the crash window (parquet committed, logbin
  not yet deleted) the logbin is a superset — prefer parquet. In the
  straggler case the recreated logbin is disjoint — union. Names alone can't
  distinguish these, so the sqlite row records the consumed logbin's
  identity (path + record count/byte size at consumption), making "is this
  logbin already represented" exact.

## Cross-node merger (L2 → L3) — deferred

Optional consolidation pass; correctness never depends on it. At current
scale (~150 batch files per deployment per day across a few nodes) the
node-level passes are enough; add the cross-node merge when file counts or
S3 GET costs justify it.

- **Trigger: previous day + grace**, never "all nodes advanced". Gating on
  node-set completeness turns the merger into a barrier that a dead node
  blocks forever, forcing a timeout override anyway. Merge whatever files
  exist for day D once D+grace has passed.
- Record exactly which inputs produced each merged file (sqlite). A
  late-arriving file from a recovered node stays valid standalone — the
  planner reads all levels by name ranges — and a later sweep can fold
  stragglers into a supplementary merge.

## Metadata layer (later phase)

Sqlite stores per-file: time range, node, level, record count, schema
identity (hash), which keys are dense columns vs present in spill maps, and
consumed-source identity. Used for query planning (file pruning, column-vs-
spill resolution per key per file) and upload tracking. It is an index over
the filesystem, not a source of truth: everything except spill-key inventories
is recoverable from names alone, and the rest from footers.

## Open questions

- The size caps (pick during implementation; the design is insensitive
  within the stated ranges).
- Retention policy shape (per-deployment day counts; day dirs make deletion
  trivial).
- Tail RPC design for cross-node queries in the S3 phase.
