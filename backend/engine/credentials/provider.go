package credentials

import (
	"context"

	"github.com/jptrs93/opsagent/backend/util/secretu"
)

type GithubCredentials struct {
	Token string
}

type GithubCredentialsProvider interface {
	GithubCredentials(ctx context.Context) (GithubCredentials, error)
}

type StaticGithubCredentialsProvider struct {
	Token secretu.SecretValue
}

func (p StaticGithubCredentialsProvider) GithubCredentials(context.Context) (GithubCredentials, error) {
	if p.Token == nil {
		return GithubCredentials{}, nil
	}
	token, err := p.Token.Reveal()
	if err != nil {
		return GithubCredentials{}, err
	}
	return GithubCredentials{Token: token}, nil
}

type EmptyGithubCredentialsProvider struct{}

func (EmptyGithubCredentialsProvider) GithubCredentials(context.Context) (GithubCredentials, error) {
	return GithubCredentials{}, nil
}

func OrEmpty(provider GithubCredentialsProvider) GithubCredentialsProvider {
	if provider == nil {
		return EmptyGithubCredentialsProvider{}
	}
	return provider
}
