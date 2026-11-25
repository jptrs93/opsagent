package state

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func chunkForTest(kind apigen.CoreEntityType, node, deployment uint64, day uint32, fileSeq int64) pq.ChunkRow {
	r := pq.ChunkRow{Kind: kind, NodeID: node, Day: day, FileSeq: fileSeq, MinTime: 1_000, MaxTime: 2_000, RowCount: 10, SizeBytes: 4_096,
		Sha256: bytes.Repeat([]byte{byte(fileSeq)}, 32), Local: true}
	if kind != apigen.CoreEntityType_CORE_ENTITY_NODE_METRICS_CHUNK {
		r.DeploymentID = deployment
	}
	if kind == apigen.CoreEntityType_CORE_ENTITY_LOG_CHUNK {
		r.Level = 2
	}
	return r
}

func createChunkForTest(t testing.TB, s *Service, r pq.ChunkRow) pq.ChunkRow {
	t.Helper()
	ctx := context.Background()
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		id, err := q.NextEntityID(ctx, r.Kind)
		if err != nil {
			return nil, err
		}
		r.ID = id
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), EventType: apigen.AuthzVerb_AUTHZ_VERB_CREATE}
		return pq.NewUpdate(pq.ChunkMutation(meta, r)), nil
	}))
	return r
}

func updateChunkForTest(t testing.TB, s *Service, r pq.ChunkRow, eventTime int64) {
	t.Helper()
	erru.Must(0, s.Commit(context.Background(), nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: eventTime, EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE}
		return pq.NewUpdate(pq.ChunkMutation(meta, r)), nil
	}))
}

func deleteChunkForTest(t testing.TB, s *Service, r pq.ChunkRow) {
	t.Helper()
	erru.Must(0, s.Commit(context.Background(), nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli()}
		return pq.NewUpdate(pq.DeleteMutation(meta, r.Kind, r.ID)), nil
	}))
}

func seedChunkHistory(t testing.TB, s *Service) {
	t.Helper()
	now := time.Now().UnixMilli()
	for _, kind := range pq.ChunkKinds {
		archived := createChunkForTest(t, s, chunkForTest(kind, 1, 7, 20_000, 1))
		archived.Archive = pq.ChunkArchiveS3
		updateChunkForTest(t, s, archived, now+1)
		archived.Local = false
		updateChunkForTest(t, s, archived, now+2)
		deleteChunkForTest(t, s, createChunkForTest(t, s, chunkForTest(kind, 1, 7, 20_000, 2)))
		createChunkForTest(t, s, chunkForTest(kind, 2, 8, 20_001, 3))
	}
}

func TestChunkRowKeepsTheTimeItWasFirstArchived(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	ctx := context.Background()
	for _, kind := range pq.ChunkKinds {
		r := createChunkForTest(t, s, chunkForTest(kind, 1, 7, 20_000, 1))
		got := erru.Must(s.q.GetChunk(ctx, kind, r.ID))
		if got.ArchivedTime != 0 || !got.Local || got.Archive != pq.ChunkArchiveNone {
			t.Fatalf("%v: a new chunk reads as local and never archived, got %+v", kind, got)
		}
		r.Archive = pq.ChunkArchiveS3
		updateChunkForTest(t, s, r, 5_000)
		r.Local = false
		updateChunkForTest(t, s, r, 9_000)
		got = erru.Must(s.q.GetChunk(ctx, kind, r.ID))
		if got.ArchivedTime != 5_000 || got.EventTime != 9_000 || got.Local || got.Archive != pq.ChunkArchiveS3 {
			t.Fatalf("%v: archived at 5000 and released at 9000, got %+v", kind, got)
		}
		if !got.SameIdentity(&r) || !got.SameContent(&r) {
			t.Fatalf("%v: the row lost its identity or content: %+v", kind, got)
		}
		loc := got.Entity()
		back, ok := chunkRowOfEntityForTest(&loc)
		if !ok || back.Local || back.Archive != pq.ChunkArchiveS3 {
			t.Fatalf("%v: the location does not survive the entity: %+v", kind, back)
		}
	}
}

func chunkRowOfEntityForTest(e *apigen.CoreEntity) (pq.ChunkRow, bool) {
	switch {
	case e.Value.LogChunk != nil:
		return pq.LogChunkRow(e.Value.LogChunk), true
	case e.Value.DeploymentMetricsChunk != nil:
		return pq.DeploymentMetricsChunkRow(e.Value.DeploymentMetricsChunk), true
	case e.Value.NodeMetricsChunk != nil:
		return pq.NodeMetricsChunkRow(e.Value.NodeMetricsChunk), true
	}
	return pq.ChunkRow{}, false
}

func TestChunkQueriesAreScopedToANodeAndItsDays(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	ctx := context.Background()
	kind := apigen.CoreEntityType_CORE_ENTITY_LOG_CHUNK
	old := createChunkForTest(t, s, chunkForTest(kind, 1, 7, 19_990, 1))
	mid := createChunkForTest(t, s, chunkForTest(kind, 1, 7, 20_000, 2))
	done := createChunkForTest(t, s, chunkForTest(kind, 1, 8, 20_000, 3))
	createChunkForTest(t, s, chunkForTest(kind, 2, 7, 20_000, 4))
	done.Archive = pq.ChunkArchiveS3
	updateChunkForTest(t, s, done, 7_000)

	ids := func(rows []pq.ChunkRow) []uint64 {
		out := make([]uint64, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.ID)
		}
		return out
	}
	if got := ids(erru.Must(s.q.ListNodeChunksInDays(ctx, kind, 1, 20_000, 20_000))); len(got) != 2 || got[0] != mid.ID || got[1] != done.ID {
		t.Fatalf("day 20000 of node 1 holds the two chunks written there, got %v", got)
	}
	if got := ids(erru.Must(s.q.ListNodeChunksToArchive(ctx, kind, 1, 10))); len(got) != 2 || got[0] != old.ID || got[1] != mid.ID {
		t.Fatalf("the chunks to archive are the unarchived ones, oldest day first, got %v", got)
	}
	if got := ids(erru.Must(s.q.ListNodeChunksToArchive(ctx, kind, 1, 1))); len(got) != 1 || got[0] != old.ID {
		t.Fatalf("the limit keeps the oldest, got %v", got)
	}
	var local, archived, pending int64
	for _, c := range erru.Must(s.q.ChunkTotals(ctx, kind)) {
		if c.NodeID != 1 {
			continue
		}
		if c.Local {
			local += c.Chunks
		}
		if c.Archive != pq.ChunkArchiveNone {
			archived += c.Chunks
			if c.LastArchived != 7_000 {
				t.Fatalf("the newest archive time is 7000, got %d", c.LastArchived)
			}
		}
		if c.Local && c.Archive == pq.ChunkArchiveNone {
			pending += c.Chunks
		}
	}
	if local != 3 || archived != 1 || pending != 2 {
		t.Fatalf("node 1 has 3 local, 1 archived, 2 pending; got %d, %d, %d", local, archived, pending)
	}
	if _, err := s.q.GetChunk(ctx, kind, 999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a chunk with no record reads as no rows, got %v", err)
	}
}

func TestTwoChunkRecordsCannotNameOneFile(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	ctx := context.Background()
	for _, kind := range pq.ChunkKinds {
		createChunkForTest(t, s, chunkForTest(kind, 1, 7, 20_000, 1))
		err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
			r := chunkForTest(kind, 1, 7, 20_000, 1)
			r.ID = erru.Must(q.NextEntityID(ctx, kind))
			return pq.NewUpdate(pq.ChunkMutation(pq.EventMeta{GlobalSeq: seq, EventTime: 1, EventType: apigen.AuthzVerb_AUTHZ_VERB_CREATE}, r)), nil
		})
		if err == nil {
			t.Fatalf("%v: a second record for the same node, day and file was accepted", kind)
		}
	}
}

func TestOpeningSnapshotLeavesChunkRecordsOut(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	ctx := context.Background()
	seedChunkHistory(t, s)
	count := func(entries []*apigen.MaterialisedEntity) int {
		n := 0
		for _, e := range entries {
			if pq.IsChunkKind(e.EntityType) {
				n++
			}
		}
		return n
	}
	if n := count(erru.Must(s.q.Snapshot(ctx))); n != 0 {
		t.Fatalf("an opening snapshot carried %d chunk records", n)
	}
	if n := count(erru.Must(s.q.SnapshotAll(ctx))); n != 6 {
		t.Fatalf("the full snapshot holds the 6 live chunk records, got %d", n)
	}
}
