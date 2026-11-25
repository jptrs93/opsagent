package chunks

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var InvalidSyncErr = apigen.NewApiErr("Invalid chunk listing", "invalid_chunk_sync", http.StatusBadRequest)

func invalidSync(format string, args ...any) error {
	e := InvalidSyncErr
	e.DisplayErr = fmt.Sprintf(format, args...)
	return e
}

const (
	defaultUploadWork = 16
	maxUploadWork     = 64
)

type Object struct {
	Size   int64
	Sha256 []byte
}

type Target interface {
	Head(ctx context.Context, key string) (Object, bool, error)
	PresignPut(ctx context.Context, key string, sha256 []byte, size int64) (url string, headers map[string]string, err error)
}

type secretStore interface {
	RevealByRef(ref apigen.ValueRef) ([]byte, error)
}

type Service struct {
	DB         *state.Service
	Config     func() *apigen.ClusterSettings
	Loader     systemconfig.Loader
	Secrets    secretStore
	OpenTarget func(cfg *apigen.ClusterSettings) (Target, error)
}

func (s *Service) Enabled() bool {
	cfg := s.Config()
	return s.Loader.MustLoadBoolSetting(cfg.Backup.Enabled) && s.Loader.MustLoadBoolSetting(cfg.Archive.Enabled)
}

func (s *Service) target() (Target, error) {
	cfg := s.Config()
	if s.OpenTarget != nil {
		return s.OpenTarget(cfg)
	}
	return s.openS3(cfg)
}

type identity struct {
	deploymentID uint64
	day          uint32
	fileSeq      int64
}

func identityOf(r *pq.ChunkRow) identity {
	return identity{r.DeploymentID, r.Day, r.FileSeq}
}

func listed(req *apigen.ClusterChunksSyncRequest, nodeID uint64) (map[apigen.CoreEntityType][]pq.ChunkRow, error) {
	out := map[apigen.CoreEntityType][]pq.ChunkRow{}
	if req.Logs.Present {
		rows := make([]pq.ChunkRow, 0, len(req.Logs.Value.Chunks))
		for i := range req.Logs.Value.Chunks {
			rows = append(rows, pq.LogChunkRow(&req.Logs.Value.Chunks[i]))
		}
		out[apigen.CoreEntityType_CORE_ENTITY_LOG_CHUNK] = rows
	}
	if req.DeploymentMetrics.Present {
		rows := make([]pq.ChunkRow, 0, len(req.DeploymentMetrics.Value.Chunks))
		for i := range req.DeploymentMetrics.Value.Chunks {
			rows = append(rows, pq.DeploymentMetricsChunkRow(&req.DeploymentMetrics.Value.Chunks[i]))
		}
		out[apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT_METRICS_CHUNK] = rows
	}
	if req.NodeMetrics.Present {
		rows := make([]pq.ChunkRow, 0, len(req.NodeMetrics.Value.Chunks))
		for i := range req.NodeMetrics.Value.Chunks {
			rows = append(rows, pq.NodeMetricsChunkRow(&req.NodeMetrics.Value.Chunks[i]))
		}
		out[apigen.CoreEntityType_CORE_ENTITY_NODE_METRICS_CHUNK] = rows
	}
	for kind, rows := range out {
		seen := make(map[identity]bool, len(rows))
		for i := range rows {
			r := &rows[i]
			r.ID, r.NodeID, r.Local, r.Archive = 0, nodeID, true, pq.ChunkArchiveNone
			if r.Day < req.FromDay || r.Day > req.ToDay {
				return nil, invalidSync("%v of day %d is outside %d..%d", kind, r.Day, req.FromDay, req.ToDay)
			}
			if len(r.Sha256) != 32 || r.MinTime > r.MaxTime {
				return nil, invalidSync("%v of day %d has no hash or an inverted time range", kind, r.Day)
			}
			if seen[identityOf(r)] {
				return nil, invalidSync("%v of day %d is listed twice", kind, r.Day)
			}
			seen[identityOf(r)] = true
		}
	}
	return out, nil
}

func (s *Service) Sync(ctx context.Context, nodeID uint64, req *apigen.ClusterChunksSyncRequest) (*apigen.ClusterChunksSyncResponse, error) {
	if req.FromDay > req.ToDay {
		return nil, invalidSync("from_day %d is after to_day %d", req.FromDay, req.ToDay)
	}
	sets, err := listed(req, nodeID)
	if err != nil {
		return nil, err
	}
	resp := &apigen.ClusterChunksSyncResponse{}
	err = s.DB.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		*resp = apigen.ClusterChunksSyncResponse{}
		meta := func(verb apigen.AuthzVerb) pq.EventMeta {
			return pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), EventType: verb}
		}
		create, update := meta(apigen.AuthzVerb_AUTHZ_VERB_CREATE), meta(apigen.AuthzVerb_AUTHZ_VERB_UPDATE)
		update.EventTime = create.EventTime
		out := &state.WriteUpdate{}
		for _, kind := range pq.ChunkKinds {
			rows, covered := sets[kind]
			if !covered {
				continue
			}
			existing, err := q.ListNodeChunksInDays(ctx, kind, nodeID, req.FromDay, req.ToDay)
			if err != nil {
				return nil, err
			}
			known := make(map[identity]*pq.ChunkRow, len(existing))
			for i := range existing {
				known[identityOf(&existing[i])] = &existing[i]
			}
			held := make(map[uint64]bool, len(rows))
			for i := range rows {
				r := rows[i]
				e, ok := known[identityOf(&r)]
				switch {
				case !ok:
					if r.ID, err = q.NextEntityID(ctx, kind); err != nil {
						return nil, err
					}
					pq.AppendMutations(out, pq.ChunkMutation(create, r))
					resp.Created++
				case e.SameContent(&r):
					held[e.ID] = true
					if !e.Local {
						back := *e
						back.Local = true
						pq.AppendMutations(out, pq.ChunkMutation(update, back))
					}
				case e.Archive == pq.ChunkArchiveNone:
					held[e.ID] = true
					r.ID = e.ID
					pq.AppendMutations(out, pq.ChunkMutation(update, r))
					resp.Created++
				default:
					resp.Conflicts++
				}
			}
			for i := range existing {
				e := existing[i]
				if held[e.ID] || !e.Local {
					continue
				}
				if e.Archive == pq.ChunkArchiveNone {
					pq.AppendMutations(out, pq.DeleteMutation(update, kind, e.ID))
				} else {
					e.Local = false
					pq.AppendMutations(out, pq.ChunkMutation(update, e))
				}
				resp.Released++
			}
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

type refKey struct {
	kind apigen.CoreEntityType
	id   uint64
}

type candidate struct {
	row pq.ChunkRow
	key string
}

func (s *Service) candidates(ctx context.Context, nodeID uint64, limit int, exclude []apigen.ChunkRef) ([]candidate, error) {
	skip := map[refKey]bool{}
	for _, ref := range exclude {
		skip[refKey{ref.EntityType, ref.EntityID}] = true
	}
	q := s.DB.Queries()
	var out []candidate
	for _, kind := range pq.ChunkKinds {
		rows, err := q.ListNodeChunksToArchive(ctx, kind, nodeID, limit+len(exclude))
		if err != nil {
			return nil, err
		}
		taken := 0
		for _, r := range rows {
			if taken == limit {
				break
			}
			if skip[refKey{kind, r.ID}] {
				continue
			}
			out = append(out, candidate{row: r, key: ObjectKey(&r)})
			taken++
		}
	}
	slices.SortStableFunc(out, func(a, b candidate) int { return cmp.Compare(a.row.Day, b.row.Day) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func holds(obj Object, r *pq.ChunkRow) bool {
	return obj.Size == int64(r.SizeBytes) && (obj.Sha256 == nil || bytes.Equal(obj.Sha256, r.Sha256))
}

func (s *Service) UploadWork(ctx context.Context, nodeID uint64, req *apigen.ClusterChunksUploadWorkRequest) (*apigen.ClusterChunksUploadWorkResponse, error) {
	resp := &apigen.ClusterChunksUploadWorkResponse{}
	if !s.Enabled() {
		return resp, nil
	}
	limit := int(req.Limit)
	if limit == 0 {
		limit = defaultUploadWork
	}
	limit = min(limit, maxUploadWork)
	found, err := s.candidates(ctx, nodeID, limit, req.Exclude)
	if err != nil || len(found) == 0 {
		return resp, err
	}
	target, err := s.target()
	if err != nil {
		return nil, err
	}
	var present []pq.ChunkRow
	for i := range found {
		c := &found[i]
		obj, ok, err := target.Head(ctx, c.key)
		if err != nil {
			return nil, fmt.Errorf("checking the archive for %s: %w", c.key, err)
		}
		if ok && holds(obj, &c.row) {
			present = append(present, c.row)
			continue
		}
		url, headers, err := target.PresignPut(ctx, c.key, c.row.Sha256, int64(c.row.SizeBytes))
		if err != nil {
			return nil, fmt.Errorf("signing the upload of %s: %w", c.key, err)
		}
		resp.Uploads = append(resp.Uploads, upload(&c.row, url, headers))
	}
	archived, err := s.markArchived(ctx, present)
	if err != nil {
		return nil, err
	}
	resp.Archived = uint32(archived)
	return resp, nil
}

func upload(r *pq.ChunkRow, url string, headers map[string]string) apigen.ChunkUpload {
	u := apigen.ChunkUpload{Ref: apigen.ChunkRef{EntityType: r.Kind, EntityID: r.ID}, Url: url, Headers: headers}
	switch r.Kind {
	case apigen.CoreEntityType_CORE_ENTITY_LOG_CHUNK:
		u.Chunk.Log = r.LogChunk()
	case apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT_METRICS_CHUNK:
		u.Chunk.DeploymentMetrics = r.DeploymentMetricsChunk()
	default:
		u.Chunk.NodeMetrics = r.NodeMetricsChunk()
	}
	return u
}

func (s *Service) markArchived(ctx context.Context, rows []pq.ChunkRow) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	archived := 0
	err := s.DB.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		archived = 0
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE}
		out := &state.WriteUpdate{}
		for i := range rows {
			cur, err := q.GetChunk(ctx, rows[i].Kind, rows[i].ID)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if !cur.SameIdentity(&rows[i]) || !cur.SameContent(&rows[i]) {
				continue
			}
			archived++
			if cur.Archive != pq.ChunkArchiveNone {
				continue
			}
			cur.Archive = pq.ChunkArchiveS3
			pq.AppendMutations(out, pq.ChunkMutation(meta, *cur))
		}
		return out, nil
	})
	return archived, err
}

func (s *Service) Uploaded(ctx context.Context, nodeID uint64, req *apigen.ClusterChunksUploadedRequest) (*apigen.ClusterChunksUploadedResponse, error) {
	resp := &apigen.ClusterChunksUploadedResponse{}
	if !s.Enabled() || len(req.Uploaded) == 0 {
		return resp, nil
	}
	target, err := s.target()
	if err != nil {
		return nil, err
	}
	q := s.DB.Queries()
	var present []pq.ChunkRow
	seen := map[refKey]bool{}
	for _, ref := range req.Uploaded {
		if seen[refKey{ref.EntityType, ref.EntityID}] || !pq.IsChunkKind(ref.EntityType) {
			continue
		}
		seen[refKey{ref.EntityType, ref.EntityID}] = true
		row, err := q.GetChunk(ctx, ref.EntityType, ref.EntityID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if row.NodeID != nodeID {
			continue
		}
		if row.Archive == pq.ChunkArchiveNone {
			key := ObjectKey(row)
			obj, ok, err := target.Head(ctx, key)
			if err != nil {
				return nil, fmt.Errorf("checking the archive for %s: %w", key, err)
			}
			if !ok || !holds(obj, row) {
				continue
			}
		}
		present = append(present, *row)
	}
	archived, err := s.markArchived(ctx, present)
	if err != nil {
		return nil, err
	}
	resp.Archived = uint32(archived)
	return resp, nil
}

func (s *Service) Status(ctx context.Context) (*apigen.ArchiveStatusResponse, error) {
	resp := &apigen.ArchiveStatusResponse{Enabled: s.Enabled()}
	byNode := map[uint64]*apigen.ArchiveNodeStatus{}
	q := s.DB.Queries()
	for _, kind := range pq.ChunkKinds {
		totals, err := q.ChunkTotals(ctx, kind)
		if err != nil {
			return nil, err
		}
		for _, t := range totals {
			n := byNode[t.NodeID]
			if n == nil {
				n = &apigen.ArchiveNodeStatus{NodeID: t.NodeID}
				byNode[t.NodeID] = n
			}
			k := &n.Logs
			switch kind {
			case apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT_METRICS_CHUNK:
				k = &n.DeploymentMetrics
			case apigen.CoreEntityType_CORE_ENTITY_NODE_METRICS_CHUNK:
				k = &n.NodeMetrics
			}
			add := func(c *apigen.ArchiveCounts) {
				c.Chunks += uint32(t.Chunks)
				c.Bytes += uint64(t.Bytes)
			}
			if t.Local {
				add(&k.Local)
			}
			if t.Archive != pq.ChunkArchiveNone {
				add(&k.Archived)
				k.LastArchivedAt = max(k.LastArchivedAt, t.LastArchived)
			}
			if t.Local && t.Archive == pq.ChunkArchiveNone {
				add(&k.Pending)
			}
		}
	}
	for _, n := range byNode {
		resp.Nodes = append(resp.Nodes, *n)
	}
	slices.SortFunc(resp.Nodes, func(a, b apigen.ArchiveNodeStatus) int { return cmp.Compare(a.NodeID, b.NodeID) })
	return resp, nil
}
