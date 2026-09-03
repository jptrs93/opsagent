# Container resource metrics implementation plan

## Purpose and scope

Default node-local measurement of per-container resource usage: CPU, memory,
pressure, process count, block IO, network IO, TCP connection counts, and disk
usage (volumes and writable snapshot layer). This document covers the
measurement layer and node-local storage. Transmission, cross-node query, and
presentation are out of scope and decided later.

## Locked-in direction

- **Node-level sampler, not per-runner collectors.** One sampler per agent
  process owns the tick, the run registry, per-read timeouts, and the slow disk
  tier. Runners contribute only registration and deregistration.
- **cgroup-direct reads, not `task.Metrics`.** containerd is not in the
  sampling path. The kernel's cgroup v2 files are read directly from
  `/sys/fs/cgroup`. This removes the shim/gRPC dependency (a wedged shim cannot
  stall sampling) and is runtime-uniform: any unit that runs in its own cgroup
  (containerd task, systemd unit, future runtimes) is sampled the same way.
- **cgroup as the accounting unit, not the process.** Pure per-process
  `/proc/<pid>` accounting was considered and rejected: the task PID is only
  pid 1, summing a process tree loses the CPU of exited children, tree walks
  race against fork/exit, and summed RSS over-counts shared pages. cgroup
  counters are the kernel's atomic aggregate over the whole container
  including exited processes. The registered PID is used per tick only for
  the netns-scoped procfs reads.
- **One cgroup per run, from the container id.** Container ids are
  `opendeploy-{deploymentID}-{specVersion}-{scheduledInstanceID}-{run}`, and
  containerd's default OCI `cgroupsPath` is `/{namespace}/{id}`, so every
  run a node ever creates has its own cgroup under `/sys/fs/cgroup/opendeploy`
  without the runner setting a path: a respawn is a new directory, never a
  reused one. The runner hands the sampler the path read back from the task's
  OCI spec, which also covers containers adopted from an older agent under
  the legacy `opendeploy-{deploymentID}-v{specVersion}` name. The installer's
  containerd config uses the cgroupfs driver, under which the path is taken
  literally. `/proc/<pid>/cgroup` is read once at registration as a tripwire
  that logs a mismatch; it is verification, not resolution.
- **Placement in the key.** Container IDs, volume dirs, and log dirs are
  keyed on deployment ID alone and would collide if two placements of one
  deployment landed on a node. The metrics key carries
  `ScheduledInstanceID` and `Ordinal` so the sampler does not inherit that
  limitation.
- **cgroup v2 only.** The sampler checks for `cgroup.controllers` at the
  mount root on start and disables itself with a warning otherwise.
- **Two sampling tiers.** A fast tick for counter/gauge reads that cost
  microseconds, and a slow tick for disk usage, which requires filesystem
  walks.
- **Raw cumulative counters, not rates, stored as-is.** Samples record
  counters plus a timestamp; nothing differences at ingest. Every run has its
  own cgroup, so a counter is monotonic for the life of a `TargetKey` and the
  only reset is a new key: the query layer differences samples of the same
  key and never across keys. Raw counters survive missed ticks (two samples
  90s apart still give an exact 90s average), continue across an agent
  restart (reattach adopts the same cgroup), and downsample by dropping
  samples without losing totals.
- **Terminal sample on deregistration.** A cgroup outlives its processes
  until the task is deleted, so `Registration.Close` reads it once more and
  delivers a sample marked `Terminal`: the run's final totals (CPU consumed,
  bytes written, `oom_kill`, peak memory). It is the only sample a run
  shorter than one interval ever gets.
- **Aligned ticks, actual timestamps.** Ticks fire on wall-clock multiples
  of the interval (`nextTick`), so every node's samples share bucket
  boundaries and a missing bucket is a visible gap. `Sample.Time` is the
  actual read time, not the boundary, so rates divide by real elapsed time.
  All samples in a tick share one timestamp.

## Registry

`lib/metrics.Sampler` (process-wide `metrics.Default`, started next to
`netaudit` in primary and secondary) holds a map of registered runs. Runners
register a target and hold the returned `Registration`, whose `Close`
deregisters:

```
TargetKey  {DeploymentID, ScheduledInstanceID, Ordinal, SpecVersion, Run}
TargetSpec {Key, PID, CgroupsPath, HostNetwork}
```

- Registration keys on the run, so a rollover candidate and the incumbent are
  two concurrent entries for the same deployment.
- `Register` never fails. A target whose cgroup cannot be located still
  registers and yields empty samples.
- The internal OpenDeploy self-deployment is not registered yet. It would pass
  its systemd unit cgroup (readable from `/proc/self/cgroup`) with no PID.
- A target whose files vanish mid-tick yields an empty sample; the runner's
  deregistration follows within the tick. The first failure per registration
  is logged at debug.

### Runner hook points (`lib/engine/runner/container.go`)

- Fresh spawn: `registerSampling(task, runNumber)` after `RunTask` and the
  inbound-address claim succeed, before the STARTING/RUNNING status publish, so
  a run visible as running in the store is being sampled.
- Reattach: `registerSampling(task, currentRunNumber())` after `LoadTask` and
  network recovery succeed. The adopted task never stopped, so this is the
  same run and the same cgroup; counters continue across the agent restart and
  the run number does not increment.
- Deregister: `deleteTask`, which every teardown path (crash, stop, rollover
  candidate failure, adopted-task stop) calls exactly once after the process
  has exited. The crash path then re-enters the spawn path with `run+1` and a
  new cgroup.

## Metrics and sources

### Fast tier (per tick, per registered run)

cgroup v2 files under the resolved cgroup path:

| Metric | File | Kind |
| --- | --- | --- |
| CPU usage, user/system, throttled | `cpu.stat` | counters |
| Memory current, peak (5.19+), breakdown, oom_kill events | `memory.current`, `memory.peak`, `memory.stat`, `memory.events` | gauge + counters |
| Block IO bytes/ops per device | `io.stat` | counters |
| Process/thread count | `pids.current` | gauge |
| CPU/memory/IO pressure (PSI) | `cpu.pressure`, `memory.pressure`, `io.pressure` | gauge (avg) + counter (total stall us) |

procfs via the registered PID (netns-scoped, no `setns` required):

| Metric | File | Kind |
| --- | --- | --- |
| Network rx/tx bytes, packets, drops per interface | `/proc/<pid>/net/dev` | counters |
| TCP connection counts by state | `/proc/<pid>/net/tcp`, `/proc/<pid>/net/tcp6` | gauge |
| Open file descriptor count | `/proc/<pid>/fd` (dirent count) | gauge |

Host-network runs get no network or TCP metrics: they share the host
namespace, so per-container attribution does not exist without eBPF, which is
out of scope. The sampler records the omission rather than host-wide numbers.

### Slow tier

| Metric | Source | Kind |
| --- | --- | --- |
| Volume disk usage | walk of `{dataDir}-volumes/{deploymentID}` | gauge |
| Writable snapshot layer usage | containerd snapshotter `Usage()` | gauge |

Walks run one volume at a time with yield throttling to bound CPU and page
cache disturbance. Filesystem project quotas (O(1) accounting) are a possible
later optimization requiring installer/filesystem support; not in this plan.
The snapshotter `Usage()` call is the one containerd dependency and it is
confined to the slow tier; its failure degrades to a missing sample.

## Cadence

- Fast tier: 10 seconds (`metrics.DefaultInterval`), matching cAdvisor's
  housekeeping interval and the 10-second window of the kernel's PSI `avg10`
  fields. Cost per container per tick is 9 cgroup file reads plus 4 procfs
  reads; the cgroup reads are microseconds, the TCP tables and fd listing
  scale with the container's socket and descriptor counts but stay in the
  low milliseconds even for busy servers — well under 0.1% of a core for
  tens of containers.
- Slow tier: 10 minutes, first walk on registration deferred behind a short
  delay so respawn churn does not trigger walks.
- All fast-tier reads in a tick share one sample timestamp, so per-node
  snapshots are coherent and summable.

## Output contract

Each tick produces one `metrics.Sample` per registered run:

```
{Key, Time, Terminal, Cgroup{CPU, Memory, IO, Pids, CPUPressure, MemoryPressure, IOPressure}, Net{rx/tx counters, TCP states}, OpenFDs}
```

`Time` is shared by every sample in the tick. Every section is a pointer that
is nil when its file was unreadable; `Net` is nil for host-network runs. Nil
means "could not read" and is distinct from zero; a serialised form needs
presence semantics, not default-zero.

Metric kinds, which fix what the query layer does with each field:

- Counters (monotonic within a key; rate = Δvalue / Δtime, per-run total =
  last value): `cpu_usage/user/system/throttled_usec`, `cpu_nr_throttled`,
  `io_*_bytes/ops`, `net_*`, `mem_oom`, `mem_oom_kill`,
  `psi_*_total_usec`.
- Gauges (point-in-time; rollups keep min/max/last): `mem_current`,
  `mem_peak`, `mem_anon/file/kernel/shmem`, `pids`, `tcp_*`, `open_fds`.
- Kernel-computed averages (already rates; never differenced or summed):
  `psi_*_avg10/60/300`.
The batch goes to a `metrics.Consumer`; the consumer is the node-local store
below (`metricstore.Store`). Sampling failures produce absent metrics, never
synthesized values, and never feed back into runner health or operator
decisions.

## Storage

Node-local storage follows the log storage skeleton in
[log-compaction.md](log-compaction.md): a live WAL, day buckets, compaction to
parquet, watermark routing between them, node identity in filenames, and
day-directory retention. Metrics are a simpler workload than logs — a fixed
schema, one in-process writer, a known and small volume — and the design
drops what the log design pays for those problems.

### Record

One `MetricsSample` message in `api-contract/model/metrics.proto` defines
the row for the WAL payload, the parquet schema, and any later API shape.
Metric fields are `optional` scalars so absence is presence-encoded, never
zero. (cleanproto before v1.24.0 dropped zero-valued optionals on encode, so
a fresh cgroup's `cpu_usage_usec = 0` decoded as absent; the generator pin
was raised to v1.24.0 for this.) `apigen.MetricsSample` is the store's only
public row type; the parquet struct is an internal mirror with snake_case
column tags, copied field-for-field by name and checked complete by a test.
The parquet row is a flat mirror of the message:

```
time                  TIMESTAMP(ms)   deployment_id          INT32
scheduled_instance_id INT32           ordinal                INT32
spec_version          INT32           run                    INT32
node_id               INT32           terminal               BOOLEAN

cpu_usage_usec cpu_user_usec cpu_system_usec cpu_throttled_usec cpu_nr_throttled
mem_current mem_peak mem_anon mem_file mem_kernel mem_shmem mem_oom mem_oom_kill
io_read_bytes io_write_bytes io_read_ops io_write_ops
pids
psi_{cpu,mem,io}_{some,full}_total_usec
net_{rx,tx}_{bytes,packets,dropped}
tcp_established tcp_listen tcp_time_wait tcp_close_wait tcp_other
open_fds                                                    INT64 optional

psi_{cpu,mem,io}_{some,full}_{avg10,avg60,avg300}           DOUBLE optional
```

About 55 columns. A wide row is chosen over narrow `(key, time, metric,
value)` rows because the metric set is fixed; there is no shredding,
threshold, or spill machinery. Rows within a file are sorted by the full
key `(deployment_id, scheduled_instance_id, ordinal, spec_version, run)`
then `time`, key-major rather than the log store's time-major: a run's
counters are then monotonic within a column run and delta-encode to a few
bytes per row, row-group statistics on `deployment_id` prune per-deployment
queries (row groups are 16k rows so the pruning is fine-grained), and the
rows a rate query differences are contiguous.

Sizing: roughly 80 bytes per compressed row, 8640 rows per container-day at
10s, about 700 KB per container-day and 35 MB per day for 50 containers.
Full resolution is kept; long-range query cost is addressed by rollup and
local disk by bucket upload (both below), not by thinning the data.

### Layout

```
<data>-metrics/<YYYYMMDD>.wal                          live file for the day
<data>-metrics/<YYYYMMDD>/n<node>_<seq>.parquet        sealed day
```

The root is `ainit.StaticConfig.MetricsDir` (`/var/lib/opendeploy-metrics`
installed), a sibling of the log roots.

Partitioning is per node-day, not per deployment as for logs. Logs partition
by deployment because each container has its own writer process and queries
are per deployment. The metrics sampler is one in-process writer that emits a
row for every run on each tick, and "every run on this node now" is as
common a query as "this deployment over time". One append per tick writes the
whole batch; deployment is a dictionary-encoded column. Node in the filename
and a per-day `seq` are kept as in the log design so later S3 upload is
coordination-free.

### WAL

One writer goroutine appends, so the WAL needs framing for the short-write
case (`u32 len | payload | u32 crc32c`, payload = `MetricsSample` binary
proto) and nothing else: no magic for resync among interleaved writers, no
trailing length for backward walking (a day file is a few megabytes and is
read forward in one `ReadFile`), no drop-and-resume markers. The CRC helper
in `lib/log/v2` is reused; the log `Appender` is not, because its payload
header is log-specific. A reader stops at the first frame that is short or
fails its CRC and reports how many records it kept; on the live file that is
the expected in-progress tail, at compaction it is logged. A batch is
written with one `write` call, and the file for a day is chosen per sample
time, so terminal samples and a batch straddling midnight land in the right
day. No fsync on the WAL; durability comes with the sealed parquet.

`metricstore.Store.Consume` must return promptly (it runs on the tick and on
deregistering runners under the sampler lock), so it converts the batch,
updates the last-sample cache, and hands the batch to the writer goroutine
through a channel of depth 64; when the channel is full the batch is dropped
and the drop streak is logged once at its start and once when it ends.

### Compaction

Sealing is known, not inferred: the sampler stamps and writes from one
goroutine, so day D is complete once the first record of D+1 is written. The
maintenance loop (at start and hourly) lists WAL files and compacts every day
before today-minus-ten-minutes (the grace covers clock steps around midnight)
oldest-first into one parquet per node-day, and commits by `tmp → fsync →
rename → fsync dir → delete WAL`. A well-formed parquet name is the commit.
No sqlite catalog: the files are self-describing by name, the schema is
fixed, and a directory listing is the planner.

Rows stream from the WAL through a `parquet.SortingWriter` with a 64k-row
sort buffer spilling to files in the day directory, so a day of any size
compacts in bounded memory; splitting a node-day into several files was not
needed and is not done. The `seq` in the name is one above the highest
existing seq for the node in that day directory. A crash between rename and
WAL delete, or a clock step that reopens a compacted day's WAL, therefore
produces a second file rather than replacing the first: duplicates over data
loss. Stray `.tmp` files in the day directory are removed before writing.
An empty WAL is deleted without producing a file.

### Query routing

Sealed days are read from parquet; today from the WAL. The union is
complete because a WAL is deleted only after its parquet is committed. The
query primitives in `metricstore` (no endpoints yet):

- `Scan(ctx, dir, node, Query, yield)` streams matching samples in file
  order over the days a `Query{From, To, DeploymentID, ScheduledInstanceID}`
  covers (`[From, To)`, zero bounds open, zero ids match all). Per day it
  reads every parquet file in the day directory (any node's, so a later
  bucket sync can drop other nodes' files in) and then the WAL, unless a
  parquet for this node already exists for that day, in which case the WAL
  is a not-yet-deleted duplicate and is skipped. Per-deployment queries skip
  row groups whose `deployment_id` page statistics exclude the id.
- `Collect` runs `Scan`, sorts by key then time, and drops exact
  `(key, time)` duplicates, which is what the double-commit case above
  produces. `GroupByKey` splits that into per-run series.
- `Fields` lists every metric with its kind (`Counter`, `Gauge`, `Average`)
  and accessor; `Rate(prev, cur, field)` is the only counter arithmetic:
  same key, later time, non-decreasing value, else no result.
- `Store.Latest()` is the last non-terminal sample per registered run, kept
  in memory by `Consume`; a terminal sample removes its key. It is a cache
  over the WAL for the live view, not a store. `LatestPairs` also keeps the
  sample before it, so `LatestEntry` can report a per-second rate for every
  counter without touching disk.
- `Rollup(ctx, dir, node, RollupRequest)` is the query-time engine behind
  the API: it aligns the range to the step (`ChooseStep` picks from a 10 s
  to 24 h ladder for about 300 points when none is given; `AlignRange`
  truncates the start to the step), scans `[start - 3 min, end)` so the
  first bucket's rate has a predecessor, and emits one `Series` per (run,
  field). Samples are folded into per-field bucket sums as the scan streams
  them, so memory is one previous sample plus the bucket arrays per run
  rather than the whole range; a sample at or before a run's previous one
  (a re-compacted day's duplicate) is skipped. The WAL read for an unsealed
  day peeks each frame's time and deployment id (the first two fields) and
  only fully decodes the frames the query can use. Counters become per-second rates: each consecutive sample pair of
  a run is differenced with `Rate`, and the rate is spread over the buckets
  the pair's interval overlaps, weighted by overlap, so a bucket's value is
  the mean rate over the time it has data for whatever the step. A reset
  drops that one pair. Gauges and kernel averages are the bucket mean of
  the samples inside it. A bucket with nothing is `NaN`. Nothing is
  pre-differenced or pre-aggregated; every query runs from raw samples.

`Store` methods delegate to the package functions with the store's directory
and node id so tests and tools run against a directory without a sampler.

### Rollup and retention

Retention deletes whole day directories (and any stray WAL) older than
`metricstore.DefaultRetention`, 90 days, in the same maintenance pass as
compaction. Local disk space is not what retention length is decided by: the target for data volume is uploading
sealed parquet files to bucket storage under the same key scheme, as
planned for logs, after which local retention is a cache window and history
is read from the bucket.

A later rollup pass over sealed days exists for query efficiency on long
time ranges, not for space. A 30-day chart at 10s resolution reads 259,200
rows per run; at 5-minute resolution it reads 8,640 and the plotted result
is the same. The pass emits 5-minute rows into the same schema with a
`resolution` column: for counters that is keeping every tenth sample (the
raw counter still carries exact totals, so rates over any window at or above
the rollup resolution are exact), for gauges min/max/last, for kernel
averages last. The query planner picks the coarsest resolution that
satisfies the requested range and step. This falls out of storing raw
counters; it would not with pre-differenced deltas.

## Status

Wired in: the `lib/metrics` package (registry, fast-tier cgroup and procfs
reads including `memory.peak`, terminal sample on `Close`, aligned ticks),
`ctrd.Task.CgroupsPath()` read from the OCI spec of created and adopted
containers, runner registration at the hook points above,
the `MetricsSample` proto, and the `lib/metrics/metricstore` package (WAL
writer, compactor, retention, `Scan`/`Collect`/`Latest`/`Rate` primitives).
Primary and secondary start `metricstore.Default` on
`ainit.StaticConfig.MetricsDir` with their node id and run the sampler into
it at 10 seconds.

The e2e metrics flow verifies sampling of freshly started containers and of
containers adopted after an agent upgrade. A real day roll and compaction on
a node has not been observed yet.

Served: `POST /v1/metrics/query` (rollup, fanned out by the primary to every
node holding an instance of the deployment) and `POST /v1/metrics/latest`
(live overview fanned out to all connected nodes), proxied over the cluster
session like the log query, with the Metrics page in the web UI on top
(see `docs/engineering/api.md` and `docs/engineering/frontend.md`). The e2e
flow deploys `testexamples/loadgen` on a worker and on the primary and
checks the overview, the charts, the scope controls, and that adopted
containers are sampled again after an agent upgrade.

Not yet: the slow disk tier, the self-deployment target, rollup, and bucket
upload. A leaked-cgroup sweep is no longer needed: a cgroup belongs to a
container, and the runner removes leftover containers of its family before
every start.

`open_fds` is read for every run with a pid, host-network runs included,
since descriptors are per process rather than per network namespace. The
`/proc/<pid>/fd` listing is ptrace-gated, so the installer's service unit
grants `CAP_SYS_PTRACE`; without it the field is nil for containers whose
processes run as a different uid than the agent.

## Non-goals

- Within-container per-process breakdown (`/proc` tree accounting) — possible
  later drill-down feature, not default telemetry.
- Network attribution for host-network runs.
- eBPF-based accounting of any kind.
- Alerting, thresholds, transmission of raw samples between nodes.
