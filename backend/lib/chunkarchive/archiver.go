package chunkarchive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jptrs93/goutil/contextu"
	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/log/logmanager"
	"github.com/jptrs93/opsagent/backend/lib/metrics/metricstore"
)

var (
	firstPassDelay   = 2 * time.Minute
	passInterval     = time.Hour
	fullSyncInterval = 24 * time.Hour
	uploadTimeout    = time.Hour
	registryTimeout  = 5 * time.Minute
)

func registryCall[Req, Resp any](ctx context.Context, call func(context.Context, Req) (Resp, error), req Req) (Resp, error) {
	ctx, cancel := context.WithTimeout(ctx, registryTimeout)
	defer cancel()
	return call(ctx, req)
}

const (
	syncPageChunks = 2000
	uploadBatch    = 16
	maxExcluded    = 512
)

type Registry interface {
	Sync(ctx context.Context, req *apigen.ClusterChunksSyncRequest) (*apigen.ClusterChunksSyncResponse, error)
	UploadWork(ctx context.Context, req *apigen.ClusterChunksUploadWorkRequest) (*apigen.ClusterChunksUploadWorkResponse, error)
	Uploaded(ctx context.Context, req *apigen.ClusterChunksUploadedRequest) (*apigen.ClusterChunksUploadedResponse, error)
}

type LogSource interface {
	SealedFiles(ctx context.Context, nodeID int32) ([]logmanager.SealedFile, error)
}

type Archiver struct {
	NodeID     int32
	MetricsDir string
	Logs       LogSource
	Registry   Registry
	HTTP       *http.Client

	syncedDigest [sha256.Size]byte
	syncedAt     time.Time
}

func (a *Archiver) Run(ctx context.Context) {
	ctx = logu.AddTag(ctx, "ChunkArchive")
	contextu.Sleep(ctx, firstPassDelay)
	for ctx.Err() == nil {
		if err := a.Pass(ctx); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "chunk archive pass failed", "err", err)
		}
		contextu.Sleep(ctx, passInterval)
	}
}

type fileKey struct {
	kind         apigen.CoreEntityType
	deploymentID uint64
	day          uint32
	seq          int64
}

type localFile struct {
	path   string
	size   int64
	sha256 []byte
}

type listing struct {
	logs              []apigen.LogChunk
	deploymentMetrics []apigen.DeploymentMetricsChunk
	nodeMetrics       []apigen.NodeMetricsChunk
	covered           map[apigen.CoreEntityType]bool
	files             map[fileKey]localFile
}

func (a *Archiver) list(ctx context.Context) *listing {
	l := &listing{covered: map[apigen.CoreEntityType]bool{}, files: map[fileKey]localFile{}}
	node := uint64(a.NodeID)
	if a.Logs != nil {
		files, err := a.Logs.SealedFiles(ctx, a.NodeID)
		if err != nil {
			slog.WarnContext(ctx, "listing sealed log files failed", "err", err)
		} else {
			kind := apigen.CoreEntityType_CORE_ENTITY_LOG_CHUNK
			l.covered[kind] = true
			for _, f := range files {
				l.logs = append(l.logs, apigen.LogChunk{DeploymentID: uint64(f.DeploymentID), NodeID: node, Day: f.Day, Level: f.Level, Seq: f.Seq,
					MinTime: f.MinTime, MaxTime: f.MaxTime, RowCount: uint64(f.Rows), SizeBytes: uint64(f.SizeBytes), Sha256: f.Sha256})
				l.files[fileKey{kind, uint64(f.DeploymentID), f.Day, f.Seq}] = localFile{f.Path, f.SizeBytes, f.Sha256}
			}
		}
	}
	if files, err := metricstore.SealedFiles(a.MetricsDir, a.NodeID); err != nil {
		slog.WarnContext(ctx, "listing sealed deployment metrics files failed", "err", err)
	} else {
		kind := apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT_METRICS_CHUNK
		l.covered[kind] = true
		for _, f := range files {
			l.deploymentMetrics = append(l.deploymentMetrics, apigen.DeploymentMetricsChunk{DeploymentID: uint64(f.DeploymentID), NodeID: node, Day: f.Day, Seq: uint32(f.Seq),
				MinTime: f.MinTime, MaxTime: f.MaxTime, RowCount: uint64(f.Rows), SizeBytes: uint64(f.SizeBytes), Sha256: f.Sha256})
			l.files[fileKey{kind, uint64(f.DeploymentID), f.Day, int64(f.Seq)}] = localFile{f.Path, f.SizeBytes, f.Sha256}
		}
	}
	if files, err := metricstore.SealedFiles(metricstore.NodeDir(a.MetricsDir), a.NodeID); err != nil {
		slog.WarnContext(ctx, "listing sealed node metrics files failed", "err", err)
	} else {
		kind := apigen.CoreEntityType_CORE_ENTITY_NODE_METRICS_CHUNK
		l.covered[kind] = true
		for _, f := range files {
			l.nodeMetrics = append(l.nodeMetrics, apigen.NodeMetricsChunk{NodeID: node, Day: f.Day, Seq: uint32(f.Seq),
				MinTime: f.MinTime, MaxTime: f.MaxTime, RowCount: uint64(f.Rows), SizeBytes: uint64(f.SizeBytes), Sha256: f.Sha256})
			l.files[fileKey{kind, 0, f.Day, int64(f.Seq)}] = localFile{f.Path, f.SizeBytes, f.Sha256}
		}
	}
	return l
}

func (l *listing) pages(maxChunks int) []*apigen.ClusterChunksSyncRequest {
	perDay := map[uint32]int{}
	for i := range l.logs {
		perDay[l.logs[i].Day]++
	}
	for i := range l.deploymentMetrics {
		perDay[l.deploymentMetrics[i].Day]++
	}
	for i := range l.nodeMetrics {
		perDay[l.nodeMetrics[i].Day]++
	}
	days := make([]uint32, 0, len(perDay))
	for d := range perDay {
		days = append(days, d)
	}
	slices.Sort(days)
	var lastDays []uint32
	count := 0
	for i, d := range days {
		if count > 0 && count+perDay[d] > maxChunks {
			lastDays = append(lastDays, days[i-1])
			count = 0
		}
		count += perDay[d]
	}
	lastDays = append(lastDays, math.MaxUint32)
	out := make([]*apigen.ClusterChunksSyncRequest, 0, len(lastDays))
	from := uint32(0)
	for _, to := range lastDays {
		req := &apigen.ClusterChunksSyncRequest{FromDay: from, ToDay: to}
		in := func(day uint32) bool { return day >= req.FromDay && day <= req.ToDay }
		if l.covered[apigen.CoreEntityType_CORE_ENTITY_LOG_CHUNK] {
			set := apigen.LogChunkSet{}
			for i := range l.logs {
				if in(l.logs[i].Day) {
					set.Chunks = append(set.Chunks, l.logs[i])
				}
			}
			req.Logs = apigen.Some(set)
		}
		if l.covered[apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT_METRICS_CHUNK] {
			set := apigen.DeploymentMetricsChunkSet{}
			for i := range l.deploymentMetrics {
				if in(l.deploymentMetrics[i].Day) {
					set.Chunks = append(set.Chunks, l.deploymentMetrics[i])
				}
			}
			req.DeploymentMetrics = apigen.Some(set)
		}
		if l.covered[apigen.CoreEntityType_CORE_ENTITY_NODE_METRICS_CHUNK] {
			set := apigen.NodeMetricsChunkSet{}
			for i := range l.nodeMetrics {
				if in(l.nodeMetrics[i].Day) {
					set.Chunks = append(set.Chunks, l.nodeMetrics[i])
				}
			}
			req.NodeMetrics = apigen.Some(set)
		}
		out = append(out, req)
		from = to + 1
	}
	return out
}

func (a *Archiver) Pass(ctx context.Context) error {
	l := a.list(ctx)
	pages := l.pages(syncPageChunks)
	h := sha256.New()
	for _, p := range pages {
		h.Write(p.Encode())
	}
	var digest [sha256.Size]byte
	h.Sum(digest[:0])
	if digest != a.syncedDigest || a.syncedAt.IsZero() || time.Since(a.syncedAt) >= fullSyncInterval {
		var created, released, conflicts uint32
		for _, p := range pages {
			resp, err := registryCall(ctx, a.Registry.Sync, p)
			if err != nil {
				return fmt.Errorf("syncing chunk records: %w", err)
			}
			created, released, conflicts = created+resp.Created, released+resp.Released, conflicts+resp.Conflicts
		}
		a.syncedDigest, a.syncedAt = digest, time.Now()
		if created+released+conflicts > 0 {
			slog.InfoContext(ctx, fmt.Sprintf("synced chunk records: created=%d released=%d conflicts=%d", created, released, conflicts))
		}
	}
	return a.upload(ctx, l)
}

func (a *Archiver) upload(ctx context.Context, l *listing) error {
	var exclude []apigen.ChunkRef
	sent, found, failed := 0, 0, 0
	defer func() {
		if sent+found+failed > 0 {
			slog.InfoContext(ctx, fmt.Sprintf("archived chunks: sent=%d already_in_bucket=%d failed=%d", sent, found, failed))
		}
	}()
	for len(exclude) <= maxExcluded-uploadBatch {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		work, err := registryCall(ctx, a.Registry.UploadWork, &apigen.ClusterChunksUploadWorkRequest{Limit: uploadBatch, Exclude: exclude})
		if err != nil {
			return fmt.Errorf("fetching upload work: %w", err)
		}
		found += int(work.Archived)
		if len(work.Uploads) == 0 {
			if work.Archived == 0 {
				return nil
			}
			continue
		}
		var done []apigen.ChunkRef
		for i := range work.Uploads {
			u := &work.Uploads[i]
			if err := a.put(ctx, u, l); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				failed++
				exclude = append(exclude, u.Ref)
				slog.WarnContext(ctx, fmt.Sprintf("uploading %v %d failed", u.Ref.EntityType, u.Ref.EntityID), "err", err)
				continue
			}
			done = append(done, u.Ref)
		}
		if len(done) == 0 {
			continue
		}
		resp, err := registryCall(ctx, a.Registry.Uploaded, &apigen.ClusterChunksUploadedRequest{Uploaded: done})
		if err != nil {
			return fmt.Errorf("reporting uploads: %w", err)
		}
		sent += int(resp.Archived)
		if int(resp.Archived) < len(done) {
			failed += len(done) - int(resp.Archived)
			exclude = append(exclude, done...)
		}
	}
	return nil
}

func uploadTarget(u *apigen.ChunkUpload) (fileKey, int64, []byte, error) {
	switch c := u.Chunk; {
	case c.Log != nil:
		return fileKey{apigen.CoreEntityType_CORE_ENTITY_LOG_CHUNK, c.Log.DeploymentID, c.Log.Day, c.Log.Seq}, int64(c.Log.SizeBytes), c.Log.Sha256, nil
	case c.DeploymentMetrics != nil:
		m := c.DeploymentMetrics
		return fileKey{apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT_METRICS_CHUNK, m.DeploymentID, m.Day, int64(m.Seq)}, int64(m.SizeBytes), m.Sha256, nil
	case c.NodeMetrics != nil:
		m := c.NodeMetrics
		return fileKey{apigen.CoreEntityType_CORE_ENTITY_NODE_METRICS_CHUNK, 0, m.Day, int64(m.Seq)}, int64(m.SizeBytes), m.Sha256, nil
	}
	return fileKey{}, 0, nil, errors.New("upload names no chunk")
}

func (a *Archiver) put(ctx context.Context, u *apigen.ChunkUpload, l *listing) error {
	key, size, sum, err := uploadTarget(u)
	if err != nil {
		return err
	}
	local, ok := l.files[key]
	if !ok {
		return errors.New("this node does not hold the file")
	}
	if local.size != size || !bytes.Equal(local.sha256, sum) {
		return errors.New("the record describes other content than the file on this node")
	}
	f, err := os.Open(local.path)
	if err != nil {
		return err
	}
	defer f.Close()
	ctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.Url, f)
	if err != nil {
		return err
	}
	req.ContentLength = size
	for name, value := range u.Headers {
		if strings.EqualFold(name, "Content-Length") || strings.EqualFold(name, "Host") {
			continue
		}
		req.Header.Set(name, value)
	}
	client := a.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		// A *url.Error prints the URL, and a presigned URL is a credential.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return urlErr.Err
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("bucket answered %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return nil
}
