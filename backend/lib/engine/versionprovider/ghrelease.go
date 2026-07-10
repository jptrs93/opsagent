package versionprovider

import (
	"context"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
	githubrepo "github.com/jptrs93/opsagent/backend/lib/repo/github"
	"github.com/jptrs93/opsagent/backend/lib/repo/githubcredentials"
)

// GithubReleaseVersionProvider lists release tags from the GitHub API.
type GithubReleaseVersionProvider struct {
	client *githubrepo.Client
}

func NewGithubReleaseVersionProvider(provider githubcredentials.Provider) *GithubReleaseVersionProvider {
	return NewGithubReleaseVersionProviderWithClient(githubrepo.NewClient(nil, provider))
}

func NewGithubReleaseVersionProviderWithClient(client *githubrepo.Client) *GithubReleaseVersionProvider {
	return &GithubReleaseVersionProvider{client: client}
}

func (p *GithubReleaseVersionProvider) ListReleases(ctx context.Context, repo string) ([]*apigen.Version, error) {
	if repo == "" {
		return nil, fmt.Errorf("github release repo missing")
	}
	releases, err := p.client.ListReleases(ctx, repo, 50)
	if err != nil {
		return nil, err
	}
	out := make([]*apigen.Version, 0, len(releases))
	for _, r := range releases {
		label := r.Name
		if label == "" {
			label = r.TagName
		}
		out = append(out, &apigen.Version{
			ID:     r.TagName,
			Label:  label,
			Author: r.Author.Login,
			Time:   r.PublishedAt,
		})
	}
	return out, nil
}
