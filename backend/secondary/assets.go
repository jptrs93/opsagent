package secondary

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type PrimaryAssetProvider struct {
	capi *apigen.OpsagentClusterV1Capi
}

func NewPrimaryAssetProvider(baseURL string, client *http.Client) *PrimaryAssetProvider {
	return &PrimaryAssetProvider{
		capi: apigen.NewOpsagentClusterV1Capi(baseURL, apigen.WithOpsagentClusterV1CapiHTTPClient(client)),
	}
}

func (p *PrimaryAssetProvider) FetchAsset(ctx context.Context, assetID, version int32) (*apigen.ClusterAssetBlob, error) {
	asset, err := p.capi.GetV1ClusterAsset(ctx, &apigen.ClusterAssetRequest{AssetID: assetID, Version: version})
	if err != nil {
		return nil, fmt.Errorf("fetching asset from primary: %w", err)
	}
	return asset, nil
}
