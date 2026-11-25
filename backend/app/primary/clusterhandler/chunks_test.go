package clusterhandler

import (
	"bytes"
	"context"
	"errors"
	"math"
	"net/netip"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/chunks"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func TestChunkEndpointsActForTheNodeTheCertificateNames(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	t.Cleanup(func() { _ = store.Close() })
	nodes.EnsurePrimaryNode(store, "primary", "primary-id", netip.MustParseAddr("10.0.0.1"), "")
	node := acceptTestSecondary(t, store, "secondary-id")
	handler := New(store, nil, nil, nil, network.Prefix{}, nil, nil, nil, nil)
	handler.Chunks = &chunks.Service{DB: store}
	peer := apigen.Context{Ctx: context.WithValue(context.Background(), machineCtxKey{}, node.Identifier)}
	listing := &apigen.ClusterChunksSyncRequest{ToDay: math.MaxUint32, NodeMetrics: apigen.Some(apigen.NodeMetricsChunkSet{Chunks: []apigen.NodeMetricsChunk{
		{NodeID: 999, Day: 20_000, Seq: 1, MinTime: 1, MaxTime: 2, RowCount: 3, SizeBytes: 10, Sha256: bytes.Repeat([]byte{1}, 32)},
	}})}

	resp, err := handler.PostV1ClusterChunksSync(peer, listing)
	if err != nil || resp.Created != 1 {
		t.Fatalf("sync as the member node: %+v, %v", resp, err)
	}
	rows, err := store.Queries().ListChunks(context.Background(), apigen.CoreEntityType_CORE_ENTITY_NODE_METRICS_CHUNK)
	if err != nil || len(rows) != 1 || rows[0].NodeID != node.ID {
		t.Fatalf("the record belongs to node %d, want the calling node %d (%v)", rows[0].NodeID, node.ID, err)
	}

	stranger := apigen.Context{Ctx: context.WithValue(context.Background(), machineCtxKey{}, "not-a-member")}
	if _, err := handler.PostV1ClusterChunksSync(stranger, listing); !errors.Is(err, clusterForbiddenErr) {
		t.Fatalf("sync from an unknown machine: %v", err)
	}
	if _, err := handler.PostV1ClusterChunksUploadWork(stranger, &apigen.ClusterChunksUploadWorkRequest{}); !errors.Is(err, clusterForbiddenErr) {
		t.Fatalf("upload work for an unknown machine: %v", err)
	}
	if _, err := handler.PostV1ClusterChunksUploaded(stranger, &apigen.ClusterChunksUploadedRequest{}); !errors.Is(err, clusterForbiddenErr) {
		t.Fatalf("uploaded from an unknown machine: %v", err)
	}
	if _, err := handler.PostV1ClusterChunksSync(apigen.Context{Ctx: context.Background()}, listing); err == nil {
		t.Fatal("sync without a machine identity was accepted")
	}

	if _, err := nodes.EvictNode(apigen.Context{Ctx: context.Background()}, store, node.Identifier, node.Seq, true); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.PostV1ClusterChunksSync(peer, listing); !errors.Is(err, clusterForbiddenErr) {
		t.Fatalf("sync from an evicted node: %v", err)
	}
}
