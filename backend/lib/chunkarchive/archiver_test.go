package chunkarchive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/log/logmanager"
	"github.com/jptrs93/opsagent/backend/lib/metrics/metricstore"
)

const (
	logKind  = apigen.CoreEntityType_CORE_ENTITY_LOG_CHUNK
	depKind  = apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT_METRICS_CHUNK
	nodeKind = apigen.CoreEntityType_CORE_ENTITY_NODE_METRICS_CHUNK
)

type fakeLogs struct {
	files []logmanager.SealedFile
	err   error
}

func (f *fakeLogs) SealedFiles(context.Context, int32) ([]logmanager.SealedFile, error) {
	return f.files, f.err
}

type record struct {
	ref      apigen.ChunkRef
	upload   apigen.ChunkUpload
	key      string
	archived bool
}

type fakeRegistry struct {
	mu        sync.Mutex
	bucketURL string
	bucket    *bucket
	records   []*record
	syncs     []*apigen.ClusterChunksSyncRequest
	workCalls int
	blind     bool
	syncErr   error
	checks    int
}

func (r *fakeRegistry) Sync(_ context.Context, req *apigen.ClusterChunksSyncRequest) (*apigen.ClusterChunksSyncResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.syncErr != nil {
		return nil, r.syncErr
	}
	r.syncs = append(r.syncs, req)
	resp := &apigen.ClusterChunksSyncResponse{}
	add := func(kind apigen.CoreEntityType, key string, sum []byte, chunk apigen.ChunkUploadChunkOneof) {
		for _, rec := range r.records {
			if rec.key == key {
				return
			}
		}
		ref := apigen.ChunkRef{EntityType: kind, EntityID: uint64(len(r.records) + 1)}
		r.records = append(r.records, &record{ref: ref, key: key, upload: apigen.ChunkUpload{Ref: ref, Chunk: chunk, Url: r.bucketURL + "/" + key + "?X-Amz-Signature=secret",
			Headers: map[string]string{"x-amz-checksum-sha256": base64.StdEncoding.EncodeToString(sum), "Content-Length": "ignored", "Host": "ignored"}}})
		resp.Created++
	}
	for i := range req.Logs.Value.Chunks {
		c := &req.Logs.Value.Chunks[i]
		add(logKind, fmt.Sprintf("logs/%d/%d/%d", c.DeploymentID, c.Day, c.Seq), c.Sha256, apigen.ChunkUploadChunkOneof{Log: c})
	}
	for i := range req.DeploymentMetrics.Value.Chunks {
		c := &req.DeploymentMetrics.Value.Chunks[i]
		add(depKind, fmt.Sprintf("metrics/%d/d%d_%d", c.Day, c.DeploymentID, c.Seq), c.Sha256, apigen.ChunkUploadChunkOneof{DeploymentMetrics: c})
	}
	for i := range req.NodeMetrics.Value.Chunks {
		c := &req.NodeMetrics.Value.Chunks[i]
		add(nodeKind, fmt.Sprintf("metrics/node/%d/%d", c.Day, c.Seq), c.Sha256, apigen.ChunkUploadChunkOneof{NodeMetrics: c})
	}
	return resp, nil
}

func (r *fakeRegistry) UploadWork(_ context.Context, req *apigen.ClusterChunksUploadWorkRequest) (*apigen.ClusterChunksUploadWorkResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workCalls++
	resp := &apigen.ClusterChunksUploadWorkResponse{}
	limit := int(req.Limit)
	if r.checks > 0 {
		limit = r.checks
	}
	considered := 0
	for _, rec := range r.records {
		excluded := false
		for _, x := range req.Exclude {
			excluded = excluded || x.EntityType == rec.ref.EntityType && x.EntityID == rec.ref.EntityID
		}
		if rec.archived || excluded || considered == limit {
			continue
		}
		considered++
		if r.checks > 0 && r.bucket.has(rec.key) {
			rec.archived = true
			resp.Archived++
			continue
		}
		resp.Uploads = append(resp.Uploads, rec.upload)
	}
	return resp, nil
}

func (r *fakeRegistry) Uploaded(_ context.Context, req *apigen.ClusterChunksUploadedRequest) (*apigen.ClusterChunksUploadedResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	resp := &apigen.ClusterChunksUploadedResponse{}
	for _, ref := range req.Uploaded {
		for _, rec := range r.records {
			if rec.ref.EntityType == ref.EntityType && rec.ref.EntityID == ref.EntityID && !r.blind && r.bucket.has(rec.key) {
				rec.archived = true
				resp.Archived++
			}
		}
	}
	return resp, nil
}

func (r *fakeRegistry) archived() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, rec := range r.records {
		if rec.archived {
			n++
		}
	}
	return n
}

type bucket struct {
	mu      sync.Mutex
	objects map[string][]byte
	refuse  string
	puts    int
}

func (b *bucket) has(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.objects[key]
	return ok
}

func (b *bucket) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.puts++
	key := strings.TrimPrefix(req.URL.Path, "/")
	sum := sha256.Sum256(body)
	switch {
	case req.Method != http.MethodPut || req.URL.Query().Get("X-Amz-Signature") == "":
		http.Error(w, "unsigned", http.StatusForbidden)
	case b.refuse != "" && strings.Contains(key, b.refuse):
		http.Error(w, "<Error><Code>AccessDenied</Code></Error>", http.StatusForbidden)
	case req.ContentLength != int64(len(body)) || req.Header.Get("x-amz-checksum-sha256") != base64.StdEncoding.EncodeToString(sum[:]):
		http.Error(w, "<Error><Code>BadDigest</Code></Error>", http.StatusBadRequest)
	default:
		b.objects[key] = body
	}
}

type fixture struct {
	*Archiver
	reg    *fakeRegistry
	bucket *bucket
	logs   *fakeLogs
	dir    string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	b := &bucket{objects: map[string][]byte{}}
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	f := &fixture{reg: &fakeRegistry{bucketURL: srv.URL, bucket: b}, bucket: b, logs: &fakeLogs{}, dir: t.TempDir()}
	f.Archiver = &Archiver{NodeID: 7, MetricsDir: filepath.Join(f.dir, "metrics"), Logs: f.logs, Registry: f.reg, HTTP: srv.Client()}
	return f
}

func (f *fixture) addLog(t *testing.T, dep int32, day uint32, seq int64, content string) {
	t.Helper()
	path := filepath.Join(f.dir, fmt.Sprintf("log-%d-%d-%d.parquet", dep, day, seq))
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	f.logs.files = append(f.logs.files, logmanager.SealedFile{DeploymentID: dep, Day: day, Level: 2, Seq: seq, MinTime: 1, MaxTime: 2, Rows: 3,
		SizeBytes: int64(len(content)), Sha256: sum[:], Path: path})
}

func (f *fixture) pass(t *testing.T) {
	t.Helper()
	if err := f.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func sealMetrics(t *testing.T, metricsDir string) {
	t.Helper()
	write := func(dir string, files map[string]int32) {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		var entries []string
		for name, deployment := range files {
			content := []byte("sealed " + name)
			if err := os.WriteFile(filepath.Join(dir, name), content, 0o640); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(content)
			entries = append(entries, fmt.Sprintf(`{"name":%q,"deployment_id":%d,"rows":5,"min_time":1000,"max_time":2000,"size_bytes":%d,"sha256":"%x"}`,
				name, deployment, len(content), sum))
		}
		manifest := fmt.Sprintf(`{"node_id":7,"seq":1,"files":[%s]}`, strings.Join(entries, ","))
		if err := os.WriteFile(filepath.Join(dir, "n7_1.manifest"), []byte(manifest), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(metricsDir, "20260301"), map[string]int32{"d11_n7_1.parquet": 11, "d12_n7_1.parquet": 12})
	write(filepath.Join(metricstore.NodeDir(metricsDir), "20260301"), map[string]int32{"n7_1.parquet": 0})
}

func TestPassRecordsEverySealedFileAndSendsItToTheBucket(t *testing.T) {
	f := newFixture(t)
	f.addLog(t, 11, 20_000, 501, "first log file")
	f.addLog(t, 12, 20_001, 502, "second log file")
	sealMetrics(t, f.MetricsDir)
	f.pass(t)

	if len(f.reg.syncs) != 1 {
		t.Fatalf("a small listing went out in %d sync calls", len(f.reg.syncs))
	}
	req := f.reg.syncs[0]
	if req.FromDay != 0 || req.ToDay != math.MaxUint32 || len(req.Logs.Value.Chunks) != 2 || len(req.DeploymentMetrics.Value.Chunks) != 2 || len(req.NodeMetrics.Value.Chunks) != 1 {
		t.Fatalf("sync request: days %d..%d logs=%d deployment metrics=%d node metrics=%d", req.FromDay, req.ToDay,
			len(req.Logs.Value.Chunks), len(req.DeploymentMetrics.Value.Chunks), len(req.NodeMetrics.Value.Chunks))
	}
	if c := req.Logs.Value.Chunks[0]; c.NodeID != 7 || c.DeploymentID != 11 || c.Seq != 501 || c.SizeBytes != 14 || len(c.Sha256) != 32 {
		t.Fatalf("listed log chunk: %+v", c)
	}
	if f.reg.archived() != 5 || len(f.bucket.objects) != 5 {
		t.Fatalf("archived %d records, the bucket holds %d objects, want 5 of each", f.reg.archived(), len(f.bucket.objects))
	}
	if got := string(f.bucket.objects["logs/11/20000/501"]); got != "first log file" {
		t.Fatalf("the bucket holds %q for the first log file", got)
	}
	for _, c := range req.DeploymentMetrics.Value.Chunks {
		local, err := os.ReadFile(metricstore.RunFilePath(f.MetricsDir, c.Day, int32(c.DeploymentID), 7, int(c.Seq)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(f.bucket.objects[fmt.Sprintf("metrics/%d/d%d_%d", c.Day, c.DeploymentID, c.Seq)], local) {
			t.Fatalf("the bucket's copy of deployment %d differs from the node's file", c.DeploymentID)
		}
	}

	puts := f.bucket.puts
	f.pass(t)
	if len(f.reg.syncs) != 1 || f.bucket.puts != puts {
		t.Fatalf("a pass with nothing new synced %d times and sent %d files", len(f.reg.syncs)-1, f.bucket.puts-puts)
	}
	f.addLog(t, 11, 20_002, 503, "third log file")
	f.pass(t)
	if len(f.reg.syncs) != 2 || f.reg.archived() != 6 {
		t.Fatalf("a new sealed file: %d syncs, %d archived", len(f.reg.syncs), f.reg.archived())
	}
	f.syncedAt = time.Now().Add(-25 * time.Hour)
	f.pass(t)
	if len(f.reg.syncs) != 3 {
		t.Fatal("a day after the last sync the listing was not sent again")
	}
}

func TestPassLeavesAChunkTypeOutWhenItsListingFails(t *testing.T) {
	f := newFixture(t)
	f.logs.err = errors.New("catalog locked")
	f.pass(t)
	req := f.reg.syncs[0]
	if req.Logs.Present {
		t.Fatal("log chunks were listed as none held although the listing failed")
	}
	if !req.DeploymentMetrics.Present || !req.NodeMetrics.Present {
		t.Fatal("the metrics chunk types were left out")
	}
}

func TestSyncPagesCoverEveryDayOnce(t *testing.T) {
	l := &listing{covered: map[apigen.CoreEntityType]bool{logKind: true, nodeKind: true}}
	for day := uint32(100); day < 110; day++ {
		for seq := range 3 {
			l.logs = append(l.logs, apigen.LogChunk{Day: day, Seq: int64(seq)})
		}
		l.nodeMetrics = append(l.nodeMetrics, apigen.NodeMetricsChunk{Day: day, Seq: 1})
	}
	pages := l.pages(10)
	if len(pages) != 5 {
		t.Fatalf("40 chunks at 4 a day in pages of 10 make %d pages, want 5", len(pages))
	}
	next := uint32(0)
	total := 0
	for i, p := range pages {
		if p.FromDay != next {
			t.Fatalf("page %d starts at day %d, want %d", i, p.FromDay, next)
		}
		if p.DeploymentMetrics.Present || !p.Logs.Present || !p.NodeMetrics.Present {
			t.Fatalf("page %d covers the wrong chunk types", i)
		}
		for _, c := range p.Logs.Value.Chunks {
			if c.Day < p.FromDay || c.Day > p.ToDay {
				t.Fatalf("page %d lists day %d outside %d..%d", i, c.Day, p.FromDay, p.ToDay)
			}
		}
		total += len(p.Logs.Value.Chunks) + len(p.NodeMetrics.Value.Chunks)
		next = p.ToDay + 1
	}
	if pages[4].ToDay != math.MaxUint32 || total != 40 {
		t.Fatalf("pages end at day %d and list %d chunks", pages[4].ToDay, total)
	}
	if empty := (&listing{covered: map[apigen.CoreEntityType]bool{logKind: true}}).pages(10); len(empty) != 1 || empty[0].FromDay != 0 || empty[0].ToDay != math.MaxUint32 || !empty[0].Logs.Present {
		t.Fatalf("an empty listing is one page over every day, got %+v", empty)
	}
}

func TestAFailedUploadIsSetAsideAndTheRestStillGo(t *testing.T) {
	f := newFixture(t)
	for seq := int64(1); seq <= 40; seq++ {
		f.addLog(t, 11, 20_000, seq, fmt.Sprintf("log file %d", seq))
	}
	f.bucket.refuse = "/20000/7"
	f.pass(t)
	if f.reg.archived() != 39 {
		t.Fatalf("archived %d of 40 with one refused, want 39", f.reg.archived())
	}
	f.bucket.refuse = ""
	f.pass(t)
	if f.reg.archived() != 40 {
		t.Fatal("the refused file was not sent on the next pass")
	}
}

func TestThePassGoesOnWhenABatchWasAlreadyInTheBucket(t *testing.T) {
	f := newFixture(t)
	for seq := int64(1); seq <= 6; seq++ {
		f.addLog(t, 11, 20_000, seq, fmt.Sprintf("log file %d", seq))
	}
	f.reg.checks = 2
	for seq := 1; seq <= 4; seq++ {
		f.bucket.objects[fmt.Sprintf("logs/11/20000/%d", seq)] = []byte("held")
	}
	f.pass(t)
	if f.reg.archived() != 6 || f.bucket.puts != 2 {
		t.Fatalf("archived %d of 6 with %d uploads, want 6 with the 2 the bucket lacked", f.reg.archived(), f.bucket.puts)
	}
}

func TestUploadsThePrimaryCannotConfirmAreNotRepeatedInThePass(t *testing.T) {
	f := newFixture(t)
	for seq := int64(1); seq <= 20; seq++ {
		f.addLog(t, 11, 20_000, seq, fmt.Sprintf("log file %d", seq))
	}
	f.reg.blind = true
	f.pass(t)
	if f.bucket.puts != 20 {
		t.Fatalf("sent %d uploads for 20 files the primary never confirms", f.bucket.puts)
	}
}

func TestAFileThatDiffersFromItsRecordIsNotSent(t *testing.T) {
	f := newFixture(t)
	f.addLog(t, 11, 20_000, 1, "the content the record describes")
	f.addLog(t, 11, 20_000, 2, "another file")
	f.pass(t)
	if f.reg.archived() != 2 {
		t.Fatal("setup: both files should be archived")
	}
	for _, rec := range f.reg.records {
		rec.archived = false
	}
	f.bucket.objects = map[string][]byte{}
	changed := sha256.Sum256([]byte("changed"))
	f.logs.files[0].Sha256 = changed[:]
	f.logs.files = f.logs.files[:1]
	puts := f.bucket.puts
	f.syncedDigest = [32]byte{}
	f.pass(t)
	if f.bucket.puts != puts {
		t.Fatalf("sent %d files: one differs from its record and the node no longer holds the other", f.bucket.puts-puts)
	}
}

func TestAnUploadErrorDoesNotCarryTheSignedURL(t *testing.T) {
	f := newFixture(t)
	f.addLog(t, 11, 20_000, 1, "content")
	l := f.list(context.Background())
	sum := sha256.Sum256([]byte("content"))
	u := &apigen.ChunkUpload{Url: "http://127.0.0.1:1/logs/x?X-Amz-Signature=secret",
		Chunk: apigen.ChunkUploadChunkOneof{Log: &apigen.LogChunk{DeploymentID: 11, Day: 20_000, Seq: 1, SizeBytes: 7, Sha256: sum[:]}}}
	err := f.put(context.Background(), u, l)
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "X-Amz") {
		t.Fatalf("upload error: %v", err)
	}
}

func TestAFailedSyncIsRetriedOnTheNextPass(t *testing.T) {
	f := newFixture(t)
	f.addLog(t, 11, 20_000, 1, "content")
	f.reg.syncErr = errors.New("primary unreachable")
	if err := f.Pass(context.Background()); err == nil {
		t.Fatal("a pass whose sync failed reported success")
	}
	if f.bucket.puts != 0 {
		t.Fatal("files were sent after a failed sync")
	}
	f.reg.syncErr = nil
	f.pass(t)
	if f.reg.archived() != 1 {
		t.Fatal("the listing was not synced on the pass after a failure")
	}
}
