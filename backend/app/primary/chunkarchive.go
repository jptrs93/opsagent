package primary

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/chunks"
)

type localChunkRegistry struct {
	chunks *chunks.Service
	nodeID uint64
}

func (r localChunkRegistry) Sync(ctx context.Context, req *apigen.ClusterChunksSyncRequest) (*apigen.ClusterChunksSyncResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return r.chunks.Sync(ctx, r.nodeID, req)
}

func (r localChunkRegistry) UploadWork(ctx context.Context, req *apigen.ClusterChunksUploadWorkRequest) (*apigen.ClusterChunksUploadWorkResponse, error) {
	return r.chunks.UploadWork(ctx, r.nodeID, req)
}

func (r localChunkRegistry) Uploaded(ctx context.Context, req *apigen.ClusterChunksUploadedRequest) (*apigen.ClusterChunksUploadedResponse, error) {
	return r.chunks.Uploaded(ctx, r.nodeID, req)
}
