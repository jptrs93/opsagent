package secondary

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type SystemConfigProvider struct {
	capi *apigen.OpsagentClusterV1Capi
}

func NewSystemConfigProvider(baseURL string, client *http.Client) *SystemConfigProvider {
	return &SystemConfigProvider{
		capi: apigen.NewOpsagentClusterV1Capi(baseURL, apigen.WithOpsagentClusterV1CapiHTTPClient(client)),
	}
}

func (p *SystemConfigProvider) FetchConfigs(ctx context.Context, ids []int32) (map[int32]string, error) {
	resp, err := p.capi.GetV1ClusterConfigs(ctx, &apigen.ClusterConfigsRequest{Ids: ids})
	if err != nil {
		return nil, fmt.Errorf("fetching configs from primary: %w", err)
	}
	values := make(map[int32]string, len(resp.Items))
	for _, item := range resp.Items {
		if item == nil {
			continue
		}
		values[item.ID] = item.Value
	}
	return values, nil
}
