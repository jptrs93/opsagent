# Chunk records and the archive

A chunk is one sealed log or metrics file on a node. The primary keeps a record of every chunk as cluster state. When the archive is on, each node sends its chunks to the backup bucket and the record says the bucket holds a copy.

The records exist whether or not the archive is on. The archive adds the upload.

Nothing reads a chunk back from the bucket, and nothing deletes a local file or a bucket object because of a record. See [Not implemented](#not-implemented).

## Chunk records

### Types

| Entity type | Number | One record per |
| --- | --- | --- |
| `LogChunk` | 24 | sealed log archive file of a deployment on a node |
| `DeploymentMetricsChunk` | 25 | sealed run metrics file of a deployment on a node for one day |
| `NodeMetricsChunk` | 26 | sealed host metrics file of a node for one day |

The shapes are in `api-contract/model/chunks.proto`. Every record carries the node, the UTC day (days since the Unix epoch), the file's number on the node, the time range, the row count, the size in bytes, the SHA-256 of the file, and a location.

`deployment_id` is a plain number, not a reference. A chunk outlives its deployment.

### Identity

A record is identified by its type, node, deployment, day and file number. The file number is the random `seq` of a log file, or the compaction number of a metrics file. A node never writes two files with the same identity and different content after the file is sealed.

### Location

`ChunkLocation` says where the bytes are:

- `local`: the node that wrote the file still holds it.
- `archive`: absent, or `s3`. `S3Archive` has no fields. The object key is derived from the record (see [Bucket layout](#bucket-layout)).

A chunk that is neither local nor archived has no record.

### Storage

The records are entities in the write log, reduced into `log_chunks`, `deployment_metrics_chunks` and `node_metrics_chunks` (`storage/primarydb/pq/chunks.go`, `sql/schema_chunks.sql`). `pq.ChunkRow` is the one Go shape for the three tables. `archived_time` is set by the reducer when a record first gains an archive.

Chunk records are never sent to a browser. `Snapshot` leaves them out, `SnapshotAll` includes them, and the event stream has no visibility rule that admits them.

### Lifetime

| Event | Effect on the record |
| --- | --- |
| A node lists a sealed file the primary has no record of | created, `local` |
| The bucket holds the file | `archive.s3` set |
| The node no longer lists the file, and an archive holds it | `local` cleared |
| The node no longer lists the file, and no archive holds it | deleted |
| The node lists the file again | `local` set |
| The node is evicted | as if the node listed nothing (`pq.NodeChunkReleases`, in the evict commit) |

## Sealed files

A node lists only files that no later step rewrites.

- Logs: files whose catalog row has a hash. See [log-storage.md](log-storage.md), Sealing.
- Run metrics: per-deployment files named in a compaction manifest. See [metrics.md](metrics.md), Compaction.
- Node metrics: per-day files named in a compaction manifest. Files sealed before manifests existed get one from the maintenance loop.

Two kinds of file are never listed: the system log (deployment 0), and run metrics files written before the per-deployment split (one file per node and day, holding every deployment).

## Node archiver

`lib/chunkarchive.Archiver` runs on every node, the primary included. A secondary reaches the primary's records over the cluster API. The primary's own node calls the same functions in process (`app/primary/chunkarchive.go`).

### Pass

The first pass starts 2 minutes after the agent starts. A pass then runs every hour.

1. List the sealed files on the node.
2. Sync the listing with the primary, when the listing changed since the last sync or 24 hours have passed.
3. Upload until the primary has no more work.

### Sync

The listing is split into requests of about 2,000 chunks. The requests cover disjoint day ranges that together cover every day, so a record the node no longer lists falls in exactly one request.

A chunk type whose listing failed on the node is left out of the request. The primary leaves the records of an absent type alone.

### Upload

The node asks for upload work in batches of 16. For each item it checks that it holds the file and that the file's size and hash match the record, then sends the file with one HTTP `PUT` to the URL the primary signed. It reports the files it sent, and the primary answers with how many it found in the bucket.

The answer to a work request also says how many records the primary found in the bucket already. The node asks again while the answer has uploads or that count is above zero, so a batch that needed no upload does not end the pass.

A file that fails to send, or that the primary cannot confirm, goes on an exclude list for the rest of the pass. The node sends the list with the next request, so one stuck record does not block the files behind it. The pass ends when the list reaches 512.

A presigned URL is a credential. The archiver never logs it: a transport error is unwrapped before it is logged.

## Primary

`app/primary/domain/chunks.Service` holds the three operations. Each one identifies the calling node from its cluster certificate and touches only that node's records.

| Cluster endpoint | Operation |
| --- | --- |
| `POST /v1/cluster/chunks/sync` | `Sync` |
| `POST /v1/cluster/chunks/upload-work` | `UploadWork` |
| `POST /v1/cluster/chunks/uploaded` | `Uploaded` |

### Sync

One commit makes the node's records within the request's days match the listing.

| Listed file | Record | Result |
| --- | --- | --- |
| present | none | record created |
| present | same content, `local` | nothing |
| present | same content, not `local` | `local` set |
| present | other content, not archived | record takes the listed content |
| present | other content, archived | record loses `local`; counted as a conflict |
| absent | `local`, archived | `local` cleared |
| absent | `local`, not archived | record deleted |

A listing with nothing to change commits nothing.

### UploadWork

`UploadWork` answers with the node's oldest records that are local and not archived, across the three types, oldest day first. The default is 16 and the maximum is 64.

For each candidate the primary first asks the bucket for the object. A record whose object is already there is marked archived and is not offered. The answer counts those records in `archived`. Every other candidate gets a presigned URL.

Each chunk table has a partial index over the records that are local and not archived (`idx_<table>_to_archive`), which is what this query reads. A node with nothing to send costs one index lookup per table.

### Uploaded

`Uploaded` asks the bucket for each object the node names and marks the records it finds as archived. It ignores a reference to another node's record.

### When the bucket is used

`UploadWork` and `Uploaded` do nothing unless both `Backup.Enabled` and `Archive.Enabled` are true. `Sync` runs regardless.

## Bucket layout

The archive uses the backup bucket and its credentials, under its own prefix (`Archive.S3Path`, default `opendeploy/archive`). A key mirrors the path of the file on the node (`chunks.ObjectKey`):

| Type | Key under the prefix |
| --- | --- |
| `LogChunk` | `logs/<deployment>/<YYYYMMDD>/L<level>_<minMs>-<maxMs>_n<node>_<seq>.parquet` |
| `DeploymentMetricsChunk` | `metrics/<YYYYMMDD>/d<deployment>_n<node>_<seq>.parquet` |
| `NodeMetricsChunk` | `metrics/node/<YYYYMMDD>/n<node>_<seq>.parquet` |

### Presigned upload

The node holds no bucket credentials. The primary signs one `PUT` per file, valid for 6 hours, with the size and the SHA-256 as signed headers (`content-length;host;x-amz-checksum-sha256`). The bucket refuses a body of another size or with another hash.

The SDK's default presigner moves `x-amz-checksum-sha256` into the query string, where some S3 implementations ignore it and accept any body. `newS3Target` builds the presigner with header hoisting off.

### Verification

The primary never takes the node's word that a file is in the bucket. It marks a record archived only after a `HEAD` shows an object of the recorded size. When the bucket reports a SHA-256 for the object, that must match too. A bucket that reports no hash is accepted on the size.

### Network

Each node sends its files directly to the bucket. A node needs network access to the bucket endpoint, under the same URL the primary uses.

## Settings

| Setting | Default | Meaning |
| --- | --- | --- |
| `Archive.Enabled` | `false` | Nodes send sealed log and metrics files to the backup bucket. Counts only while `Backup.Enabled` is true. |
| `Archive.S3Path` | `opendeploy/archive` | Prefix of the archive in the backup bucket. |

Changing the prefix or the bucket does not move objects. Records made before the change still say `archive.s3`, and their keys now resolve under the new prefix.

Turning the archive off stops uploads. Records and objects stay.

## Status

`POST /v1/archive/status` returns, per node and chunk type, the count and bytes of records that are local, archived, and pending (local and not archived), with the time of the newest archive confirmation. It needs view permission on nodes in every space.

The Nodes page shows it on the Replication tab: one row per node, files in the bucket out of files recorded, and what is still to send (`frontend/src/lib/archiveStatus.js`).

## Failure handling

| Failure | Result |
| --- | --- |
| The primary is unreachable, or does not answer a call within 5 minutes | The pass fails with a warning. The next pass runs in an hour. |
| An upload takes longer than an hour | It is cancelled and the file goes on the exclude list. |
| The bucket refuses an upload | The file goes on the exclude list. The next pass tries again. |
| The file on the node differs from its record | The node does not send it. The next sync replaces or releases the record. |
| The primary cannot find an object after an upload | The record stays unarchived. The next pass offers it again. |
| An upload finished but the report was lost | The next `UploadWork` finds the object and marks the record. |
| The primary was restored from a backup older than some uploads | The records come back unarchived. `UploadWork` finds each object and marks the record, with no upload. |
| The bucket is not configured | `UploadWork` returns an error and the pass fails with a warning. |

## Tests

- `storage/primarydb/state/chunks_test.go` and the update oracle: the records through the write log, and a rebuild from the log.
- `app/primary/domain/chunks`: sync, upload work, uploaded and status against a fake bucket; the presigned URL shape; `HEAD` handling.
- `lib/chunkarchive`: passes against a fake registry and a bucket that enforces the signed hash and size.
- `TestRealBucketHoldsAnUploadToItsHashAndSize` runs the upload contract against a real bucket. It is skipped unless `ARCHIVE_S3_TEST_BUCKET`, `ARCHIVE_S3_TEST_ACCESS_KEY_ID` and `ARCHIVE_S3_TEST_SECRET_ACCESS_KEY` are set (`ARCHIVE_S3_TEST_ENDPOINT` and `ARCHIVE_S3_TEST_REGION` for a bucket outside AWS).
- VM e2e: `testing-vms/e2e/cases/archive.js` and the orchestrator's archive checks. See `testing-vms/e2e/README.md`.

## Not implemented

- Reading logs or metrics from the bucket.
- Deleting a local file once the bucket holds it, or a shorter local retention for archived files.
- Deleting bucket objects. An object stays after its record is gone.
- Multipart upload. A file is sent in one `PUT`.
- Chunks for the system log and for run metrics files written before the per-deployment split.
