# logmanager implementation plan

## Scope

The build-out of the node-local log storage backend. The shipped system
(WAL, collector, two-pass commit, catalog, maintenance loop, query engine)
is described in [log-storage.md](../engineering/log-storage.md) and the
design rationale in [log-compaction.md](log-compaction.md). This document
keeps the settled decisions, records what landed, and lists what is still
open.

## Status

All eight steps of the plan are implemented in `backend/lib/log/logmanager`.

| Step | Landed as |
| --- | --- |
| 1 Shared typed shredder | `shred.go`, `lineview.go`: typed values, dotted keys, the number rule, caps; property test across both parse paths |
| 2 Level 1 schema and writer | `schema.go`, `parquetio.go`: dynamic `parquet.Group`, dense `f_<key>__<t>` leaves, four spill maps, generic row writer and reader |
| 3 Two-pass commit and catalog | `archivewrite.go`, `logcollector.go`: tally then write, `log_file_keys`, 64MB commits, maintenance nudge |
| 4 Column-resolved filters | `twopass.go`, `semantics.go`: per-file binding from the catalog, dense and spill readers, absent-key short-circuit |
| 5 Reconciliation and retention | `maintenance.go`: rowless file sweep, fileless row sweep, 30-day archive and WAL retention |
| 6 Backfill | Ran once as a newest-first level 0 rewrite with the 4-step swap and grace unlink; completed on every node on 2026-09-11 and the code was removed |
| 7 Typed ops and API | `gt`/`gte`/`lt`/`lte`, `LogFilter.text`, query-bar grammar, row-group pruning from the column index |
| 8 Roll-up | `maintenance.go`: byte-triggered level 2 merge with the day-end sweep |

Deviations from the plan as written:

- Keys beyond the 4,096 tally cap spill, and the write pass counts them
  exactly for `log_file_keys`, so the catalog is complete even for capped
  batches.
- The catalog read is `ListLogFileKeysInRange` over the query's time range
  and field names rather than a per-file-id list; it runs in the same read
  transaction as the file list.
- Level 0 no longer exists anywhere in the code. The migration rewrote
  every level 0 file, and the raw-scan branch the planner used for them
  was removed with the backfill, so every file is planned from the catalog
  alone. `forceFullScan` remains as the test oracle.

## Settled decisions

- **Column type is the JSON value type, never coerced.** `2` is int64;
  `2.0` and `1e3` are float64; integer text outside int64 goes to the float
  column. A key with mixed types gets one column per type
  (`f_<key>__i`, `__f`, `__b`, `__s`), and all of a key's variants are dense
  or all spill.
- **Budget: 400 leaf key columns per file.** Under budget every key is a
  column. Over budget, keys rank by row presence from the tally pass; a key
  is admitted only if all its variants fit. The tally tracks at most 4,096
  distinct keys per batch; later keys are uncounted and spill.
- **Nested objects flatten to dotted keys**, depth capped at 8. Arrays stay
  JSON text in the string variant and match per element. `null` is absent.
  Duplicate keys: last wins. Key length capped at 256 bytes.
- **Spill is four typed maps** (`spill_int`, `spill_float`, `spill_bool`,
  `spill_str`).
- **Query resolution is typed and cross-variant.** A literal that parses as
  a number matches the int and float variants numerically and the string
  variant textually; range ops touch numeric variants only; `contains` runs
  over the textual rendering of any variant. The WAL tail applies the same
  rules through the shared shredder.
- **Batch commits trigger on 64MB of raw WAL bytes**, or the day deadline,
  or producer exit, whichever comes first. Size is the axis that bounds the
  tail: filtered queries scan the uncommitted WAL raw, so the threshold caps
  that scan at 64MB regardless of how quiet or busy a deployment is. The day
  deadline keeps files day-scoped; the one-minute tick keeps silent
  deployments committing. No further time trigger until S3 upload makes
  off-node latency matter.
- **Roll-up triggers on accumulated parquet bytes within the day.** When a
  day's level 1 files sum to at least 256MB of parquet they merge into one
  level 2 file; at day end plus 15 minutes the remainder merges if there are
  at least two files. Each byte is written exactly twice. Merging on every
  commit was rejected: it rewrites the growing file about twenty times
  before it is full.
- **`level` is a processing ladder.** Each level implies everything below
  it, and every rewrite re-runs shredding from `raw_message`.

| Level | Meaning | Written by |
| --- | --- | --- |
| 0 | Batch output, fixed schema, no key columns | Commits before shredding shipped |
| 1 | Batch output, shredded | Every commit once shredding ships |
| 2 | Node day roll-up, shredded over the merged batch | Roll-up pass |
| 3 | Cross-node merge | Deferred |

A later change to shredder semantics is not expressed by the ladder; it
gets its own version column when it happens.

## Open items

- **Row group size.** The footer tax scales with row groups times columns,
  not with file count: a 64MB raw batch is about five 128k-row groups, so
  roughly 500KB of footer at the full budget. Larger row groups would
  shrink it at the cost of coarser time pruning. Keep 128k unless measured
  footers say otherwise.
- **Roll-up tiers.** A third tier merging level 2 files into larger units
  is the same routine with level 2 inputs and waits for S3 upload.
- **Field sidebar from the catalog.** `log_file_keys.row_count` summed over
  the files in range gives coverage without sampling.
- **Display map from columns** instead of `raw_message`, once the sidebar
  and filters no longer need the parse.
- **Record identity.** The API is one-shot with no cursor. If pagination or
  tail-follow ever need one, `(time, node, instance_ordinal, run, stream,
  seq)` is already unique per record and sorts the archive, so a cursor is
  that tuple; nothing else is required.
- **S3 upload.** Identical keys under a bucket prefix; the random `seq`
  gives idempotent re-upload. Level 3 cross-node merge stays deferred.
