package githubcredentials

import (
	"context"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
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
	MetaByID(id int32) (secrets.Meta, bool)
	RevealByID(id int32) ([]byte, error)
}

func (p SecretProvider) LoadCredentials(ctx context.Context) (*GithubCredentials, error) {
	if p.SecretRef == nil {
		return &GithubCredentials{}, nil
	}
	ref := p.SecretRef(ctx)
	if ref.ID == 0 || p.Secrets == nil {
		return &GithubCredentials{}, nil
	}
	token, err := p.Secrets.RevealByID(ref.ID)
	if err != nil {
		return nil, err
	}
	meta, _ := p.Secrets.MetaByID(ref.ID)
	changedAt := meta.CreatedAt
	return &GithubCredentials{Token: string(token), ChangedAt: changedAt}, nil
}
