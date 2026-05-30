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
)

// GithubReleaseVersionProvider lists release tags from the GitHub API.
type GithubReleaseVersionProvider struct {
	githubToken string
}

func NewGithubReleaseVersionProvider(githubToken string) *GithubReleaseVersionProvider {
	return &GithubReleaseVersionProvider{githubToken: githubToken}
}

func (p *GithubReleaseVersionProvider) ListScopes(_ context.Context, _ *apigen.PrepareConfig) ([]string, error) {
	return nil, nil
}

func (p *GithubReleaseVersionProvider) ListVersions(ctx context.Context, cfg *apigen.PrepareConfig, _ string) ([]*apigen.Version, error) {
	if cfg == nil || cfg.GithubRelease.IsZero() {
		return nil, fmt.Errorf("githubRelease config missing")
	}
	ownerRepo, err := preparer.RepoOwnerName(cfg.GithubRelease.Repo)
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

// AssetExists reports whether an asset with the given name is attached to any
// of the repo's recent published releases.
func (p *GithubReleaseVersionProvider) AssetExists(ctx context.Context, cfg *apigen.PrepareConfig, assetName string) (bool, error) {
	if cfg == nil || cfg.GithubRelease.IsZero() {
		return false, fmt.Errorf("githubRelease config missing")
	}
	ownerRepo, err := preparer.RepoOwnerName(cfg.GithubRelease.Repo)
	if err != nil {
		return false, err
	}
	releases, err := p.fetchReleases(ctx, ownerRepo, 50)
	if err != nil {
		return false, err
	}
	for _, r := range releases {
		for _, a := range r.Assets {
			if a.Name == assetName {
				return true, nil
			}
		}
	}
	return false, nil
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	PublishedAt time.Time `json:"published_at"`
	Author      struct {
		Login string `json:"login"`
	} `json:"author"`
	Assets []struct {
		Name string `json:"name"`
	} `json:"assets"`
}

func (p *GithubReleaseVersionProvider) fetchReleases(ctx context.Context, ownerRepo string, limit int) ([]ghRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=%d", ownerRepo, limit)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if p.githubToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.githubToken)
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
