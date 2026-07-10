package secondary

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type PrimarySecretProvider struct {
	capi *apigen.OpsagentClusterV1Capi
}

func NewPrimarySecretProvider(baseURL string, client *http.Client) *PrimarySecretProvider {
	return &PrimarySecretProvider{
		capi: apigen.NewOpsagentClusterV1Capi(baseURL, apigen.WithOpsagentClusterV1CapiHTTPClient(client)),
	}
}

func (p *PrimarySecretProvider) FetchSecrets(ctx context.Context, ids []int32) (map[int32]string, error) {
	resp, err := p.capi.GetV1ClusterSecrets(ctx, &apigen.ClusterSecretsRequest{Ids: ids})
	if err != nil {
		return nil, fmt.Errorf("fetching secrets from primary: %w", err)
	}
	values := make(map[int32]string, len(resp.Items))
	for _, item := range resp.Items {
		if item == nil {
			continue
		}
		values[item.ID] = string(item.Value)
	}
	return values, nil
}
