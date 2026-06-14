package secondary

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine/configdist"
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

func (p *PrimaryConfigProvider) FetchConfigs(ctx context.Context, keys []string) (map[string]string, error) {
	resp, err := p.capi.GetV1ClusterConfigs(ctx, &apigen.ClusterConfigsRequest{Keys: keys})
	if err != nil {
		return nil, fmt.Errorf("fetching configs from primary: %w", err)
	}
	values := make(map[string]string, len(resp.Items))
	for _, item := range resp.Items {
		if item == nil {
			continue
		}
		values[item.Key] = item.Value
	}
	for _, key := range keys {
		if _, ok := values[key]; !ok {
			return nil, fmt.Errorf("primary did not return config %q", key)
		}
	}
	p.Store(values)
	return values, nil
}
