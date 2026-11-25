package pq

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type ChunkArchive int

const (
	ChunkArchiveNone ChunkArchive = 0
	ChunkArchiveS3   ChunkArchive = 1
)

var ChunkKinds = []apigen.CoreEntityType{
	apigen.CoreEntityType_CORE_ENTITY_LOG_CHUNK,
	apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT_METRICS_CHUNK,
	apigen.CoreEntityType_CORE_ENTITY_NODE_METRICS_CHUNK,
}

func IsChunkKind(t apigen.CoreEntityType) bool {
	_, ok := chunkTables[t]
	return ok
}

type chunkTable struct {
	name       string
	deployment bool
	level      bool
}

var chunkTables = map[apigen.CoreEntityType]chunkTable{
	apigen.CoreEntityType_CORE_ENTITY_LOG_CHUNK:                {name: "log_chunks", deployment: true, level: true},
	apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT_METRICS_CHUNK: {name: "deployment_metrics_chunks", deployment: true},
	apigen.CoreEntityType_CORE_ENTITY_NODE_METRICS_CHUNK:       {name: "node_metrics_chunks"},
}

func chunkTableOf(kind apigen.CoreEntityType) (chunkTable, error) {
	t, ok := chunkTables[kind]
	if !ok {
		return chunkTable{}, fmt.Errorf("%v is not a chunk type", kind)
	}
	return t, nil
}

type ChunkRow struct {
	Kind         apigen.CoreEntityType
	ID           uint64
	NodeID       uint64
	DeploymentID uint64
	Day          uint32
	Level        uint32
	FileSeq      int64
	MinTime      int64
	MaxTime      int64
	RowCount     uint64
	SizeBytes    uint64
	Sha256       []byte
	Local        bool
	Archive      ChunkArchive
	ArchivedTime int64
	CreatedTime  int64
	Seq          int64
	EventTime    int64
	Author       int64
}

func (r *ChunkRow) SameIdentity(o *ChunkRow) bool {
	return r.Kind == o.Kind && r.NodeID == o.NodeID && r.DeploymentID == o.DeploymentID && r.Day == o.Day && r.FileSeq == o.FileSeq
}

func (r *ChunkRow) SameContent(o *ChunkRow) bool {
	return r.SizeBytes == o.SizeBytes && bytes.Equal(r.Sha256, o.Sha256)
}

func (r *ChunkRow) location() apigen.ChunkLocation {
	loc := apigen.ChunkLocation{Local: r.Local}
	if r.Archive == ChunkArchiveS3 {
		loc.Archive = apigen.Some(apigen.ChunkLocationArchiveOneof{S3: &apigen.S3Archive{}})
	}
	return loc
}

func (r *ChunkRow) setLocation(loc apigen.ChunkLocation) {
	r.Local = loc.Local
	r.Archive = ChunkArchiveNone
	if loc.Archive.Present && loc.Archive.Value.S3 != nil {
		r.Archive = ChunkArchiveS3
	}
}

func (r *ChunkRow) LogChunk() *apigen.LogChunk {
	return &apigen.LogChunk{ID: r.ID, DeploymentID: r.DeploymentID, NodeID: r.NodeID, Day: r.Day, Level: r.Level, Seq: r.FileSeq,
		MinTime: r.MinTime, MaxTime: r.MaxTime, RowCount: r.RowCount, SizeBytes: r.SizeBytes, Sha256: r.Sha256, Location: r.location()}
}

func (r *ChunkRow) DeploymentMetricsChunk() *apigen.DeploymentMetricsChunk {
	return &apigen.DeploymentMetricsChunk{ID: r.ID, DeploymentID: r.DeploymentID, NodeID: r.NodeID, Day: r.Day, Seq: uint32(r.FileSeq),
		MinTime: r.MinTime, MaxTime: r.MaxTime, RowCount: r.RowCount, SizeBytes: r.SizeBytes, Sha256: r.Sha256, Location: r.location()}
}

func (r *ChunkRow) NodeMetricsChunk() *apigen.NodeMetricsChunk {
	return &apigen.NodeMetricsChunk{ID: r.ID, NodeID: r.NodeID, Day: r.Day, Seq: uint32(r.FileSeq),
		MinTime: r.MinTime, MaxTime: r.MaxTime, RowCount: r.RowCount, SizeBytes: r.SizeBytes, Sha256: r.Sha256, Location: r.location()}
}

func (r *ChunkRow) Entity() apigen.CoreEntity {
	switch r.Kind {
	case apigen.CoreEntityType_CORE_ENTITY_LOG_CHUNK:
		return entityOf(apigen.CoreEntityValueOneof{LogChunk: r.LogChunk()})
	case apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT_METRICS_CHUNK:
		return entityOf(apigen.CoreEntityValueOneof{DeploymentMetricsChunk: r.DeploymentMetricsChunk()})
	}
	return entityOf(apigen.CoreEntityValueOneof{NodeMetricsChunk: r.NodeMetricsChunk()})
}

func LogChunkRow(c *apigen.LogChunk) ChunkRow {
	r := ChunkRow{Kind: apigen.CoreEntityType_CORE_ENTITY_LOG_CHUNK, ID: c.ID, NodeID: c.NodeID, DeploymentID: c.DeploymentID, Day: c.Day, Level: c.Level,
		FileSeq: c.Seq, MinTime: c.MinTime, MaxTime: c.MaxTime, RowCount: c.RowCount, SizeBytes: c.SizeBytes, Sha256: c.Sha256}
	r.setLocation(c.Location)
	return r
}

func DeploymentMetricsChunkRow(c *apigen.DeploymentMetricsChunk) ChunkRow {
	r := ChunkRow{Kind: apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT_METRICS_CHUNK, ID: c.ID, NodeID: c.NodeID, DeploymentID: c.DeploymentID, Day: c.Day,
		FileSeq: int64(c.Seq), MinTime: c.MinTime, MaxTime: c.MaxTime, RowCount: c.RowCount, SizeBytes: c.SizeBytes, Sha256: c.Sha256}
	r.setLocation(c.Location)
	return r
}

func NodeMetricsChunkRow(c *apigen.NodeMetricsChunk) ChunkRow {
	r := ChunkRow{Kind: apigen.CoreEntityType_CORE_ENTITY_NODE_METRICS_CHUNK, ID: c.ID, NodeID: c.NodeID, Day: c.Day,
		FileSeq: int64(c.Seq), MinTime: c.MinTime, MaxTime: c.MaxTime, RowCount: c.RowCount, SizeBytes: c.SizeBytes, Sha256: c.Sha256}
	r.setLocation(c.Location)
	return r
}

func chunkRowOfEntity(v *apigen.CoreEntityValueOneof) (ChunkRow, bool) {
	switch {
	case v.LogChunk != nil:
		return LogChunkRow(v.LogChunk), true
	case v.DeploymentMetricsChunk != nil:
		return DeploymentMetricsChunkRow(v.DeploymentMetricsChunk), true
	case v.NodeMetricsChunk != nil:
		return NodeMetricsChunkRow(v.NodeMetricsChunk), true
	}
	return ChunkRow{}, false
}

func ChunkMutation(meta EventMeta, r ChunkRow) Mutation {
	return Mutation{EventMeta: meta, Type: r.Kind, ID: r.ID, Entity: r.Entity()}
}

func (t chunkTable) columns() string {
	cols := []string{"id", "node_id"}
	if t.deployment {
		cols = append(cols, "deployment_id")
	}
	cols = append(cols, "day")
	if t.level {
		cols = append(cols, "level")
	}
	cols = append(cols, "file_seq", "min_time", "max_time", "row_count", "size_bytes", "sha256", "is_local", "archive", "archived_time", "created_time", "seq", "event_time", "author")
	return strings.Join(cols, ", ")
}

func (t chunkTable) scan(kind apigen.CoreEntityType, row scanner) (ChunkRow, error) {
	r := ChunkRow{Kind: kind}
	dest := []any{&r.ID, &r.NodeID}
	if t.deployment {
		dest = append(dest, &r.DeploymentID)
	}
	dest = append(dest, &r.Day)
	if t.level {
		dest = append(dest, &r.Level)
	}
	dest = append(dest, &r.FileSeq, &r.MinTime, &r.MaxTime, &r.RowCount, &r.SizeBytes, &r.Sha256, &r.Local, &r.Archive, &r.ArchivedTime, &r.CreatedTime, &r.Seq, &r.EventTime, &r.Author)
	return r, row.Scan(dest...)
}

func (q *Queries) queryChunks(ctx context.Context, kind apigen.CoreEntityType, where string, args ...any) ([]ChunkRow, error) {
	t, err := chunkTableOf(kind)
	if err != nil {
		return nil, err
	}
	rows, err := q.db.QueryContext(ctx, `SELECT `+t.columns()+` FROM `+t.name+` `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChunkRow
	for rows.Next() {
		r, err := t.scan(kind, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (q *Queries) GetChunk(ctx context.Context, kind apigen.CoreEntityType, id uint64) (*ChunkRow, error) {
	t, err := chunkTableOf(kind)
	if err != nil {
		return nil, err
	}
	r, err := t.scan(kind, q.db.QueryRowContext(ctx, `SELECT `+t.columns()+` FROM `+t.name+` WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (q *Queries) ListChunks(ctx context.Context, kind apigen.CoreEntityType) ([]ChunkRow, error) {
	return q.queryChunks(ctx, kind, `ORDER BY id`)
}

func (q *Queries) ListNodeChunks(ctx context.Context, kind apigen.CoreEntityType, nodeID uint64) ([]ChunkRow, error) {
	return q.queryChunks(ctx, kind, `WHERE node_id = ? ORDER BY id`, nodeID)
}

func (q *Queries) ListNodeChunksInDays(ctx context.Context, kind apigen.CoreEntityType, nodeID uint64, fromDay, toDay uint32) ([]ChunkRow, error) {
	return q.queryChunks(ctx, kind, `WHERE node_id = ? AND day >= ? AND day <= ? ORDER BY id`, nodeID, fromDay, toDay)
}

func (q *Queries) ListNodeChunksToArchive(ctx context.Context, kind apigen.CoreEntityType, nodeID uint64, limit int) ([]ChunkRow, error) {
	return q.queryChunks(ctx, kind, `WHERE node_id = ? AND is_local = 1 AND archive = 0 ORDER BY day, id LIMIT ?`, nodeID, limit)
}

type ChunkTotal struct {
	NodeID       uint64
	Local        bool
	Archive      ChunkArchive
	Chunks       int64
	Bytes        int64
	LastArchived int64
}

func (q *Queries) ChunkTotals(ctx context.Context, kind apigen.CoreEntityType) ([]ChunkTotal, error) {
	t, err := chunkTableOf(kind)
	if err != nil {
		return nil, err
	}
	rows, err := q.db.QueryContext(ctx, `SELECT node_id, is_local, archive, COUNT(*), SUM(size_bytes), MAX(archived_time) FROM `+t.name+` GROUP BY node_id, is_local, archive ORDER BY node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChunkTotal
	for rows.Next() {
		var c ChunkTotal
		if err := rows.Scan(&c.NodeID, &c.Local, &c.Archive, &c.Chunks, &c.Bytes, &c.LastArchived); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (q *Queries) reduceChunk(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id uint64, v *apigen.CoreEntityValueOneof) error {
	r, ok := chunkRowOfEntity(v)
	if !ok {
		return fmt.Errorf("payload has no chunk")
	}
	t, err := chunkTableOf(r.Kind)
	if err != nil {
		return err
	}
	cols := []string{"id", "node_id"}
	args := []any{id, r.NodeID}
	if t.deployment {
		cols, args = append(cols, "deployment_id"), append(args, r.DeploymentID)
	}
	cols, args = append(cols, "day"), append(args, r.Day)
	if t.level {
		cols, args = append(cols, "level"), append(args, r.Level)
	}
	archivedTime := int64(0)
	if r.Archive != ChunkArchiveNone {
		archivedTime = env.EventTime
	}
	cols = append(cols, "file_seq", "min_time", "max_time", "row_count", "size_bytes", "sha256", "is_local", "archive", "archived_time", "created_time", "seq", "event_time", "author")
	args = append(args, r.FileSeq, r.MinTime, r.MaxTime, r.RowCount, r.SizeBytes, notNullBlob(r.Sha256), r.Local, int64(r.Archive), archivedTime, env.EventTime, env.Seq, env.EventTime, env.Author)
	sets := make([]string, 0, len(cols))
	for _, c := range cols {
		switch c {
		case "id", "created_time":
		case "archived_time":
			sets = append(sets, `archived_time = CASE WHEN `+t.name+`.archive = 0 THEN excluded.archived_time ELSE `+t.name+`.archived_time END`)
		default:
			sets = append(sets, c+` = excluded.`+c)
		}
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")
	return q.upsert(ctx, meta, `INSERT INTO `+t.name+` (`+strings.Join(cols, ", ")+`) VALUES (`+marks+`)
ON CONFLICT (id) DO UPDATE SET `+strings.Join(sets, ", ")+`
RETURNING created_time`, args...)
}

func (q *Queries) deleteChunkRow(ctx context.Context, kind apigen.CoreEntityType, id uint64) error {
	t, err := chunkTableOf(kind)
	if err != nil {
		return err
	}
	_, err = q.db.ExecContext(ctx, `DELETE FROM `+t.name+` WHERE id = ?`, id)
	return err
}

func (q *Queries) NodeChunkReleases(ctx context.Context, meta EventMeta, nodeID uint64) ([]Mutation, error) {
	var out []Mutation
	meta.EventType = apigen.AuthzVerb_AUTHZ_VERB_UPDATE
	for _, kind := range ChunkKinds {
		rows, err := q.ListNodeChunks(ctx, kind, nodeID)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			switch {
			case r.Archive == ChunkArchiveNone:
				out = append(out, DeleteMutation(meta, kind, r.ID))
			case r.Local:
				r.Local = false
				out = append(out, ChunkMutation(meta, r))
			}
		}
	}
	return out, nil
}
