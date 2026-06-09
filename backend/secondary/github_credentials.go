package secondary

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine/credentials"
)

const githubCredentialsCacheTTL = 30 * time.Minute

type PrimaryGithubCredentialsProvider struct {
	capi *apigen.OpsagentClusterV1Capi

	mu        sync.Mutex
	cached    credentials.GithubCredentials
	expiresAt time.Time
}

func NewPrimaryGithubCredentialsProvider(baseURL string, client *http.Client) *PrimaryGithubCredentialsProvider {
	return &PrimaryGithubCredentialsProvider{
		capi: apigen.NewOpsagentClusterV1Capi(baseURL, apigen.WithOpsagentClusterV1CapiHTTPClient(client)),
	}
}

func (p *PrimaryGithubCredentialsProvider) GithubCredentials(ctx context.Context) (credentials.GithubCredentials, error) {
	now := time.Now()
	p.mu.Lock()
	if now.Before(p.expiresAt) {
		creds := p.cached
		p.mu.Unlock()
		return creds, nil
	}
	p.mu.Unlock()

	res, err := p.capi.GetV1ClusterGithubCredentials(ctx)
	if err != nil {
		return credentials.GithubCredentials{}, fmt.Errorf("fetching github credentials from primary: %w", err)
	}
	creds := credentials.GithubCredentials{Token: res.Token}

	p.mu.Lock()
	p.cached = creds
	p.expiresAt = now.Add(githubCredentialsCacheTTL)
	p.mu.Unlock()
	return creds, nil
}
