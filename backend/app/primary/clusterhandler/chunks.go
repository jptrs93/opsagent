package clusterhandler

import (
	"context"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
)

func (p *Handler) chunkNode(ctx context.Context) (uint64, error) {
	if p.Chunks == nil {
		return 0, fmt.Errorf("chunk registry is not configured")
	}
	machine, err := requireMachine(ctx)
	if err != nil {
		return 0, err
	}
	nodeID, err := nodes.MemberNodeIDByIdentifier(p.store.Queries(), machine)
	if err != nil {
		return 0, clusterForbiddenErr
	}
	return nodeID, nil
}

func (p *Handler) PostV1ClusterChunksSync(authCtx apigen.Context, req *apigen.ClusterChunksSyncRequest) (*apigen.ClusterChunksSyncResponse, error) {
	nodeID, err := p.chunkNode(authCtx)
	if err != nil {
		return nil, err
	}
	return p.Chunks.Sync(authCtx, nodeID, req)
}

func (p *Handler) PostV1ClusterChunksUploadWork(authCtx apigen.Context, req *apigen.ClusterChunksUploadWorkRequest) (*apigen.ClusterChunksUploadWorkResponse, error) {
	nodeID, err := p.chunkNode(authCtx)
	if err != nil {
		return nil, err
	}
	return p.Chunks.UploadWork(authCtx, nodeID, req)
}

func (p *Handler) PostV1ClusterChunksUploaded(authCtx apigen.Context, req *apigen.ClusterChunksUploadedRequest) (*apigen.ClusterChunksUploadedResponse, error) {
	nodeID, err := p.chunkNode(authCtx)
	if err != nil {
		return nil, err
	}
	return p.Chunks.Uploaded(authCtx, nodeID, req)
}
