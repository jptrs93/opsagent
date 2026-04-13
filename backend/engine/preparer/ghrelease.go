package preparer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

// GithubReleaseDownloader fetches a prebuilt artifact from a GitHub release
// and installs it in a predictable location. The target version is a release
// tag name (not a commit hash).
type GithubReleaseDownloader struct {
	dataDir     string
	githubToken string
	sem         chan struct{}
}

func NewGithubReleaseDownloader(dataDir string, githubToken string) *GithubReleaseDownloader {
	return &GithubReleaseDownloader{
		dataDir:     dataDir,
		githubToken: githubToken,
		sem:         make(chan struct{}, 1),
	}
}

func (g *GithubReleaseDownloader) start(parentCtx context.Context, store storage.OperatorStore, dep *apigen.DeploymentConfig) Preparer {
	ctx, cancel := context.WithCancel(parentCtx)
	p := &activePreparer{cancel: cancel, done: make(chan struct{}), deploymentConfigVersion: dep.Version}

	version := desiredVersion(dep)
	if version == "" {
		cancel()
		writePrepareStatus(ctx, store, dep, "", apigen.PreparationStatus_FAILED)
		close(p.done)
		return p
	}

	go func() {
		defer close(p.done)
		select {
		case g.sem <- struct{}{}:
			defer func() { <-g.sem }()
		case <-ctx.Done():
			writePrepareStatus(ctx, store, dep, "", apigen.PreparationStatus_FAILED)
			return
		}
		artifact, status := g.runDownload(ctx, store, dep, version)
		writePrepareStatus(ctx, store, dep, artifact, status)
	}()

	return p
}

func (g *GithubReleaseDownloader) runDownload(ctx context.Context, store storage.OperatorStore, dep *apigen.DeploymentConfig, version string) (string, apigen.PreparationStatus) {
	logPath := dep.PrepareOutputPath()
	slog.InfoContext(ctx, "github release download starting", "log_path", logPath)
	writePrepareStatus(ctx, store, dep, "", apigen.PreparationStatus_DOWNLOADING)

	logFile, err := os.Create(logPath)
	if err != nil {
		slog.ErrorContext(ctx, "creating prepare log file failed", "path", logPath, "err", err)
		return "", apigen.PreparationStatus_FAILED
	}
	defer logFile.Close()

	writeLog := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		slog.InfoContext(ctx, msg)
		fmt.Fprintf(logFile, "==> %s\n", msg)
	}

	gh := dep.Spec.Prepare.GithubRelease
	ownerRepo, err := RepoOwnerName(gh.Repo)
	if err != nil {
		writeLog("ERROR parsing repo: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}

	tag := version
	writeLog("fetching release %s tag %s", ownerRepo, tag)
	release, err := g.fetchReleaseByTag(ctx, ownerRepo, tag)
	if err != nil {
		writeLog("ERROR fetching release: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	if len(release.Assets) == 0 {
		writeLog("ERROR release %s has no assets", tag)
		return "", apigen.PreparationStatus_FAILED
	}

	asset := pickAsset(release.Assets, gh.Asset)
	if asset == nil {
		writeLog("ERROR asset %q not found in release %s; available: %v", gh.Asset, tag, assetNames(release.Assets))
		return "", apigen.PreparationStatus_FAILED
	}
	writeLog("selected asset %s (%d bytes)", asset.Name, asset.Size)

	dstDir := filepath.Join(g.dataDir, "releases", ownerRepo, tag)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		writeLog("ERROR creating release dir: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	dstPath := filepath.Join(dstDir, asset.Name)

	if info, err := os.Stat(dstPath); err == nil && info.Size() == asset.Size {
		writeLog("asset already present at %s, skipping download", dstPath)
	} else {
		writeLog("downloading asset to %s", dstPath)
		if err := g.downloadAsset(ctx, asset.URL, dstPath); err != nil {
			writeLog("ERROR download failed: %v", err)
			return "", apigen.PreparationStatus_FAILED
		}
	}
	if err := os.Chmod(dstPath, 0o755); err != nil {
		writeLog("ERROR chmod failed: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}

	writeLog("download complete, artifact: %s", dstPath)
	return dstPath, apigen.PreparationStatus_READY
}

// --- github api helpers ---

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	PublishedAt time.Time `json:"published_at"`
	Author      ghUser    `json:"author"`
	Assets      []ghAsset `json:"assets"`
}

type ghUser struct {
	Login string `json:"login"`
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

var releaseNotFoundErr = errors.New("release not found")

func (g *GithubReleaseDownloader) fetchReleaseByTag(ctx context.Context, ownerRepo, tag string) (*ghRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/%s", ownerRepo, tag)
	var r ghRelease
	if err := g.doGithubJSON(ctx, url, "application/vnd.github+json", &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (g *GithubReleaseDownloader) fetchReleases(ctx context.Context, ownerRepo string, limit int) ([]ghRelease, error) {
	if limit <= 0 {
		limit = 30
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=%d", ownerRepo, limit)
	var rs []ghRelease
	if err := g.doGithubJSON(ctx, url, "application/vnd.github+json", &rs); err != nil {
		return nil, err
	}
	return rs, nil
}

func (g *GithubReleaseDownloader) doGithubJSON(ctx context.Context, url, accept string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", accept)
	if g.githubToken != "" {
		req.Header.Set("Authorization", "Bearer "+g.githubToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("github api: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return releaseNotFoundErr
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github api %d: %s", resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// downloadAsset fetches a release asset via the GitHub API. The API returns a
// 302 redirect to the actual asset bytes when Accept is octet-stream; the
// default http client follows redirects but strips auth headers, which breaks
// private-repo downloads. We handle the redirect manually and only keep the
// Authorization header for api.github.com.
func (g *GithubReleaseDownloader) downloadAsset(ctx context.Context, assetAPIURL, dstPath string) error {
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequestWithContext(ctx, "GET", assetAPIURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/octet-stream")
	if g.githubToken != "" {
		req.Header.Set("Authorization", "Bearer "+g.githubToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	// Follow redirects manually (if any) without re-sending the auth header.
	for redirects := 0; resp.StatusCode >= 300 && resp.StatusCode < 400 && redirects < 5; redirects++ {
		loc := resp.Header.Get("Location")
		resp.Body.Close()
		if loc == "" {
			return fmt.Errorf("redirect without location")
		}
		req, err = http.NewRequestWithContext(ctx, "GET", loc, nil)
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

func pickAsset(assets []ghAsset, requested string) *ghAsset {
	if requested == "" {
		return &assets[0]
	}
	for i := range assets {
		if assets[i].Name == requested {
			return &assets[i]
		}
	}
	return nil
}

func assetNames(assets []ghAsset) []string {
	names := make([]string, len(assets))
	for i, a := range assets {
		names[i] = a.Name
	}
	return names
}
