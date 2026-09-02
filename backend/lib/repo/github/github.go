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
	"sync"
	"time"
)

const defaultAPIBaseURL = "https://api.github.com"

var ErrReleaseNotFound = errors.New("release not found")

// Client reads GitHub release data for the public OpenDeploy release
// repository, authenticating when a token source is configured. Only the
// primary configures one — it moves these calls off the unauthenticated
// per-IP rate limit; workers download release assets unauthenticated.
type Client struct {
	apiBaseURL         string
	tokenSource        func(context.Context) string
	assetDownloadMutex sync.Mutex
}

type Option func(*Client)

// WithAPIBaseURL overrides the GitHub API endpoint, primarily for tests.
func WithAPIBaseURL(baseURL string) Option {
	return func(c *Client) {
		c.apiBaseURL = strings.TrimRight(baseURL, "/")
	}
}

// WithTokenSource supplies a bearer token per request. An empty return sends
// the request unauthenticated, so an unconfigured or unreadable token degrades
// to the public rate limit rather than failing the call.
func WithTokenSource(source func(context.Context) string) Option {
	return func(c *Client) {
		c.tokenSource = source
	}
}

func NewClient(options ...Option) *Client {
	c := &Client{
		apiBaseURL: defaultAPIBaseURL,
	}
	for _, option := range options {
		option(c)
	}
	return c
}

// LockAssetDownload serializes release asset cache checks and downloads.
func (c *Client) LockAssetDownload() func() {
	c.assetDownloadMutex.Lock()
	return c.assetDownloadMutex.Unlock
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
	req, err := c.newRequest(ctx, url, "application/vnd.github+json")
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

func (c *Client) newRequest(ctx context.Context, url, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	if c.tokenSource != nil {
		if token := c.tokenSource(ctx); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	return req, nil
}

// DownloadAsset atomically replaces dstPath after a successful download.
func (c *Client) DownloadAsset(ctx context.Context, assetAPIURL, dstPath string) error {
	req, err := c.newRequest(ctx, assetAPIURL, "application/octet-stream")
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
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
