# Log compaction and parquet storage design

## Purpose

Design for compacting raw `.logbin` deployment logs into parquet files for
structured querying, covering the on-disk file/directory scheme, column
shredding, file sizing, query routing for fresh data, and the node-level and
cross-node compaction passes. Files are stored locally on each node first; an
S3-backed location comes later and uses the identical key scheme, so every
naming decision here must hold without cross-node coordination.

The concrete build-out lives in
[logmanager-implementation-plan.md](logmanager-implementation-plan.md):
package layout, the sqlite catalog schema, commit ordering and crash windows,
query planning, and milestones. It revises a few decisions here (commit point,
level numbering, watermark routing) and says so explicitly; where the two
disagree, the implementation plan wins.

## Existing substrate

- Raw logs are written by the per-container log consumer into per-deployment
  dirs as `<bucket>_<version>_<run>.logbin`, bucketed into 30-minute UTC
  windows (`logBucket` in `backend/lib/log/record.go`). Writes are unbuffered
  per line, so a logbin is durable to the last line the consumer drained.
- Each logbin has a single writer process. Record timestamps are stamped with
  `time.Now()` inside the consumer at pipe-read time, and the bucket a record
  lands in is chosen from that timestamp — including reopening an older
  bucket file (`O_CREATE|O_APPEND`) for a late-stamped record.
- `StructuredLogLine` (`api-contract/model/logs.proto`) is the structured
  format compaction produces: time, deployment/version/run/instance/node/
  stream identity, plus four typed key maps (int/float/bool/str).

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
logs/<deployment_id>/<YYYYMMDD>/L0_<minUnixMs>-<maxUnixMs>_n<node>_<seq>.parquet
logs/<deployment_id>/<YYYYMMDD>/L1_<minUnixMs>-<maxUnixMs>_<ulid>.parquet
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
- **`seq` is per (deployment, node, day)**, derived by the single writer as
  the count of existing files in the day dir (no durable counter). It gives
  unconditional name uniqueness (without it, uniqueness depends on never
  splitting a file mid-millisecond) and trivial gap detection for S3 upload
  reconciliation ("node 3 uploaded seq 0–17, 12 is missing").
- **Level tag**: `L0` = fresh per-node compactor output; `L1` = optional
  cross-node merged output, the S3 upload unit of choice.
- Instance ordinal is deliberately *not* in the path or name: it changes
  across restarts within a bucket, it is a cheap column, and L1 merging
  would have to erase it anyway.
- S3 later: identical keys under a bucket prefix; nodes upload their own
  files with zero coordination.
- Reserve a namespace for system logs now (`logs/system/...` or a sentinel
  deployment id) so the scheme covers them without migration.

Every `StructuredLogLine` field is also a column inside the file, including
those duplicated in the path — self-describing files work with external
tools (DuckDB) without path-parsing conventions and survive moves/merges.

## Column shredding: threshold hybrid

Per-file schema: all keys become real parquet columns up to a threshold N
(~256–512 distinct keys). Beyond N, the top-N dense keys (ranked by
row-presence within the batch — deterministic, so consecutive files from
the same workload converge to the same schema) get columns; the tail spills
into four typed MAP columns mirroring the proto (`spill_int`, `spill_float`,
`spill_bool`, `spill_str`). Precedent: ClickHouse's JSON type (typed
subcolumns up to `max_dynamic_paths`, overflow to a shared column) and the
Parquet VARIANT shredding spec in Iceberg v3.

Rationale over the alternatives:

- *All keys as columns, always*: fine to a few hundred columns, degrades
  badly (footer bloat, writer memory, tiny pages, schema-union churn) when
  workloads emit generated key names — which we don't control.
- *Fully adaptive per file*: handles everything but makes every deployment's
  schema dynamic; well-behaved users pay the complexity tax for misbehaving
  ones.
- *Threshold hybrid*: teams that keep keys bounded get stable, fully-shredded
  schemas and never interact with the adaptive machinery; hostile keyspaces
  degrade gracefully instead of blowing up the writer.

Type conflicts are already disambiguated by the proto's typed maps: the same
key in `int_fields` and `str_fields` is two logical fields. Column naming
encodes this (`f_<key>__i/__f/__b/__s`) so shredded columns can never
collide. Never coerce types.

Since the compactor writes from complete sealed buckets, two-pass writing
(scan batch → fix schema → write) is free; there is no streaming-schema
problem.

## File sizing

Roll at `min(target size, partition boundary)` — never time-only (2KB files
for quiet deployments, multi-GB for chatty ones) and never size-only (files
straddling retention cutoffs, unbounded staleness).

- **L0**: one parquet per sealed 30-minute bucket, unless the bucket exceeds
  a size cap (~32–64MB), in which case split — only at millisecond
  transitions, so ranges in names stay honest.
- **L1**: merge within a day toward ~128–512MB for S3 economics.
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

## Node-level compactor (WAL → L0)

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
  into a second L0 for the same range — which the `seq` naming permits. Cost
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

## Cross-node merger (L0 → L1) — deferred

Optional consolidation pass; correctness never depends on it. At current
scale (~150 L0 files per deployment per day across a few nodes) L0-only is
fine; add L1 when file counts or S3 GET costs justify it.

- **Trigger: previous day + grace**, never "all nodes advanced". Gating on
  node-set completeness turns the merger into a barrier that a dead node
  blocks forever, forcing a timeout override anyway. Merge whatever L0s
  exist for day D once D+grace has passed.
- Record exactly which inputs produced each L1 (sqlite). A late-arriving L0
  from a recovered node stays valid standalone — the planner reads L0 ∪ L1
  by name ranges — and a later sweep can fold stragglers into a
  supplementary L1.

## Metadata layer (later phase)

Sqlite stores per-file: time range, node, level, record count, schema
identity (hash), which keys are dense columns vs present in spill maps, and
consumed-source identity. Used for query planning (file pruning, column-vs-
spill resolution per key per file) and upload tracking. It is an index over
the filesystem, not a source of truth: everything except spill-key inventories
is recoverable from names alone, and the rest from footers.

## Open questions

- Exact dense-key threshold N and the size caps (pick during implementation;
  the design is insensitive within the stated ranges).
- Retention policy shape (per-deployment day counts; day dirs make deletion
  trivial).
- Tail RPC design for cross-node queries in the S3 phase.
- Whether L1 outputs re-run shredding over the merged batch (better schemas)
  or concatenate L0 schemas (cheaper); leaning re-run, since the merger
  already reads every row.
