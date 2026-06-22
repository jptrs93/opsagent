package versionprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine/preparer"
	"github.com/jptrs93/opsagent/backend/localtest"
	"github.com/jptrs93/opsagent/backend/repo/githubcredentials"
)

// GithubReleaseVersionProvider lists release tags from the GitHub API.
type GithubReleaseVersionProvider struct {
	credentials githubcredentials.Provider
}

func NewGithubReleaseVersionProvider(provider githubcredentials.Provider) *GithubReleaseVersionProvider {
	return &GithubReleaseVersionProvider{credentials: provider}
}

func (p *GithubReleaseVersionProvider) ListReleases(ctx context.Context, repo string) ([]*apigen.Version, error) {
	if repo == "" {
		return nil, fmt.Errorf("github release repo missing")
	}
	ownerRepo, err := preparer.RepoOwnerName(repo)
	if err != nil {
		return nil, err
	}
	releases, err := p.fetchReleases(ctx, ownerRepo, 50)
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

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	PublishedAt time.Time `json:"published_at"`
	Author      struct {
		Login string `json:"login"`
	} `json:"author"`
}

func (p *GithubReleaseVersionProvider) fetchReleases(ctx context.Context, ownerRepo string, limit int) ([]ghRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=%d", ownerRepo, limit)
	if localtest.Enabled() {
		url = localtest.APIURL(fmt.Sprintf("/repos/%s/releases?per_page=%d", ownerRepo, limit))
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	creds, err := p.credentials.LoadCredentials(ctx)
	if err != nil {
		return nil, err
	}
	if creds.Token != "" {
		req.Header.Set("Authorization", "Bearer "+creds.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github api: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("github api %d: %s", resp.StatusCode, string(body))
	}
	var rs []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rs); err != nil {
		return nil, err
	}
	return rs, nil
}
