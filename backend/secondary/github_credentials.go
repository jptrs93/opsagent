package secondary

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/repo/githubcredentials"
)

type PrimaryGithubCredentialsProvider struct {
	capi *apigen.OpsagentClusterV1Capi

	mu     sync.Mutex
	cached *githubcredentials.GithubCredentials
}

func NewPrimaryGithubCredentialsProvider(baseURL string, client *http.Client) *PrimaryGithubCredentialsProvider {
	return &PrimaryGithubCredentialsProvider{
		capi: apigen.NewOpsagentClusterV1Capi(baseURL, apigen.WithOpsagentClusterV1CapiHTTPClient(client)),
	}
}

func (p *PrimaryGithubCredentialsProvider) LoadCredentials(ctx context.Context) (*githubcredentials.GithubCredentials, error) {
	res, err := p.capi.GetV1ClusterGithubCredentials(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching github credentials from primary: %w", err)
	}
	creds := &githubcredentials.GithubCredentials{Token: res.Token, ChangedAt: res.ChangedAt}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cached != nil && !creds.ChangedAt.IsZero() && creds.ChangedAt.Equal(p.cached.ChangedAt) {
		return &githubcredentials.GithubCredentials{Token: p.cached.Token, ChangedAt: p.cached.ChangedAt}, nil
	}
	p.cached = creds
	return creds, nil
}
