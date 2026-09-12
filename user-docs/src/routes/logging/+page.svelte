<script lang="ts">
  import CodeBlock from '$lib/CodeBlock.svelte';
  import CodeTabs from '$lib/CodeTabs.svelte';
  import { logEventVariants } from '$lib/log-event';
  import {
    eventShape,
    walLayout,
    droppedMarker,
    archiveLayout,
    queryExamples,
  } from '$lib/logging-snippets';
</script>

<svelte:head>
  <title>Logging - OpenDeploy Docs</title>
</svelte:head>

<h1 class="page-title">Logging</h1>
<p class="lead">OpenDeploy ships a complete built-in logging system suitable for production use without additional configuration. This page describes the design of that system.</p>

<section class="page" id="goals">
<h2>Design overview</h2>
<p>Every container instance has a <a href="#capture">log consumer</a> process that consumes its stdout and stderr streams. All workloads should write their logs directly to stdout or stderr.</p>
<p>Streams are split by newline, and every line is considered an <strong>event</strong>. Each event has a timestamp, recorded when the consumer sees the line, a sequence number, and source information such as deployment, instance ordinal, node, and stream.</p>
<p>An example log event looks like this:</p>
<CodeBlock code={eventShape} language="json" title="Log event example" />

<p>Log events are globally uniquely ordered by timestamp, with ties broken by sequence number and source information. This gives downstream collectors and query engines a consistent order for sorting and merging events.</p>

<h3 id="structure">Parsed payload</h3>
<p>When a log line is valid JSON, it is also parsed into a loosely typed, general-purpose structure. Every field is stored with a suffix <code class="inl">__&lt;T&gt;</code>, where <code class="inl">T</code> is determined by its JSON value type:</p>
<table>
  <thead><tr><th>Type suffix</th><th>JSON value</th></tr></thead>
  <tbody>
    <tr><td><code class="inl">__i</code></td><td>Integer</td></tr>
    <tr><td><code class="inl">__f</code></td><td>Float</td></tr>
    <tr><td><code class="inl">__s</code></td><td>String</td></tr>
    <tr><td><code class="inl">__ai</code></td><td>Array of integers</td></tr>
    <tr><td><code class="inl">__af</code></td><td>Array of floats</td></tr>
    <tr><td><code class="inl">__as</code></td><td>Array of strings</td></tr>
  </tbody>
</table>
<p>Nested fields are flattened to dot-joined keys, such as <code class="inl">obj.a__i</code>. Arrays of objects or mixtures of strings and numbers use <code class="inl">__as</code>, with non-string elements represented as JSON text.</p>
<p>In theory, other structured log formats could also be parsed. However, for now JSON is the only format OpenDeploy parses.</p>

<h3>Log event schema</h3>
<p>The full log event can be typed as:</p>
<CodeTabs variants={logEventVariants} title="LogEvent" />

<h3 id="storage">Write-ahead log</h3>
<p>Log event records are written to binary, append-only WAL files, shared per deployment on each node and rotated in 30-minute UTC buckets. Records within a file are not necessarily sorted, but each event is written to the bucket for its timestamp:</p>
<CodeBlock code={walLayout} language="text" title="WAL layout" />

<p>An independent <a href="#collector">log collector</a> process streams the WAL files and compacts records into compressed column-based storage, currently Parquet. Compaction is triggered after a given amount of log data is produced, currently 64 MB, or on a UTC day transition. The files are further merged into larger files targeting up to about 256 MB of compressed data. Log events are bucketed by UTC day, so quiet workloads typically have a single Parquet file per day and noisy workloads have many. All events within a Parquet file are sorted by their global event order.</p>

<h3>Columnar storage compaction</h3>
<p>During compaction to columnar format, a decision must be made about which fields get their own columns and which remain in generic spill columns. OpenDeploy uses a straightforward heuristic for each compacted file:</p>
<ul>
  <li>The most frequently occurring leaf fields get their own columns, within a budget of 400 leaves; everything else goes into typed spill columns. Different type variants of the same key, such as <code class="inl">user__i</code> and <code class="inl">user__s</code>, are never split between dedicated columns and spill storage.</li>
</ul>
<p>It is recommended you choose deliberately which values your workload writes as named fields in structured logs, rather than allowing a long tail of arbitrary keys. Keep field names stable and put changing identifiers in their values.</p>
<p>Each compacted log file includes statistics about its columns to support more efficient querying. A catalogue of the compacted files is stored in SQLite. At query time, the <a href="#search">query engine</a> uses this catalogue and the column statistics to create a query plan.</p>
<p>Because each key can exist as several type variants, a query for <code class="inl">user:5</code> fans out across the variants of <code class="inl">user</code> and combines their matches, whether they are in dedicated columns or spill storage. The engine also reads the uncommitted WAL tail, so recent events are searchable without waiting for compaction.</p>
</section>

<section class="page" id="collector">
<h2>Log collector</h2>
<p>Each node runs one collector for each deployment with active producers or WAL data on disk. The collector follows the shared WAL, tracks the uncommitted ranges, and converts them into a sorted Parquet archive. Consumers do not wait for compression, indexing, or archive maintenance.</p>
<p>The collector also maintains a live view of the uncommitted tail, including per-minute level counts. Recent events remain queryable while they are still in the WAL; archival is a storage transition, not the point at which a line becomes visible.</p>

<h3>When a batch is committed</h3>
<p>A collector commits a batch when any of these conditions holds:</p>
<ul>
  <li>An uncommitted range reaches 64 MB of raw WAL data.</li>
  <li>The UTC day containing the events has ended, with one minute of grace.</li>
  <li>The deployment's last producer on the node exits.</li>
</ul>
<p>Timer checks ensure that a quiet workload still commits without needing another log line to arrive. A batch never crosses a UTC day.</p>

<h3>From events to columns</h3>
<p>The collector makes a tally pass over the batch to count field presence, then a write pass to produce the archive. It sorts events by their timestamp and source discriminator, preserves their raw payloads, and writes the parsed fields as typed columns or typed spill maps. Parquet uses Zstandard compression.</p>
<p>Frequently occurring fields get dedicated columns, such as <code class="inl">f_obj.a__i</code>. Less frequent fields go into shared maps of the same types. The collector chooses placement separately for each file, within a budget of 400 dense field leaves. All type variants of a field are admitted together or spill together. This bounds schema growth when a workload emits many distinct keys, without dropping those fields from search.</p>
<p>Every file also carries fixed event metadata and the raw message. The top-level <code class="inl">level</code> field supplies the upper-cased display level; <code class="inl">msg</code>, or <code class="inl">message</code> as a fallback, supplies the display message. These derived columns make common message and level queries independent of the rest of the payload.</p>

<h3>Commit and restart recovery</h3>
<ol>
  <li>The collector writes the sorted batch to a temporary Parquet file.</li>
  <li>A recoverable commit publishes the finished archive and updates the SQLite catalog with the file, its fields, and the deployment's new WAL commit position. File publication includes the final rename and directory sync.</li>
  <li>Fully consumed WAL buckets behind the commit position are removed once their grace period has passed.</li>
</ol>
<p>The commit position identifies how far archival has progressed by WAL bucket and byte offset, not by timestamp alone. On restart, the collector resumes from that position and completes an interrupted final rename. This keeps ordinary process-crash recovery from skipping a committed range or exposing it twice.</p>

<h3>Archive layout</h3>
<CodeBlock code={archiveLayout} language="text" title="Archive layout" />
<p>Level 1 files are collector batches. Level 2 files are larger, merged batches for the same deployment and UTC day. The file sequence identifies an archive file; it is separate from the sequence number on each event.</p>
<p>The SQLite catalog records file time bounds, node, row count, size, and field placement, together with each deployment's commit position. The query engine uses this metadata to find relevant files without opening every archive file.</p>

<h3>Roll-up and retention</h3>
<p>Background maintenance merges small level 1 files into level 2 files, targeting about 256 MB of compressed Parquet per output. It runs when a day's batch files reach that size, or after the day ends when several smaller files remain. Roll-up preserves individual events and their ordering; it is not a downsampling operation.</p>
<p>Rewrites publish replacement files through the catalog before retiring their inputs, with a grace period for readers already using the old files. Maintenance also reconciles the catalog with files on disk after interruptions.</p>
<p>Logs are retained for 30 days. Retention removes expired archive days and old WAL buckets automatically. Storage is node-local, so provision enough disk for the workload's retained logs and the temporary space needed by compaction.</p>
</section>

<section class="page" id="search">
<h2>Query engine</h2>
<p>The query engine presents the archive and uncommitted WAL tail as one event history. A query selects a deployment, a node, a time range, and filters. The primary authorises the request and routes it to the node holding the data; that node performs the search.</p>

<h3>One query across both tiers</h3>
<p>The engine snapshots the archive catalog and WAL commit position together. It reads archived events on one side of that boundary and uncommitted events on the other, so moving a batch into Parquet does not create a gap or duplicate results.</p>
<p>File time bounds exclude irrelevant archives. Within each file, field metadata resolves a filter to the relevant typed columns or spill maps, and row-group statistics skip ranges that cannot match. The WAL tail uses the same field interpretation, so a query's meaning does not change when an event is archived.</p>
<p>Results use the global event order, newest first by default or oldest first on request. The engine keeps a bounded set of matching rows rather than retaining every match in memory. Counts and histograms describe the full selected range; returned rows and field statistics have explicit limits.</p>

<table>
  <thead><tr><th>Result or setting</th><th>Behaviour</th></tr></thead>
  <tbody>
    <tr><td>Time range</td><td>Start-inclusive, end-exclusive. Defaults to the last 12 hours.</td></tr>
    <tr><td>Rows</td><td>Up to 5,000 matching events, with source metadata, raw payload, and parsed fields. Narrow the time range or filters to inspect more specific results.</td></tr>
    <tr><td>Match count</td><td>The number of matches across the full query range, not just the returned rows.</td></tr>
    <tr><td>Histogram</td><td>Matches by time and level, with at most 300 time buckets.</td></tr>
    <tr><td>Field statistics</td><td>Presence and common values sampled from the newest 5,000 matching events, capped at 200 field names and 10 top values per field.</td></tr>
  </tbody>
</table>

<h3>Field and message filters</h3>
<p>Filters combine with AND. Bare words and quoted phrases search the message. Named filters use dot-joined field paths <em>without</em> storage suffixes: write <code class="inl">obj.a:7</code>, not <code class="inl">obj.a__i:7</code>. The engine resolves the field's type variants.</p>
<CodeBlock code={queryExamples} language="text" title="Query examples" />
<table>
  <thead><tr><th>Syntax</th><th>Meaning</th></tr></thead>
  <tbody>
    <tr><td><code class="inl">timeout</code></td><td>Message contains <code class="inl">timeout</code>.</td></tr>
    <tr><td><code class="inl">-"health check"</code></td><td>Message does not contain the phrase.</td></tr>
    <tr><td><code class="inl">status:503</code></td><td>Field equality. Numeric variants compare numerically; string variants compare as text.</td></tr>
    <tr><td><code class="inl">status:"503"</code></td><td>Force text comparison.</td></tr>
    <tr><td><code class="inl">err:*</code> / <code class="inl">-err:*</code></td><td>Require a field to exist or be absent.</td></tr>
    <tr><td><code class="inl">duration_ms&gt;=100</code></td><td>Numeric range comparison. Numeric-looking strings do not satisfy numeric ranges.</td></tr>
    <tr><td><code class="inl">version:12</code></td><td>Restrict events to a deployment configuration version.</td></tr>
  </tbody>
</table>
<p>Text comparisons ignore case. Array fields are matched by their elements. Metadata filters such as <code class="inl">node</code>, <code class="inl">instance</code>, <code class="inl">run</code>, and <code class="inl">stream</code> refer to event source information, not similarly named application fields.</p>

<h3>Using the Logs page</h3>
<p>The query box holds the filter expression. The scope picker narrows it to a version, instance, and run; the histogram lets you zoom into a time range or select levels. The field sidebar shows the sampled fields and their common values, and selecting a value adds a filter.</p>
<p>Use <strong>View in context</strong> to see surrounding unfiltered events. Searches are snapshots: refresh the query to include new output. Recent events are read from the WAL and do not need to wait for an archive batch.</p>
</section>

<section class="page" id="system">
<h2>System logs</h2>
<p>The OpenDeploy agent's own logs use the same pipeline. Each node writes them under deployment ID <code class="inl">0</code>, shown as <code class="inl">opendeploy</code> in the Logs page. They use the same WAL format, collector, archive, retention, and query engine as workload logs.</p>
<p>System events are structured JSON with a level, message, and relevant component and identity fields. They provide the node-side context for deployment preparation, restarts, health checks, and networking. Access to a node's system log is checked against permissions on that node; access to workload logs is checked against the deployment's space.</p>
<p>Build and preparation output, such as a Nix build or image pull, is separate. It is a per-version transcript on the node that prepared the version, streamed on the deployment page rather than treated as a container run's event history.</p>
</section>

<section class="page" id="capture">
<h2>Log consumer</h2>
<p>Every log consumer writes a stream of <code class="inl">LogEvent</code> records to an append-only WAL file. The file is shared per deployment: if a node has multiple instances of the same deployment, they write to the same files. Files rotate in 30-minute UTC time buckets.</p>
<p>With multiple writers, ordering within a WAL file is not strictly guaranteed. The UTC bucket boundaries are guaranteed: consumers always write their events to the file for the event's timestamp.</p>

<h3>WAL framing and recovery</h3>
<p>The WAL format is binary, with lengths and a checksum framing each record. Readers validate records and can recover after a partially written record in the middle of a file by locating the next valid frame.</p>

<h3>Edge cases</h3>
<ul>
  <li><strong>Long lines.</strong> Lines longer than 64 KiB are split into separate events, preserving UTF-8 boundaries where possible. Split JSON may no longer parse.</li>
  <li><strong>Final lines.</strong> A final line without a newline is emitted when the stream closes.</li>
  <li><strong>Invalid text.</strong> Non-UTF-8 bytes are preserved in the raw payload.</li>
  <li><strong>Dropped events.</strong> Failed appends are counted and dropped. The consumer continues reading and attempts to write a marker when writes recover.</li>
</ul>
<CodeBlock code={droppedMarker} language="text" title="Dropped log marker" />
<p>Drop markers are best-effort. WAL framing cannot recover lost bytes, and successful appends do not guarantee durability through power loss.</p>
</section>
