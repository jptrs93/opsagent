# Asset content deletion and bucket usage

Deleting an asset leaves its object in the S3 bucket. This note records a
design that deletes the object, driven by the delete events in the write log,
and makes bucket usage a sum over state the primary already holds.

This note extends [../engineering/assets.md](../engineering/assets.md).

## Status

Not implemented and not scheduled. Recorded from a design discussion on
2026-11-24. The chosen direction is the bucket pass described under
[Design](#design). A heavier alternative, content as a logged entity, is
recorded under [Alternative considered](#alternative-considered). The open
questions at the end are undecided.

## Current state

### What a delete removes

| Thing | On asset delete |
| --- | --- |
| Asset identity, versions, key | Removed by the delete event. The write log keeps the history. |
| Content row in `asset_store` | Removed by the reconciler's sweep once no version names it. |
| Local file in the large-asset root | Removed with the content row. |
| Object in the bucket | Kept. No code path deletes a bucket object. |

Expiry of kept objects is left to the bucket's lifecycle policy. An age-based
lifecycle rule cannot tell the object of a deleted asset from the object of a
live one, so it is not a safe way to reclaim them.

The reconciler also keeps the object when the storage target changes back to
local: it clears `remote_status` and leaves the object in the bucket.

### The sweep's grace period

The sweep reclaims a content row when no version names it and the row was
created more than 24 hours ago. The 24 hours is measured from the row's
creation, not from the delete. It protects an upload in progress. Content
uploaded a month ago and deleted now is reclaimed on the next pass, within the
hour.

### `asset_store` is not in the write log

Asset events are in the write log, and each one carries the version's
`sha256`, `size_bytes`, and `storage_key`. `assets`, `asset_versions`, and
`asset_keys` are materialised from the log.

`asset_store` is not. It is a table in the primary database with one row per
content blob and two flags: `local_status` and `remote_status`. The two flags
differ in kind:

- `local_status` is a fact about one primary's disk. Every reconcile pass
  verifies it against the large-asset root.
- `remote_status` is a fact about the shared bucket. Nothing verifies it. No
  code probes the bucket to rebuild it.

The table travels with the database backup, so a restored or moved primary
keeps it. A copy of the write log alone names every storage key, hash, and
size, but not which keys the bucket holds.

## Design

The design adds no entity and no event. The write log already names every
storage key ever written, and its delete events say when a key stopped being
needed. The primary derives the work from the log and keeps its progress in a
local table. The table is a cache: losing it costs repeated work and nothing
else.

### The bucket pass

The existing sweep is unchanged. It removes the content row and the local file
of unreferenced content.

A bucket pass runs in the reconciler after the sweep. It walks the asset
delete events in log order, starting after the last event it handled:

1. It stops at the first delete event younger than the grace period.
2. For each storage key the deleted asset named:
   - If a live version names the key, the pass skips it. Another asset shares
     the content, and the delete of that asset covers the key later.
   - If the key has a content row that no live version names, the pass stops.
     The sweep has not removed the row yet, and the next pass tries again.
   - Otherwise the pass deletes the bucket object.
3. When every key of the event is handled, the pass records the event's log
   sequence as its progress.

A failed bucket delete stops the pass at that event. The next pass retries it.

### Why a key without a content row is safe to delete

Content is deduplicated by hash, so an upload of identical bytes links to
existing content. It links only through the content row. Once the sweep has
removed the row, nothing references that key again: an identical upload gets a
fresh key. The bucket pass therefore needs no lock across the bucket call and
has no race with an upload.

### Delete without checking

The pass issues the delete without checking that the object exists. Deleting a
missing object succeeds, so a repeated delete is harmless.

### Progress

Progress is one number in a local table: the log sequence of the last delete
event the pass handled. It is not in the write log.

A primary that loses the number starts the pass from the first delete event
and repeats every delete. A moved or rebuilt primary needs no other recovery.

### Grace period after delete

The grace period is measured from the time of the delete event, which the log
holds. The default is 24 hours. The local file is still removed by the sweep
within the hour; the grace period applies to the bucket object.

The grace period covers a restored database that is slightly behind and still
references content deleted just before the failure.

### Storage target changes back to local

The reconciler deletes the bucket object when it moves content back to local
storage: it downloads the content when the local file is missing, deletes the
object, and then clears `remote_status`. A crash before the flag is cleared
leaves a claim with no object, and the next pass repeats the delete and clears
the flag.

### Objects the log never named

The bucket pass reaches only keys the log names. Two kinds of object are
outside it:

- An upload that crashed after the bucket write and before its commit.
- Objects of versions deleted before storage keys were recorded in the log
  (v0.0.614). This is inferred from the migration description and not checked
  against a real database.

A listing pass over the asset prefix removes objects whose key the log never
named and that are older than the grace period. The large-asset root already
has the same pair: a row sweep and a disk scan. The listing pass is separate
work and the bucket pass does not depend on it.

### Rebuilding `remote_status`

`remote_status` stays outside the log. A primary that must rebuild it probes
the bucket for each key a live version names, the way a reconcile pass already
verifies `local_status` against the disk.

## Bucket usage

With deletion in place, usage is a sum over state the primary holds and needs
no bucket listing:

- Assets: `size_bytes` over `asset_store` rows with `remote_status` set.
- Log and metrics files: `size_bytes` over the planned chunk records with
  `remote` set.

The sum is what the cluster believes it uses. It omits deleted content inside
its grace period, whose row is gone and whose object still exists. The listing
pass reports what the bucket holds. The two also differ through orphans,
incomplete multipart uploads, and noncurrent versions on a versioned bucket.
The database backup uses a separate path and is outside the sum.

## Restore trade-off

Restoring the latest point stays supported: it references only content that
live versions name, plus content inside its grace period.

Restoring an older point is already unsupported, because the local file of
deleted content is swept. Kept bucket objects make that case work only when
the content was in the bucket and the lifecycle rule has not expired it. The
design gives up that partial cover.

An operator who wants a longer window lengthens the grace period or enables
bucket versioning. With versioning, the delete leaves a noncurrent version
that the operator's own lifecycle rule expires.

## Alternative considered

Content as a logged entity: an `AssetContent` entity keyed by storage key,
with `sha256`, `size_bytes`, and a `remote` flag, created with the first
version that names it and deleted by an event once the bucket object is
confirmed gone. It matches the planned records for sealed log and metrics
files, and it lets the write log alone say which keys the bucket holds.

It is not chosen. It needs a new entity type, a conversion of every existing
`asset_store` row, and an event for each upload, storage-target move, and
confirmed deletion. The bucket pass reaches the same result from events the
log already has. The alternative is worth revisiting if the write log replaces
the database tables as the only state.

## Open questions

- Whether the primary deletes log and metrics objects at retention through the
  same pass.
- How the bucket pass behaves across a storage-target change or an S3
  configuration change while deleted content is still inside its grace period.
  Its objects are in the old location.
- Whether and when the listing pass is built.
- How content shared across spaces is attributed in a per-space usage figure.
  A log or metrics file belongs to one deployment, so only assets have this
  problem.
- Where the usage figure is shown.
