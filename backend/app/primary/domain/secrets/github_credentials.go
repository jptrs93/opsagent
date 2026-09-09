package secrets

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/repo/githubcredentials"
)

type GithubCredentialsProvider struct {
	SecretRef func(context.Context) apigen.SecretRef
	Secrets   *Manager
}

func (p GithubCredentialsProvider) LoadCredentials(ctx context.Context) (*githubcredentials.GithubCredentials, error) {
	if p.SecretRef == nil {
		return &githubcredentials.GithubCredentials{}, nil
	}
	ref := p.SecretRef(ctx)
	if ref.VersionID == 0 || p.Secrets == nil {
		return &githubcredentials.GithubCredentials{}, nil
	}
	token, err := p.Secrets.RevealByID(ref.VersionID)
	if err != nil {
		return nil, err
	}
	meta, _ := p.Secrets.MetaByID(ref.VersionID)
	return &githubcredentials.GithubCredentials{Token: string(token), ChangedAt: meta.CreatedAt}, nil
}
