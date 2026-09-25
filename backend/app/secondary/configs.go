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

func (p *SystemConfigProvider) FetchConfigs(ctx context.Context, refs []apigen.ValueRef) (map[apigen.ValueRef]string, error) {
	resp, err := p.capi.GetV1ClusterConfigs(ctx, &apigen.ClusterConfigsRequest{Refs: refPointers(refs)})
	if err != nil {
		return nil, fmt.Errorf("fetching configs from primary: %w", err)
	}
	values := make(map[apigen.ValueRef]string, len(resp.Items))
	for _, item := range resp.Items {
		if item == nil {
			continue
		}
		values[item.Ref] = item.Value
	}
	return values, nil
}
