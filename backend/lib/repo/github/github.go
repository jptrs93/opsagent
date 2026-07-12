package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/lib/repo/githubcredentials"
)

const defaultAPIBaseURL = "https://api.github.com"

var ErrReleaseNotFound = errors.New("release not found")

type Client struct {
	credentials githubcredentials.Provider
	apiBaseURL  string
}

type Option func(*Client)

// WithAPIBaseURL overrides the GitHub API endpoint, primarily for tests.
func WithAPIBaseURL(baseURL string) Option {
	return func(c *Client) {
		c.apiBaseURL = strings.TrimRight(baseURL, "/")
	}
}

func NewClient(provider githubcredentials.Provider, options ...Option) *Client {
	c := &Client{
		credentials: provider,
		apiBaseURL:  defaultAPIBaseURL,
	}
	for _, option := range options {
		option(c)
	}
	return c
}

type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	PublishedAt time.Time `json:"published_at"`
	Author      User      `json:"author"`
	Assets      []Asset   `json:"assets"`
}

type User struct {
	Login string `json:"login"`
}

type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

// RepoOwnerName extracts "owner/repo" from a URL like "github.com/owner/repo".
func RepoOwnerName(repoURL string) (string, error) {
	repoURL = strings.TrimPrefix(repoURL, "https://")
	repoURL = strings.TrimPrefix(repoURL, "http://")
	repoURL = strings.TrimSuffix(repoURL, ".git")
	parts := strings.SplitN(repoURL, "/", 2)
	if len(parts) < 2 || parts[1] == "" {
		return "", fmt.Errorf("cannot parse owner/repo from %q", repoURL)
	}
	return parts[1], nil
}

func (c *Client) ListReleases(ctx context.Context, repo string, limit int) ([]Release, error) {
	ownerRepo, err := RepoOwnerName(repo)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 30
	}
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=%d", c.apiBaseURL, ownerRepo, limit)
	var releases []Release
	if err := c.getJSON(ctx, url, &releases, false); err != nil {
		return nil, err
	}
	return releases, nil
}

func (c *Client) ReleaseByTag(ctx context.Context, repo, tag string) (*Release, error) {
	ownerRepo, err := RepoOwnerName(repo)
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/repos/%s/releases/tags/%s", c.apiBaseURL, ownerRepo, tag)
	var release Release
	if err := c.getJSON(ctx, url, &release, true); err != nil {
		return nil, err
	}
	return &release, nil
}

func (c *Client) getJSON(ctx context.Context, url string, out any, releaseByTag bool) error {
	req, err := c.authenticatedRequest(ctx, url, "application/vnd.github+json")
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("github api: %w", err)
	}
	defer resp.Body.Close()
	if releaseByTag && resp.StatusCode == http.StatusNotFound {
		return ErrReleaseNotFound
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github api %d: %s", resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) authenticatedRequest(ctx context.Context, url, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	creds, err := c.credentials.LoadCredentials(ctx)
	if err != nil {
		return nil, err
	}
	if creds.Token != "" {
		req.Header.Set("Authorization", "Bearer "+creds.Token)
	}
	return req, nil
}

// DownloadAsset follows API redirects without forwarding GitHub credentials to
// the asset host, and atomically replaces dstPath after a successful download.
func (c *Client) DownloadAsset(ctx context.Context, assetAPIURL, dstPath string) error {
	client := *http.DefaultClient
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	req, err := c.authenticatedRequest(ctx, assetAPIURL, "application/octet-stream")
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	for redirects := 0; resp.StatusCode >= 300 && resp.StatusCode < 400 && redirects < 5; redirects++ {
		location := resp.Header.Get("Location")
		resp.Body.Close()
		if location == "" {
			return fmt.Errorf("redirect without location")
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
		if err != nil {
			return err
		}
		resp, err = client.Do(req)
		if err != nil {
			return err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("asset download %d: %s", resp.StatusCode, string(body))
	}

	tmp, err := os.CreateTemp(filepath.Dir(dstPath), filepath.Base(dstPath)+".new-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}
