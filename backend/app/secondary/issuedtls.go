package secondary

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/lib/issuedtls"
)

type PrimaryIssuedTLSProvider struct {
	capi *apigen.OpsagentClusterV1Capi
}

func NewPrimaryIssuedTLSProvider(baseURL string, client *http.Client) *PrimaryIssuedTLSProvider {
	return &PrimaryIssuedTLSProvider{
		capi: apigen.NewOpsagentClusterV1Capi(baseURL, apigen.WithOpsagentClusterV1CapiHTTPClient(client)),
	}
}

func (p *PrimaryIssuedTLSProvider) FetchIssuedTLS(ctx context.Context, deploymentID, specVersion int32) (*runtimeinputs.IssuedTLSValue, error) {
	resp, err := p.capi.GetV1ClusterIssuedTls(ctx, &apigen.ClusterIssuedTLSRequest{
		DeploymentID:          deploymentID,
		DeploymentSpecVersion: specVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("fetching issued TLS from primary: %w", err)
	}
	return issuedtls.ValueFromResponse(resp, specVersion), nil
}
