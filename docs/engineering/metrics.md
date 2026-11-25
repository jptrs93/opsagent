# Container resource metrics

## Purpose and scope

Default node-local measurement of per-container resource usage: CPU, memory,
pressure, process count, block IO, network IO, TCP connection counts, and the
disk usage of each deployment's volume. This document covers the
measurement layer (`lib/metrics`) and its node-local storage
(`lib/metrics/metricstore`). The query endpoints and their fan-out are in
[api.md](api.md) and the Metrics page in [frontend.md](frontend.md). The
Status section at the end records what is wired in and what is not.

Each agent also samples its own host: CPU, load, memory, pressure, network,
disk IO and filesystem usage. [Node metrics](#node-metrics) covers that
second table; everything before it is about the per-container table.

## Direction

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
  OCI spec, for created and adopted containers alike. The installer's
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
TargetKey  {DeploymentID, ScheduledInstanceID, Ordinal, DeploymentVersion, Run}
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

### Slow tier: volume usage

| Metric | Source | Kind |
| --- | --- | --- |
| `volume_bytes` | walk of `{dataDir}-volumes/{deploymentID}`, the sum of allocated blocks | held |
| `volume_inodes` | the same walk, a count of entries | held |

A volume is a plain host directory bind-mounted into the container, so
nothing keeps a running total for it: the kernel has no size for a directory
tree, `statfs` on the mount reports the whole host filesystem, and a cgroup
counts IO, not space held. The agent walks the directory. The walk is
`lib/metrics/volume.go`.

**What a walk counts.** One `lstat` per entry and no file contents, so the
cost follows the number of files, not their size. Bytes are allocated blocks
(`st_blocks` × 512, what `du` prints), so a sparse file counts what it holds.
Directories and symbolic links count as entries; a link is not followed. A
file with several hard links counts once. A directory on another device (a
mount inside the volume) is skipped. An entry that vanishes during the walk
is skipped.

**Which directories.** Every directory in the volume root whose name is a
deployment id, listed every 30 seconds. That includes a stopped deployment's
volume and one left on a node the deployment has moved away from. A
directory the volume sweep has removed drops out at the next listing.

**When.** A new directory is measured at the listing that finds it. After
that each volume is measured every 30 minutes (`metrics.VolumeInterval`) on
its own slot: the 30 minutes are offset by a spread of the deployment id, so
the walks of different volumes do not land together. The first listing runs
one minute after the agent starts, and measures every volume once in id
order. The slot that follows a measurement is always at least 15 minutes
later.

**Bounds.** One walk runs at a time. A walk pauses after every 2048 entries
for as long as those entries took (at most 200 ms), which halves its share
of a core and of the disk. A walk that runs longer than 10 minutes is
abandoned: the volume keeps its earlier reading, a warning is logged once,
and the next slot tries again.

**How a reading is stored.** A reading is not a row of its own. On the tick
after a walk, the sampler writes the reading on the sample of one run of
that deployment (the lowest key when several are registered), and on no
other row. Almost every row therefore has no volume. A deployment with no
running container has no row to carry a reading, so its history has a gap
while it is stopped; the live answer below still lists it.

Not built: the usage of a container's writable snapshot layer, and project
quotas. Project quotas (ext4 `project`, XFS `prjquota`) would give the same
numbers without a walk and would also allow a size limit per volume, but
they need the filesystem feature and a mount option that our nodes do not
have, so a walk stays the fallback either way.

## Cadence

- Fast tier: 10 seconds (`metrics.DefaultInterval`), matching cAdvisor's
  housekeeping interval and the 10-second window of the kernel's PSI `avg10`
  fields. Cost per container per tick is 9 cgroup file reads plus 4 procfs
  reads; the cgroup reads are microseconds, the TCP tables and fd listing
  scale with the container's socket and descriptor counts but stay in the
  low milliseconds even for busy servers — well under 0.1% of a core for
  tens of containers.
- Slow tier: each volume every 30 minutes on its own slot, the first pass
  one minute after the agent starts. A walk belongs to a volume directory,
  not to a run, so a respawn starts none.
- All fast-tier reads in a tick share one sample timestamp, so per-node
  snapshots are coherent and summable.

## Output contract

Each tick produces one `metrics.Sample` per registered run:

```
{Key, Time, Terminal, Cgroup{CPU, Memory, IO, Pids, CPUPressure, MemoryPressure, IOPressure}, Net{rx/tx counters, TCP states}, OpenFDs, Volume{Bytes, Inodes}}
```

`Volume` is nil except on the one sample that carries a new volume reading.

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
- Held readings (a value that stays true until the next reading):
  `volume_bytes`, `volume_inodes`. The rollup carries the last reading
  forward and returns one series per deployment and node, not per run.
The batch goes to a `metrics.Consumer`; the consumer is the node-local store
below (`metricstore.Store`). Sampling failures produce absent metrics, never
synthesized values, and never feed back into runner health or operator
decisions.

## Storage

Node-local storage follows the log storage skeleton in
[log-storage.md](log-storage.md): a live WAL, day buckets, compaction to
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
(spec_version holds the top-level deployment version; the column name predates that)
node_id               INT32           terminal               BOOLEAN

cpu_usage_usec cpu_user_usec cpu_system_usec cpu_throttled_usec cpu_nr_throttled
mem_current mem_peak mem_anon mem_file mem_kernel mem_shmem mem_oom mem_oom_kill
io_read_bytes io_write_bytes io_read_ops io_write_ops
pids
psi_{cpu,mem,io}_{some,full}_total_usec
net_{rx,tx}_{bytes,packets,dropped}
tcp_established tcp_listen tcp_time_wait tcp_close_wait tcp_other
open_fds
volume_bytes volume_inodes                                  INT64 optional

psi_{cpu,mem,io}_{some,full}_{avg10,avg60,avg300}           DOUBLE optional
```

About 57 columns. A wide row is chosen over narrow `(key, time, metric,
value)` rows because the metric set is fixed; there is no shredding,
threshold, or spill machinery. Rows within a file are sorted by the full
key `(deployment_id, scheduled_instance_id, ordinal, spec_version, run)`
(the `spec_version` column carries the placement's top-level deployment
version since v0.0.611; older files hold the spec version)
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
<data>-metrics/<YYYYMMDD>.wal                                    live file for the day
<data>-metrics/<YYYYMMDD>/d<deployment>_n<node>_<seq>.parquet    sealed day of one deployment
<data>-metrics/<YYYYMMDD>/n<node>_<seq>.manifest                 what one compaction sealed
```

The root is `ainit.StaticConfig.MetricsDir` (`/var/lib/opendeploy-metrics`
installed), a sibling of the log roots.

The live file is per node-day, not per deployment as for logs. Logs
partition by deployment because each container has its own writer process.
The metrics sampler is one in-process writer that emits a row for every run
on each tick, and one append per tick writes the whole batch.

The sealed form is one file per deployment per node-day. Compaction splits
the day by `deployment_id`, so a per-deployment query opens one file and a
chunk record names the metrics of one deployment
([chunk-archive.md](chunk-archive.md)). Node in the filename and a per-day
`seq` are kept as in the log design, so the bucket key of a file needs no
coordination.

A file named `n<node>_<seq>.parquet` in a run-table day directory was sealed
before the split. It holds every deployment of the day. It is still read,
it has no manifest, and it is never listed as a chunk.

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
oldest-first. The run table gets one parquet per deployment, the node table
one for the day. No sqlite catalog: the schema is fixed, and a directory
listing is the planner.

One compaction takes one `seq`, one above the highest for the node in that
day directory, and commits in this order:

1. Write every file as `<name>.parquet.tmp` and fsync it.
2. Write the manifest `n<node>_<seq>.manifest`: tmp, fsync, rename, fsync
   the directory.
3. Rename every `.tmp` to its final name and fsync the directory.
4. Delete the WAL.

The manifest is the commit. It is a JSON file that lists each sealed file
with its deployment, row count, time range, size and SHA-256, and it carries
the SHA-256 of the WAL it was built from. `SealedFiles` reads the manifests,
so a sealed file is described without opening it.

The next compaction of a day first finishes or discards what an interrupted
one left (`recoverDay`). A `.tmp` file that a manifest names is renamed. Any
other `.tmp` file is removed. A manifest that cannot be read is removed.
When a manifest already carries the hash of the WAL, the WAL is deleted and
nothing is written, so a crash between the renames and the WAL delete does
not produce a second copy. That holds only while every file the manifest
names is on disk, renamed or not. A manifest of the WAL that names a missing
file is removed with the files it does have, and the day is compacted again
under the next `seq`: the WAL is then the only complete copy. A clock step that reopens a compacted day's WAL
produces a second set of files under the next `seq`: duplicates over data
loss.

The WAL is read once into memory. The rows of each file stream through a
`parquet.SortingWriter` with a 64k-row sort buffer spilling to files in the
day directory. A WAL with no records is deleted without producing a file.

A file sealed before manifests existed gets one from the maintenance loop
(`backfillManifests`), for the node table only: the file is read once for
its row count and time range and hashed.

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
  is a not-yet-deleted duplicate and is skipped. While a compaction of this
  node is between its manifest and its last rename, some files of its `seq`
  have their final name and some are still `.tmp`: the scan skips every
  file of a `seq` that still has a `.tmp` and reads the WAL instead. A
  per-deployment query skips the files of other deployments by name, and in
  a file sealed before the split it skips row groups whose `deployment_id`
  page statistics exclude the id.
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
- `Store.LatestResponse` also returns `volumes`: the last reading of every
  volume directory on the node, with when it was taken and how long the walk
  took. It comes from the sampler through `Store.Volumes`, not from the
  rows, so it lists a volume whose deployment is stopped.
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
- Held fields are rolled up apart from the rest. The readings of a
  deployment are gathered across all of its runs on a node and sorted by
  time. A bucket takes the last reading at or before its end, as long as
  that reading is less than 75 minutes older than the bucket's start
  (`heldFor`, two intervals and a margin, so one missed walk leaves no gap)
  and the deployment has a row at or after the bucket's start. The scan
  reaches 75 minutes before the range for these fields, so a short range
  still shows the reading that was current. The series key carries the
  deployment only. The `scheduled_instance_id`, `deployment_version` and
  `run` scopes of a query do not apply: a reading sits on whichever run was
  there to carry it.

`Store` methods delegate to the package functions with the store's directory
and node id so tests and tools run against a directory without a sampler.

### Rollup and retention

Retention deletes whole day directories (and any stray WAL) older than
`metricstore.DefaultRetention`, 90 days, in the same maintenance pass as
compaction. Local disk space is not what retention length is decided by: sealed files are sent to bucket
storage under the same key scheme ([chunk-archive.md](chunk-archive.md)).
Reading history from the bucket, after which local retention is a cache
window, is not built.

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

## Node metrics

Each agent samples its own host on the same tick as the containers. The node
sample is a second table beside the run table: one row per node per tick,
stored and queried by the same code.

### Sampler

`metrics.RunNode` reads the host once at start and then on every aligned
10 s tick. Every read is a file under `/proc` or one `statfs` call. Nothing
is spawned and no directory is walked. A part that cannot be read is absent
from that sample and the rest is still written.

| Part | Source | Fields |
|------|--------|--------|
| CPU | `/proc/stat`: the `cpu` line, and a count of the `cpuN` lines | `cpu_usage_usec` (user, nice, system, irq, softirq, steal), `cpu_user_usec` (user, nice), `cpu_system_usec` (system, irq, softirq), `cpu_iowait_usec`, `cpu_steal_usec`, `cpu_count` |
| Load | `/proc/loadavg` | `load1`, `load5`, `load15` |
| Memory | `/proc/meminfo` | `mem_total`, `mem_available`, `mem_free`, `mem_cached` (Buffers plus Cached), `swap_total`, `swap_free` |
| Pressure | `/proc/pressure/{cpu,memory,io}` | the `psi_*` names of the run table |
| Network | `/proc/net/dev` | `net_{rx,tx}_{bytes,packets,dropped}` |
| Disk IO | `/proc/diskstats` | `io_{read,write}_{bytes,ops}` |
| Filesystems | `statfs` | `fs_data_*`, `extra_filesystems` |

CPU ticks are converted to microseconds at the fixed `USER_HZ` of 100, so the
CPU counters have the unit of the run table and a rate divided by 1e6 is
cores. Idle time is not stored; `cpu_count` gives the capacity.

Network and disk IO count physical devices only: an interface with a
`/sys/class/net/<if>/device` link and a block device with a
`/sys/block/<dev>/device` link. That leaves out loopback, veth pairs, bridges
and tunnels, and partitions, device-mapper, md and loop devices, so a byte is
counted once however many virtual layers it crosses.

### Filesystems

The supported layout keeps every OpenDeploy directory on one filesystem, the
one that holds the data directory. That filesystem is five flat columns:
`fs_data_total_bytes`, `fs_data_free_bytes`, `fs_data_avail_bytes`,
`fs_data_inodes_total` and `fs_data_inodes_free`. Flat columns chart, roll up
and sort like any other gauge.

`extra_filesystems` is a map from mount point to the same five numbers. It
holds every other filesystem an OpenDeploy root sits on (volumes, containerd,
nix stores, assets, releases, logs, metrics, build logs), found by comparing
device ids, so a root on the data filesystem adds nothing and the map is
empty on the supported layout. The key is the mount point, found by walking
up from the root while the parent directory is on the same device.

The map is part of the format from the first file because a map column
cannot be added later: parquet-go reads a file written without the column
back with one entry under an empty key. The reader drops empty keys, and a
test pins that for a hand-written older file. A scalar column added later
reads as absent from older files, which is the normal way to extend the row.

`free` is every free block and `avail` is what an unprivileged writer can
still use (`f_bavail`), which is lower when the filesystem reserves blocks
for root. Used is total minus free. The percentage the UI shows is used over
used plus available, the figure `df` prints.

### Storage and query

```
<data>-metrics/node/<YYYYMMDD>.wal                     live file for the day
<data>-metrics/node/<YYYYMMDD>/n<node>_<seq>.parquet   sealed day
<data>-metrics/node/<YYYYMMDD>/n<node>_<seq>.manifest  what one compaction sealed
```

`metricstore` is generic over the sample type. A `table[S, R]` holds what
differs between the two tables (decode and encode of the proto, the time
accessor, the mapping to the parquet row, the sort order), and the WAL
writer, compaction, retention, scan routing and the rollup accumulator are
written once over it. `Store` is the run table and `NodeStore` the node
table; both run the same write and maintenance loops. Node rows sort by time.
The frame format, the commit protocol and the 90 day retention are those
described above. The node table is not split: a compaction seals one file
for the day.

The run store lists `<data>-metrics` and skips every name that is not a day,
so the `node` directory is invisible to it, and to an older agent after a
rollback.

- `NodeFields` lists the stored columns with their kind plus four gauges
  derived at read time: `mem_used` (total minus available), `swap_used`,
  `fs_data_used_bytes` (total minus free) and `fs_data_inodes_used`.
- `NodeRollup` is the rollup of the run table with one accumulator: counters
  become per-second rates, gauges and averages become bucket means, an empty
  bucket is `NaN`.
- `NodeStore.LatestResponse` is the last sample with the per-second rate of
  every counter against the sample before it, both kept in memory.

`extra_filesystems` is stored and returned with the latest sample but is not
a rollup field: the rollup works on named scalar fields, and charting a map
entry needs a field that carries a label.

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
containers adopted after an agent upgrade. The e2e archive checks compact a
copied WAL under a past day on the primary and a worker and compare every
sealed file with its object in the bucket. A real day roll on a node has not
been observed under the per-deployment layout yet.

Served: `POST /v1/metrics/query` (rollup, fanned out by the primary to every
node holding an instance of the deployment) and `POST /v1/metrics/latest`
(live overview fanned out to all connected nodes), proxied over the cluster
session like the log query, with the Metrics page in the web UI on top
(see `docs/engineering/api.md` and `docs/engineering/frontend.md`). The e2e
flow deploys `testexamples/loadgen` on a worker and on the primary and
checks the overview, the charts, the scope controls, and that adopted
containers are sampled again after an agent upgrade.

Node metrics are wired in the same way: primary and secondary start
`metricstore.DefaultNode` on `<metrics>/node` and run `metrics.RunNode` into
it with the roots from `metrics.OpenDeployRoots()`. They are served by
`POST /v1/metrics/nodes/query` and `POST /v1/metrics/nodes/latest`, shown on
the Metrics page (Nodes overview and one tab per node) and in the Disk column
of the Nodes page. The e2e metrics flow checks both nodes report CPU, memory
and filesystem figures, that a node's charts carry data, and that the Disk
column fills in.

Volume usage is wired in: primary and secondary run
`metrics.Default.RunVolumes` on `ainit.StaticConfig.VolumesDir` and hand
`metrics.Default.Volumes` to the store. `POST /v1/metrics/latest` returns the
last reading of every volume, `POST /v1/metrics/query` serves `volume_bytes`
and `volume_inodes`, and the Metrics page shows a Volume column and chart.
The e2e metrics flow checks both load generators report a measured volume in
the overview and in their charts.

Not yet for node metrics: charts of `extra_filesystems` entries and
host-mount paths of deployments as a source for that map.

Not yet: the writable snapshot layer's usage, the self-deployment target,
stored rollup (the rollup engine runs at query time), and reading from the
bucket. Sealed files are recorded as chunks and sent to the bucket when the
archive is on ([chunk-archive.md](chunk-archive.md)). A leaked-cgroup sweep is no longer needed: a cgroup belongs to a
container, and the runner removes leftover containers of its family before
every start.

`open_fds` is read for every run with a pid, host-network runs included,
since descriptors are per process rather than per network namespace. The
`/proc/<pid>/fd` listing is ptrace-gated. The agent runs as root, which
passes the gate; on a node still running the unprivileged unit of an older
install the field is nil for containers whose processes run as another uid
unless that unit grants `CAP_SYS_PTRACE`.

## Non-goals

- Within-container per-process breakdown (`/proc` tree accounting) — possible
  later drill-down feature, not default telemetry.
- Network attribution for host-network runs.
- eBPF-based accounting of any kind.
- Alerting, thresholds, transmission of raw samples between nodes.
