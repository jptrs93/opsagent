package secondary

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/configdist"
)

type PrimaryConfigProvider struct {
	*configdist.Cache
	capi *apigen.OpsagentClusterV1Capi
}

func NewPrimaryConfigProvider(baseURL string, client *http.Client) *PrimaryConfigProvider {
	return &PrimaryConfigProvider{
		Cache: configdist.NewCache(),
		capi:  apigen.NewOpsagentClusterV1Capi(baseURL, apigen.WithOpsagentClusterV1CapiHTTPClient(client)),
	}
}

func (p *PrimaryConfigProvider) FetchConfigs(ctx context.Context, ids []int32) (map[int32]string, error) {
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
	for _, id := range ids {
		if _, ok := values[id]; !ok {
			return nil, fmt.Errorf("primary did not return config id %d", id)
		}
	}
	p.Store(values)
	return values, nil
}
