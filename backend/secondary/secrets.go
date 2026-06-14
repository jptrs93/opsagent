package secondary

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine/secretdist"
)

type PrimarySecretProvider struct {
	*secretdist.Cache
	capi *apigen.OpsagentClusterV1Capi
}

func NewPrimarySecretProvider(baseURL string, client *http.Client) *PrimarySecretProvider {
	return &PrimarySecretProvider{
		Cache: secretdist.NewCache(),
		capi:  apigen.NewOpsagentClusterV1Capi(baseURL, apigen.WithOpsagentClusterV1CapiHTTPClient(client)),
	}
}

func (p *PrimarySecretProvider) FetchSecrets(ctx context.Context, keys []string) (map[string]string, error) {
	resp, err := p.capi.GetV1ClusterSecrets(ctx, &apigen.ClusterSecretsRequest{Keys: keys})
	if err != nil {
		return nil, fmt.Errorf("fetching secrets from primary: %w", err)
	}
	values := make(map[string]string, len(resp.Items))
	for _, item := range resp.Items {
		if item == nil {
			continue
		}
		values[item.Key] = string(item.Value)
	}
	for _, key := range keys {
		if _, ok := values[key]; !ok {
			return nil, fmt.Errorf("primary did not return secret %q", key)
		}
	}
	p.Store(values)
	return values, nil
}
