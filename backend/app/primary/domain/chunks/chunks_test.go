package chunks

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

const (
	logKind  = apigen.CoreEntityType_CORE_ENTITY_LOG_CHUNK
	depKind  = apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT_METRICS_CHUNK
	nodeKind = apigen.CoreEntityType_CORE_ENTITY_NODE_METRICS_CHUNK
)

type literals struct{}

func (literals) MustLoadBoolSetting(v apigen.BoolSetting) bool {
	return v.Value.Value.Literal != nil && *v.Value.Value.Literal
}

func (literals) MustLoadStringSetting(v apigen.StringSetting) string {
	if v.Value.Value.Literal == nil {
		return ""
	}
	return *v.Value.Value.Literal
}

type fakeBucket struct {
	objects  map[string]Object
	headErr  error
	heads    int
	presigns []string
}

func (b *fakeBucket) Head(_ context.Context, key string) (Object, bool, error) {
	b.heads++
	if b.headErr != nil {
		return Object{}, false, b.headErr
	}
	obj, ok := b.objects[key]
	return obj, ok, nil
}

func (b *fakeBucket) PresignPut(_ context.Context, key string, sha256 []byte, size int64) (string, map[string]string, error) {
	b.presigns = append(b.presigns, key)
	return "https://bucket.test/" + key, map[string]string{"x-amz-checksum-sha256": fmt.Sprintf("%x", sha256), "Content-Length": fmt.Sprint(size)}, nil
}

type env struct {
	*Service
	bucket   *fakeBucket
	settings *apigen.ClusterSettings
}

func newEnv(t *testing.T, archive bool) *env {
	t.Helper()
	db := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = db.Close() })
	e := &env{bucket: &fakeBucket{objects: map[string]Object{}}, settings: &apigen.ClusterSettings{}}
	e.settings.Backup.Enabled = systemconfig.BoolLiteral(true)
	e.settings.Archive.Enabled = systemconfig.BoolLiteral(archive)
	e.Service = &Service{DB: db, Config: func() *apigen.ClusterSettings { return e.settings }, Loader: literals{},
		OpenTarget: func(*apigen.ClusterSettings) (Target, error) { return e.bucket, nil }}
	return e
}

func sum(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func logChunk(dep uint64, day uint32, seq int64, content byte) apigen.LogChunk {
	return apigen.LogChunk{DeploymentID: dep, Day: day, Level: 2, Seq: seq, MinTime: int64(day) * 86_400e9, MaxTime: int64(day)*86_400e9 + 5e9,
		RowCount: 10, SizeBytes: 1000 + uint64(content), Sha256: sum(content)}
}

func depChunk(dep uint64, day uint32, seq uint32, content byte) apigen.DeploymentMetricsChunk {
	return apigen.DeploymentMetricsChunk{DeploymentID: dep, Day: day, Seq: seq, MinTime: 1, MaxTime: 2, RowCount: 10, SizeBytes: 2000 + uint64(content), Sha256: sum(content)}
}

func nodeChunk(day uint32, seq uint32, content byte) apigen.NodeMetricsChunk {
	return apigen.NodeMetricsChunk{Day: day, Seq: seq, MinTime: 1, MaxTime: 2, RowCount: 10, SizeBytes: 3000 + uint64(content), Sha256: sum(content)}
}

type sets struct {
	logs []apigen.LogChunk
	deps []apigen.DeploymentMetricsChunk
	node []apigen.NodeMetricsChunk
}

func everything(s sets) *apigen.ClusterChunksSyncRequest {
	return &apigen.ClusterChunksSyncRequest{FromDay: 0, ToDay: math.MaxUint32,
		Logs:              apigen.Some(apigen.LogChunkSet{Chunks: s.logs}),
		DeploymentMetrics: apigen.Some(apigen.DeploymentMetricsChunkSet{Chunks: s.deps}),
		NodeMetrics:       apigen.Some(apigen.NodeMetricsChunkSet{Chunks: s.node})}
}

func (e *env) sync(t *testing.T, node uint64, req *apigen.ClusterChunksSyncRequest) *apigen.ClusterChunksSyncResponse {
	t.Helper()
	resp, err := e.Sync(context.Background(), node, req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func (e *env) rows(t *testing.T, kind apigen.CoreEntityType) []pq.ChunkRow {
	t.Helper()
	return erru.Must(e.DB.Queries().ListChunks(context.Background(), kind))
}

func (e *env) seq(t *testing.T) int64 {
	t.Helper()
	return erru.Must(e.DB.Queries().GetGlobalSeq(context.Background()))
}

func (e *env) archive(t *testing.T, r pq.ChunkRow) {
	t.Helper()
	e.bucket.objects[ObjectKey(&r)] = Object{Size: int64(r.SizeBytes), Sha256: r.Sha256}
	resp, err := e.Uploaded(context.Background(), r.NodeID, &apigen.ClusterChunksUploadedRequest{Uploaded: []apigen.ChunkRef{{EntityType: r.Kind, EntityID: r.ID}}})
	if err != nil || resp.Archived != 1 {
		t.Fatalf("archiving %v %d: archived=%d err=%v", r.Kind, r.ID, resp.Archived, err)
	}
}

func TestSyncRecordsEveryListedFileOnce(t *testing.T) {
	e := newEnv(t, false)
	listing := sets{
		logs: []apigen.LogChunk{logChunk(7, 20_000, 111, 1), logChunk(8, 20_001, 222, 2)},
		deps: []apigen.DeploymentMetricsChunk{depChunk(7, 20_000, 1, 3)},
		node: []apigen.NodeMetricsChunk{nodeChunk(20_000, 1, 4)},
	}
	if resp := e.sync(t, 5, everything(listing)); resp.Created != 4 || resp.Released != 0 || resp.Conflicts != 0 {
		t.Fatalf("first sync: %+v", resp)
	}
	logs := e.rows(t, logKind)
	if len(logs) != 2 || logs[0].NodeID != 5 || !logs[0].Local || logs[0].Archive != pq.ChunkArchiveNone || logs[0].FileSeq != 111 || logs[0].DeploymentID != 7 || logs[0].Level != 2 {
		t.Fatalf("log records: %+v", logs)
	}
	if len(e.rows(t, depKind)) != 1 || len(e.rows(t, nodeKind)) != 1 {
		t.Fatal("the metrics chunks were not recorded")
	}
	before := e.seq(t)
	if resp := e.sync(t, 5, everything(listing)); resp.Created != 0 || resp.Released != 0 {
		t.Fatalf("second sync of the same listing: %+v", resp)
	}
	if e.seq(t) != before {
		t.Fatal("a sync that changed nothing wrote to the log")
	}
	if resp := e.sync(t, 6, everything(listing)); resp.Created != 4 {
		t.Fatalf("another node listing files with the same numbers: %+v", resp)
	}
	if len(e.rows(t, logKind)) != 4 {
		t.Fatal("two nodes' files with the same numbers share a record")
	}
}

func TestSyncReleasesWhatTheNodeNoLongerLists(t *testing.T) {
	e := newEnv(t, true)
	e.sync(t, 5, everything(sets{logs: []apigen.LogChunk{logChunk(7, 20_000, 1, 1), logChunk(7, 20_000, 2, 2), logChunk(7, 20_005, 3, 3), logChunk(7, 20_009, 4, 4)}}))
	e.sync(t, 6, everything(sets{logs: []apigen.LogChunk{logChunk(7, 20_000, 1, 1)}}))
	rows := e.rows(t, logKind)
	e.archive(t, rows[0])

	resp := e.sync(t, 5, &apigen.ClusterChunksSyncRequest{FromDay: 20_000, ToDay: 20_005, Logs: apigen.Some(apigen.LogChunkSet{})})
	if resp.Released != 3 || resp.Created != 0 {
		t.Fatalf("sync of an empty listing for days 20000..20005: %+v", resp)
	}
	q := e.DB.Queries()
	ctx := context.Background()
	kept := erru.Must(q.GetChunk(ctx, logKind, rows[0].ID))
	if kept.Local || kept.Archive != pq.ChunkArchiveS3 {
		t.Fatalf("an archived chunk the node dropped keeps its record without local, got %+v", kept)
	}
	for _, r := range rows[1:3] {
		if _, err := q.GetChunk(ctx, logKind, r.ID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("a chunk held nowhere still has record %d (err %v)", r.ID, err)
		}
	}
	if out := erru.Must(q.GetChunk(ctx, logKind, rows[3].ID)); !out.Local {
		t.Fatal("a record outside the synced days was released")
	}
	if other := erru.Must(q.GetChunk(ctx, logKind, rows[4].ID)); !other.Local || other.NodeID != 6 {
		t.Fatal("another node's record was released")
	}

	if resp := e.sync(t, 5, &apigen.ClusterChunksSyncRequest{FromDay: 0, ToDay: math.MaxUint32}); resp.Released != 0 {
		t.Fatalf("a sync that covers no chunk type released %d records", resp.Released)
	}
	if resp := e.sync(t, 5, everything(sets{logs: []apigen.LogChunk{logChunk(7, 20_000, 1, 1), logChunk(7, 20_009, 4, 4)}})); resp.Created != 0 || resp.Released != 0 {
		t.Fatalf("listing the archived file again: %+v", resp)
	}
	if back := erru.Must(q.GetChunk(ctx, logKind, rows[0].ID)); !back.Local || back.Archive != pq.ChunkArchiveS3 {
		t.Fatalf("a file the node holds again is local again, got %+v", back)
	}
}

func TestSyncTakesNewContentUnlessTheArchiveHoldsTheOld(t *testing.T) {
	e := newEnv(t, true)
	e.sync(t, 5, everything(sets{deps: []apigen.DeploymentMetricsChunk{depChunk(7, 20_000, 1, 1), depChunk(8, 20_000, 1, 2)}}))
	rows := e.rows(t, depKind)
	e.archive(t, rows[1])

	resp := e.sync(t, 5, everything(sets{deps: []apigen.DeploymentMetricsChunk{depChunk(7, 20_000, 1, 9), depChunk(8, 20_000, 1, 9)}}))
	if resp.Created != 1 || resp.Conflicts != 1 || resp.Released != 1 {
		t.Fatalf("sync of two files with new content: %+v", resp)
	}
	after := e.rows(t, depKind)
	if len(after) != 2 || after[0].ID != rows[0].ID || !bytes.Equal(after[0].Sha256, sum(9)) || !after[0].Local {
		t.Fatalf("the unarchived record takes the node's content, got %+v", after[0])
	}
	if !bytes.Equal(after[1].Sha256, sum(2)) || after[1].Local || after[1].Archive != pq.ChunkArchiveS3 {
		t.Fatalf("the archived record keeps the archived content and loses local, got %+v", after[1])
	}
}

func TestSyncRefusesAListingThatIsNotWellFormed(t *testing.T) {
	e := newEnv(t, false)
	bad := []*apigen.ClusterChunksSyncRequest{
		{FromDay: 5, ToDay: 4},
		{FromDay: 0, ToDay: 10, Logs: apigen.Some(apigen.LogChunkSet{Chunks: []apigen.LogChunk{logChunk(7, 11, 1, 1)}})},
		{FromDay: 0, ToDay: 10, Logs: apigen.Some(apigen.LogChunkSet{Chunks: []apigen.LogChunk{logChunk(7, 5, 1, 1), logChunk(7, 5, 1, 2)}})},
		{FromDay: 0, ToDay: 10, NodeMetrics: apigen.Some(apigen.NodeMetricsChunkSet{Chunks: []apigen.NodeMetricsChunk{{Day: 5, Seq: 1, Sha256: []byte{1}}}})},
	}
	for i, req := range bad {
		if _, err := e.Sync(context.Background(), 5, req); !errors.Is(err, InvalidSyncErr) {
			t.Fatalf("listing %d: got %v, want the invalid listing error", i, err)
		}
	}
	if e.seq(t) != 0 {
		t.Fatal("a refused listing wrote to the log")
	}
}

func TestUploadWorkOffersTheOldestUnarchivedChunksWhileTheArchiveIsOn(t *testing.T) {
	e := newEnv(t, false)
	ctx := context.Background()
	e.sync(t, 5, everything(sets{
		logs: []apigen.LogChunk{logChunk(7, 20_003, 1, 1), logChunk(7, 20_001, 2, 2)},
		deps: []apigen.DeploymentMetricsChunk{depChunk(7, 20_002, 1, 3)},
		node: []apigen.NodeMetricsChunk{nodeChunk(20_000, 1, 4)},
	}))
	e.sync(t, 6, everything(sets{node: []apigen.NodeMetricsChunk{nodeChunk(19_000, 1, 5)}}))

	off := erru.Must(e.UploadWork(ctx, 5, &apigen.ClusterChunksUploadWorkRequest{}))
	if len(off.Uploads) != 0 || e.bucket.heads != 0 {
		t.Fatalf("work was offered while the archive is off: %+v", off.Uploads)
	}
	e.settings.Archive.Enabled = systemconfig.BoolLiteral(true)
	e.settings.Backup.Enabled = systemconfig.BoolLiteral(false)
	if off := erru.Must(e.UploadWork(ctx, 5, &apigen.ClusterChunksUploadWorkRequest{})); len(off.Uploads) != 0 {
		t.Fatal("work was offered while the backup bucket is off")
	}
	e.settings.Backup.Enabled = systemconfig.BoolLiteral(true)

	work := erru.Must(e.UploadWork(ctx, 5, &apigen.ClusterChunksUploadWorkRequest{Limit: 3}))
	if len(work.Uploads) != 3 {
		t.Fatalf("offered %d uploads, want 3", len(work.Uploads))
	}
	first, second, third := work.Uploads[0], work.Uploads[1], work.Uploads[2]
	if first.Chunk.NodeMetrics == nil || first.Chunk.NodeMetrics.Day != 20_000 || second.Chunk.Log == nil || second.Chunk.Log.Day != 20_001 || third.Chunk.DeploymentMetrics == nil {
		t.Fatalf("uploads are not oldest day first: %+v", work.Uploads)
	}
	if first.Url != "https://bucket.test/metrics/node/20241004/n5_1.parquet" || first.Headers["x-amz-checksum-sha256"] == "" || first.Ref.EntityType != nodeKind {
		t.Fatalf("first upload: %+v", first)
	}
	if second.Chunk.Log.NodeID != 5 || !second.Chunk.Log.Location.Local || second.Chunk.Log.Location.Archive.Present {
		t.Fatalf("an offered chunk carries its record: %+v", second.Chunk.Log)
	}

	rest := erru.Must(e.UploadWork(ctx, 5, &apigen.ClusterChunksUploadWorkRequest{Limit: 3, Exclude: []apigen.ChunkRef{first.Ref, second.Ref}}))
	if len(rest.Uploads) != 2 || rest.Uploads[0].Ref.EntityID != third.Ref.EntityID || rest.Uploads[0].Ref.EntityType != third.Ref.EntityType || rest.Uploads[1].Chunk.Log == nil || rest.Uploads[1].Chunk.Log.Day != 20_003 {
		t.Fatalf("excluding two records left %+v", rest.Uploads)
	}
}

func TestUploadWorkMarksAChunkTheBucketAlreadyHolds(t *testing.T) {
	e := newEnv(t, true)
	ctx := context.Background()
	e.sync(t, 5, everything(sets{logs: []apigen.LogChunk{logChunk(7, 20_000, 1, 1), logChunk(7, 20_000, 2, 2), logChunk(7, 20_000, 3, 3)}}))
	rows := e.rows(t, logKind)
	e.bucket.objects[ObjectKey(&rows[0])] = Object{Size: int64(rows[0].SizeBytes), Sha256: rows[0].Sha256}
	e.bucket.objects[ObjectKey(&rows[1])] = Object{Size: int64(rows[1].SizeBytes), Sha256: sum(99)}
	e.bucket.objects[ObjectKey(&rows[2])] = Object{Size: int64(rows[2].SizeBytes)}

	work := erru.Must(e.UploadWork(ctx, 5, &apigen.ClusterChunksUploadWorkRequest{}))
	if len(work.Uploads) != 1 || work.Uploads[0].Ref.EntityID != rows[1].ID {
		t.Fatalf("only the chunk whose object has other content is offered, got %+v", work.Uploads)
	}
	if work.Archived != 2 {
		t.Fatalf("the answer counts %d chunks found in the bucket, want 2", work.Archived)
	}
	after := e.rows(t, logKind)
	if after[0].Archive != pq.ChunkArchiveS3 || after[0].ArchivedTime == 0 || !after[0].Local {
		t.Fatalf("a chunk the bucket holds is archived, got %+v", after[0])
	}
	if after[1].Archive != pq.ChunkArchiveNone {
		t.Fatal("a chunk whose object has another hash was marked archived")
	}
	if after[2].Archive != pq.ChunkArchiveS3 {
		t.Fatal("a chunk whose object has the right size and no hash was not accepted")
	}

	e.bucket.headErr = errors.New("bucket unreachable")
	if _, err := e.UploadWork(ctx, 5, &apigen.ClusterChunksUploadWorkRequest{}); err == nil {
		t.Fatal("work was offered without checking the bucket")
	}
}

func TestUploadedArchivesOnlyWhatTheBucketHolds(t *testing.T) {
	e := newEnv(t, true)
	ctx := context.Background()
	e.sync(t, 5, everything(sets{node: []apigen.NodeMetricsChunk{nodeChunk(20_000, 1, 1), nodeChunk(20_001, 1, 2), nodeChunk(20_002, 1, 3)}}))
	e.sync(t, 6, everything(sets{node: []apigen.NodeMetricsChunk{nodeChunk(20_000, 1, 4)}}))
	rows := e.rows(t, nodeKind)
	ref := func(r pq.ChunkRow) apigen.ChunkRef { return apigen.ChunkRef{EntityType: r.Kind, EntityID: r.ID} }
	hold := func(r pq.ChunkRow) {
		e.bucket.objects[ObjectKey(&r)] = Object{Size: int64(r.SizeBytes), Sha256: r.Sha256}
	}
	hold(rows[0])
	e.bucket.objects[ObjectKey(&rows[1])] = Object{Size: int64(rows[1].SizeBytes) + 1, Sha256: rows[1].Sha256}
	hold(rows[3])

	resp := erru.Must(e.Uploaded(ctx, 5, &apigen.ClusterChunksUploadedRequest{Uploaded: []apigen.ChunkRef{
		ref(rows[0]), ref(rows[0]), ref(rows[1]), ref(rows[2]), ref(rows[3]), {EntityType: nodeKind, EntityID: 999}, {EntityType: apigen.CoreEntityType_CORE_ENTITY_NODE, EntityID: 1},
	}}))
	if resp.Archived != 1 {
		t.Fatalf("archived %d chunks, want the one the bucket holds for this node", resp.Archived)
	}
	after := e.rows(t, nodeKind)
	for i, want := range []pq.ChunkArchive{pq.ChunkArchiveS3, pq.ChunkArchiveNone, pq.ChunkArchiveNone, pq.ChunkArchiveNone} {
		if after[i].Archive != want {
			t.Fatalf("record %d: archive=%d, want %d", i, after[i].Archive, want)
		}
	}
	before := e.seq(t)
	if again := erru.Must(e.Uploaded(ctx, 5, &apigen.ClusterChunksUploadedRequest{Uploaded: []apigen.ChunkRef{ref(rows[0])}})); again.Archived != 1 {
		t.Fatalf("reporting an archived chunk again counts it as archived, got %d", again.Archived)
	}
	if e.seq(t) != before {
		t.Fatal("reporting an archived chunk again wrote to the log")
	}
	if work := erru.Must(e.UploadWork(ctx, 5, &apigen.ClusterChunksUploadWorkRequest{})); len(work.Uploads) != 2 {
		t.Fatalf("the two unconfirmed chunks are still offered, got %d", len(work.Uploads))
	}
}

func TestStatusCountsEachNodesChunksByLocation(t *testing.T) {
	e := newEnv(t, true)
	ctx := context.Background()
	e.sync(t, 5, everything(sets{
		logs: []apigen.LogChunk{logChunk(7, 20_000, 1, 1), logChunk(7, 20_001, 2, 2), logChunk(7, 20_002, 3, 3)},
		node: []apigen.NodeMetricsChunk{nodeChunk(20_000, 1, 4)},
	}))
	e.sync(t, 6, everything(sets{deps: []apigen.DeploymentMetricsChunk{depChunk(7, 20_000, 1, 5)}}))
	logs := e.rows(t, logKind)
	e.archive(t, logs[0])
	e.archive(t, logs[1])
	e.sync(t, 5, &apigen.ClusterChunksSyncRequest{FromDay: 20_000, ToDay: 20_000, Logs: apigen.Some(apigen.LogChunkSet{})})

	st := erru.Must(e.Status(ctx))
	if !st.Enabled || len(st.Nodes) != 2 || st.Nodes[0].NodeID != 5 || st.Nodes[1].NodeID != 6 {
		t.Fatalf("status: %+v", st)
	}
	l := st.Nodes[0].Logs
	if l.Local.Chunks != 2 || l.Archived.Chunks != 2 || l.Pending.Chunks != 1 || l.LastArchivedAt == 0 {
		t.Fatalf("node 5 logs: %+v", l)
	}
	if l.Local.Bytes != 1002+1003 || l.Archived.Bytes != 1001+1002 || l.Pending.Bytes != 1003 {
		t.Fatalf("node 5 log bytes: %+v", l)
	}
	if n := st.Nodes[0].NodeMetrics; n.Local.Chunks != 1 || n.Pending.Chunks != 1 || n.Archived.Chunks != 0 || n.LastArchivedAt != 0 {
		t.Fatalf("node 5 node metrics: %+v", n)
	}
	if d := st.Nodes[1].DeploymentMetrics; d.Pending.Chunks != 1 || st.Nodes[1].Logs.Local.Chunks != 0 {
		t.Fatalf("node 6: %+v", st.Nodes[1])
	}
	e.settings.Archive.Enabled = systemconfig.BoolLiteral(false)
	if erru.Must(e.Status(ctx)).Enabled {
		t.Fatal("status reads enabled with the archive setting off")
	}
}

func TestObjectKeysMirrorTheNodesPaths(t *testing.T) {
	log := pq.LogChunkRow(&apigen.LogChunk{DeploymentID: 42, NodeID: 3, Day: 20_000, Level: 2, Seq: 987, MinTime: 1_700_000_000_123_456_789, MaxTime: 1_700_000_100_999_000_000})
	dep := pq.DeploymentMetricsChunkRow(&apigen.DeploymentMetricsChunk{DeploymentID: 42, NodeID: 3, Day: 20_000, Seq: 2})
	node := pq.NodeMetricsChunkRow(&apigen.NodeMetricsChunk{NodeID: 3, Day: 20_000, Seq: 1})
	for got, want := range map[string]string{
		ObjectKey(&log):  "logs/42/20241004/L2_1700000000123-1700000100999_n3_987.parquet",
		ObjectKey(&dep):  "metrics/20241004/d42_n3_2.parquet",
		ObjectKey(&node): "metrics/node/20241004/n3_1.parquet",
	} {
		if got != want {
			t.Errorf("key %s, want %s", got, want)
		}
	}
}
