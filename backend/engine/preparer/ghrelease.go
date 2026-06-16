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
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/repo/githubcredentials"
	"github.com/jptrs93/opsagent/backend/storage"
)

// GithubReleaseDownloader fetches a prebuilt artifact from a GitHub release
// and installs it in a predictable location. The target version is a release
// tag name (not a commit hash).
type GithubReleaseDownloader struct {
	dataDir     string
	credentials githubcredentials.Provider
	sem         chan struct{}
}

func NewGithubReleaseDownloader(dataDir string, provider githubcredentials.Provider) *GithubReleaseDownloader {
	return &GithubReleaseDownloader{
		dataDir:     dataDir,
		credentials: provider,
		sem:         make(chan struct{}, 1),
	}
}

func (g *GithubReleaseDownloader) start(store storage.OperatorStore, dep *apigen.DeploymentConfig) Preparer {
	ctx, cancel := context.WithCancel(context.Background())
	p := &activePreparer{cancel: cancel, done: make(chan struct{}), deploymentConfigVersion: dep.Version}

	version := desiredVersion(dep)
	if version == "" {
		cancel()
		writePrepareStatus(store, dep, "", apigen.PreparationStatus_FAILED)
		close(p.done)
		return p
	}

	go func() {
		defer close(p.done)
		select {
		case g.sem <- struct{}{}:
			defer func() { <-g.sem }()
		case <-ctx.Done():
			writePrepareStatus(store, dep, "", apigen.PreparationStatus_FAILED)
			return
		}
		artifact, status := g.runDownload(ctx, store, dep, version)
		writePrepareStatus(store, dep, artifact, status)
	}()

	return p
}

func (g *GithubReleaseDownloader) runDownload(ctx context.Context, store storage.OperatorStore, dep *apigen.DeploymentConfig, version string) (string, apigen.PreparationStatus) {
	logPath := dep.PrepareOutputPath()
	slog.InfoContext(ctx, "github release download starting", "log_path", logPath)
	writePrepareStatus(store, dep, "", apigen.PreparationStatus_DOWNLOADING)

	logFile, logPath, err := createPrepareLog(dep)
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
	if err := EnsureRuntimeInputsReady(ctx, dep); err != nil {
		writeLog("ERROR preparing runtime inputs: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}

	gh := dep.Spec.Prepare.GithubRelease
	ownerRepo, err := RepoOwnerName(gh.Repo)
	if err != nil {
		writeLog("ERROR parsing repo: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}

	tag := version

	if script := strings.TrimSpace(gh.DownloadScript); script != "" {
		return g.runDownloadScript(ctx, gh, ownerRepo, tag, script, logFile, writeLog)
	}

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

	dstDir, err := g.prepareReleaseDir(ownerRepo, tag)
	if err != nil {
		writeLog("ERROR creating release dir: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	dstPath := filepath.Join(dstDir, asset.Name)

	if info, err := os.Stat(dstPath); err == nil && info.Size() == asset.Size && info.Mode().Perm()&0o111 != 0 {
		writeLog("asset already present at %s, skipping download", dstPath)
	} else {
		writeLog("downloading asset to %s", dstPath)
		if err := g.downloadAsset(ctx, asset.URL, dstPath); err != nil {
			writeLog("ERROR download failed: %v", err)
			return "", apigen.PreparationStatus_FAILED
		}
		if err := os.Chmod(dstPath, 0o755); err != nil {
			writeLog("ERROR chmod failed: %v", err)
			return "", apigen.PreparationStatus_FAILED
		}
	}

	writeLog("download complete, artifact: %s", dstPath)
	return dstPath, apigen.PreparationStatus_READY
}

// runDownloadScript runs a user-supplied bash script in place of downloading a
// release asset. The script is executed with the target version tag as $1 and a
// working directory of the release download dir; it is responsible for placing
// the runnable artifact there. The configured GitHub token is exposed as
// GITHUB_TOKEN so private downloads work; it is passed via the environment (not
// command args) so it is never written to the prepare log.
func (g *GithubReleaseDownloader) runDownloadScript(ctx context.Context, gh apigen.GithubReleaseConfig, ownerRepo, tag, script string, logFile *os.File, writeLog func(string, ...any)) (string, apigen.PreparationStatus) {
	dstDir, err := g.prepareReleaseDir(ownerRepo, tag)
	if err != nil {
		writeLog("ERROR creating release dir: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}

	scriptFile, err := os.CreateTemp("", "opendeploy-download-*.sh")
	if err != nil {
		writeLog("ERROR creating script file: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	scriptPath := scriptFile.Name()
	defer os.Remove(scriptPath)
	if _, err := scriptFile.WriteString(script); err != nil {
		scriptFile.Close()
		writeLog("ERROR writing script file: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	if err := scriptFile.Close(); err != nil {
		writeLog("ERROR closing script file: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}

	writeLog("running custom download script in %s with version %s", dstDir, tag)
	cmd := exec.CommandContext(ctx, "bash", scriptPath, tag)
	cmd.Dir = dstDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = os.Environ()
	creds, err := g.credentials.LoadCredentials(ctx)
	if err != nil {
		writeLog("ERROR loading GitHub credentials: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	if creds.Token != "" {
		cmd.Env = append(cmd.Env, "GITHUB_TOKEN="+creds.Token)
	}
	if err := cmd.Run(); err != nil {
		writeLog("ERROR download script failed: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}

	artifact, err := resolveScriptArtifact(dstDir, gh.Asset)
	if err != nil {
		writeLog("ERROR locating script output: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	if err := os.Chmod(artifact, 0o755); err != nil {
		writeLog("ERROR chmod failed: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}

	writeLog("download script complete, artifact: %s", artifact)
	return artifact, apigen.PreparationStatus_READY
}

// resolveScriptArtifact locates the executable a custom download script left in
// dstDir. When asset is set it must name the produced file; otherwise the script
// must leave exactly one regular file.
func resolveScriptArtifact(dstDir, asset string) (string, error) {
	if asset != "" {
		if filepath.Base(asset) != asset {
			return "", fmt.Errorf("asset must be a file name: %q", asset)
		}
		candidate := filepath.Join(dstDir, asset)
		info, err := os.Stat(candidate)
		if err != nil {
			return "", fmt.Errorf("asset %q not found in download dir: %w", asset, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("asset %q is a directory", asset)
		}
		return candidate, nil
	}

	entries, err := os.ReadDir(dstDir)
	if err != nil {
		return "", fmt.Errorf("reading download dir: %w", err)
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		files = append(files, filepath.Join(dstDir, e.Name()))
	}
	if len(files) == 0 {
		return "", fmt.Errorf("download script produced no files in %s", dstDir)
	}
	if len(files) > 1 {
		return "", fmt.Errorf("download script produced multiple files in %s; set asset to the output file name", dstDir)
	}
	return files[0], nil
}

// prepareReleaseDir creates the per-release download dir and keeps every new
// component traversable so non-owner runAs users can execute the artifact.
func (g *GithubReleaseDownloader) prepareReleaseDir(ownerRepo, tag string) (string, error) {
	base := ainit.StaticConfig.ReleasesDir
	dstDir := filepath.Join(base, ownerRepo, tag)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return "", err
	}
	for dir := dstDir; ; dir = filepath.Dir(dir) {
		if err := ensureReleaseDirMode(dir); err != nil {
			return "", err
		}
		if dir == base {
			break
		}
	}
	return dstDir, nil
}

func ensureReleaseDirMode(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	// Existing release dirs may be root-owned from installer-driven upgrades.
	// If they already have the required 0755 bits, avoid an unnecessary chmod
	// that the unprivileged daemon cannot perform.
	if info.Mode().Perm()&0o755 == 0o755 {
		return nil
	}
	return os.Chmod(dir, 0o755)
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
	creds, err := g.credentials.LoadCredentials(ctx)
	if err != nil {
		return err
	}
	if creds.Token != "" {
		req.Header.Set("Authorization", "Bearer "+creds.Token)
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
	creds, err := g.credentials.LoadCredentials(ctx)
	if err != nil {
		return err
	}
	if creds.Token != "" {
		req.Header.Set("Authorization", "Bearer "+creds.Token)
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
		preferred := "opendeploy-linux-" + runtime.GOARCH
		for i := range assets {
			if assets[i].Name == preferred {
				return &assets[i]
			}
		}
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
