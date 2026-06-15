package githubcredentials

import (
	"context"
	"time"

	"github.com/jptrs93/opsagent/backend/util/secretu"
)

type Provider interface {
	LoadCredentials(ctx context.Context) (*GithubCredentials, error)
}

type GithubCredentials struct {
	Token     string
	ChangedAt time.Time
}

type SecretProvider struct {
	Value func(context.Context) secretu.SecretValue
}

func (p SecretProvider) LoadCredentials(ctx context.Context) (*GithubCredentials, error) {
	if p.Value == nil {
		return &GithubCredentials{}, nil
	}
	value := p.Value(ctx)
	if value == nil {
		return &GithubCredentials{}, nil
	}
	token, err := value.Reveal()
	if err != nil {
		return nil, err
	}
	return &GithubCredentials{Token: token, ChangedAt: value.Updated()}, nil
}
