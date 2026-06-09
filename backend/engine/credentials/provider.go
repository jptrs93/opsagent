package credentials

import "context"

type GithubCredentials struct {
	Token string
}

type GithubCredentialsProvider interface {
	GithubCredentials(ctx context.Context) (GithubCredentials, error)
}

type StaticGithubCredentialsProvider struct {
	Token string
}

func (p StaticGithubCredentialsProvider) GithubCredentials(context.Context) (GithubCredentials, error) {
	return GithubCredentials{Token: p.Token}, nil
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
