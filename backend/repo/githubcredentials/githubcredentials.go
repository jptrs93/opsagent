package githubcredentials

import (
	"context"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type Provider interface {
	LoadCredentials(ctx context.Context) (*GithubCredentials, error)
}

type GithubCredentials struct {
	Token     string
	ChangedAt time.Time
}

type SecretProvider struct {
	SecretRef func(context.Context) apigen.SecretRef
	Secrets   secretStore
}

type secretStore interface {
	HasSecret(name string) (bool, time.Time)
	Reveal(name string) ([]byte, error)
}

func (p SecretProvider) LoadCredentials(ctx context.Context) (*GithubCredentials, error) {
	if p.SecretRef == nil {
		return &GithubCredentials{}, nil
	}
	ref := p.SecretRef(ctx)
	if ref.Key == "" || p.Secrets == nil {
		return &GithubCredentials{}, nil
	}
	token, err := p.Secrets.Reveal(ref.Key)
	if err != nil {
		return nil, err
	}
	_, changedAt := p.Secrets.HasSecret(ref.Key)
	return &GithubCredentials{Token: string(token), ChangedAt: changedAt}, nil
}
