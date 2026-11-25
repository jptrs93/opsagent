package secondary

import (
	"context"
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type primaryChunkRegistry struct {
	capi *apigen.OpsagentClusterV1Capi
}

func newPrimaryChunkRegistry(baseURL string, client *http.Client) primaryChunkRegistry {
	return primaryChunkRegistry{capi: apigen.NewOpsagentClusterV1Capi(baseURL, apigen.WithOpsagentClusterV1CapiHTTPClient(client))}
}

func (r primaryChunkRegistry) Sync(ctx context.Context, req *apigen.ClusterChunksSyncRequest) (*apigen.ClusterChunksSyncResponse, error) {
	return r.capi.PostV1ClusterChunksSync(ctx, req)
}

func (r primaryChunkRegistry) UploadWork(ctx context.Context, req *apigen.ClusterChunksUploadWorkRequest) (*apigen.ClusterChunksUploadWorkResponse, error) {
	return r.capi.PostV1ClusterChunksUploadWork(ctx, req)
}

func (r primaryChunkRegistry) Uploaded(ctx context.Context, req *apigen.ClusterChunksUploadedRequest) (*apigen.ClusterChunksUploadedResponse, error) {
	return r.capi.PostV1ClusterChunksUploaded(ctx, req)
}
