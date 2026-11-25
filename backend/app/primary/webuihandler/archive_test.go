package webuihandler

import (
	"bytes"
	"context"
	"math"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/chunks"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func TestArchiveStatusIsForUsersWhoCanViewNodes(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	h.Chunks = &chunks.Service{DB: h.Store, Loader: h.SystemConfig, Config: func() *apigen.ClusterSettings {
		snapshot := h.SystemConfig.Snapshot()
		return &snapshot.Settings
	}}
	if _, err := h.Chunks.Sync(context.Background(), 4, &apigen.ClusterChunksSyncRequest{ToDay: math.MaxUint32, NodeMetrics: apigen.Some(apigen.NodeMetricsChunkSet{
		Chunks: []apigen.NodeMetricsChunk{{Day: 20_000, Seq: 1, MinTime: 1, MaxTime: 2, RowCount: 3, SizeBytes: 4096, Sha256: bytes.Repeat([]byte{1}, 32)}},
	})}); err != nil {
		t.Fatal(err)
	}

	resp, err := h.PostV1ArchiveStatus(enforceCtx(1, false), &apigen.ArchiveStatusRequest{})
	if err != nil {
		t.Fatalf("cluster admin: %v", err)
	}
	if resp.Enabled || len(resp.Nodes) != 1 || resp.Nodes[0].NodeID != 4 || resp.Nodes[0].NodeMetrics.Pending.Chunks != 1 || resp.Nodes[0].NodeMetrics.Pending.Bytes != 4096 {
		t.Fatalf("status for the cluster admin: %+v", resp)
	}
	for _, user := range []uint64{2, 3} {
		if _, err := h.PostV1ArchiveStatus(enforceCtx(user, false), &apigen.ArchiveStatusRequest{}); err == nil {
			t.Fatalf("user %d, who cannot view every node, read the archive status", user)
		}
	}
}

func TestChunkRecordsNeverReachABrowser(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	h.Chunks = &chunks.Service{DB: h.Store}
	if _, err := h.Chunks.Sync(context.Background(), 4, &apigen.ClusterChunksSyncRequest{ToDay: math.MaxUint32, Logs: apigen.Some(apigen.LogChunkSet{
		Chunks: []apigen.LogChunk{{DeploymentID: 9, Day: 20_000, Level: 2, Seq: 77, MinTime: 1, MaxTime: 2, RowCount: 3, SizeBytes: 10, Sha256: bytes.Repeat([]byte{1}, 32)}},
	})}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range pq.ChunkKinds {
		if got := visibleOpening(t, h, enforceCtx(1, false))[kind]; len(got) != 0 {
			t.Fatalf("the cluster admin's opening snapshot carries %d %v", len(got), kind)
		}
		row := h.rowsOfKind(t, kind)
		if len(row) == 0 {
			continue
		}
		entity := row[0].Entity()
		if newStreamVisibility(h, enforceCtx(1, false)).entityVisible(kind, row[0].ID, &entity) || rootVisible(kind) {
			t.Fatalf("a live %v mutation is visible on a stream", kind)
		}
	}
}

func (h *Handler) rowsOfKind(t *testing.T, kind apigen.CoreEntityType) []pq.ChunkRow {
	t.Helper()
	rows, err := h.Queries.ListChunks(context.Background(), kind)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
