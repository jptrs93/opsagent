package githubcredentials

import (
	"context"
	"time"
)

type Provider interface {
	LoadCredentials(ctx context.Context) (*GithubCredentials, error)
}

type GithubCredentials struct {
	Token     string
	ChangedAt time.Time
}
