// Package githubrelease downloads executable artifacts from GitHub releases.
package githubrelease

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/preparerlog"
	"github.com/jptrs93/opsagent/backend/lib/repo/github"
	"github.com/jptrs93/opsagent/backend/lib/repo/githubcredentials"
)

// Preparer fetches a prebuilt artifact from a GitHub release and installs it
// in a predictable location. The target version is a release tag name.
type Preparer struct {
	releasesDir string
	client      *github.Client
	credentials githubcredentials.Provider
}

func New(releasesDir string, client *github.Client, credentials githubcredentials.Provider) *Preparer {
	return &Preparer{
		releasesDir: filepath.Clean(releasesDir),
		client:      client,
		credentials: credentials,
	}
}

func (p *Preparer) Prepare(ctx context.Context, dep *apigen.DeploymentConfig, log *preparerlog.Log) (string, apigen.PreparationStatus) {
	version := dep.DesiredState.Version
	logPath := dep.PrepareOutputPath()
	slog.InfoContext(ctx, "github release download starting", "log_path", logPath)
	gh := dep.Spec.Prepare.GithubRelease
	ownerRepo, err := github.RepoOwnerName(gh.Repo)
	if err != nil {
		log.Error("parsing repository: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	if script := strings.TrimSpace(gh.DownloadScript); script != "" {
		return p.runDownloadScript(ctx, *gh, ownerRepo, version, script, log)
	}

	assetPath, err := p.downloadReleaseAsset(ctx, gh.Repo, gh.Asset, version, log)
	if err != nil {
		log.Error("downloading release asset: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	log.Write("download complete: %s", assetPath)
	return assetPath, apigen.PreparationStatus_READY
}

func (p *Preparer) downloadReleaseAsset(ctx context.Context, repo, assetName, version string, log *preparerlog.Log) (string, error) {
	ownerRepo, err := github.RepoOwnerName(repo)
	if err != nil {
		return "", fmt.Errorf("parsing repo: %w", err)
	}

	log.Write("fetching release %s tag %s", ownerRepo, version)
	release, err := p.client.ReleaseByTag(ctx, repo, version)
	if err != nil {
		return "", fmt.Errorf("fetching release: %w", err)
	}
	if len(release.Assets) == 0 {
		return "", fmt.Errorf("release %s has no assets", version)
	}

	asset := pickAsset(release.Assets, assetName)
	if asset == nil {
		return "", fmt.Errorf("asset %q not found in release %s; available: %v", assetName, version, assetNames(release.Assets))
	}
	log.Write("selected asset %s (%d bytes)", asset.Name, asset.Size)

	dstDir, err := p.prepareReleaseDir(ownerRepo, version)
	if err != nil {
		return "", fmt.Errorf("creating release dir: %w", err)
	}
	dstPath := filepath.Join(dstDir, asset.Name)

	if info, err := os.Stat(dstPath); err == nil && info.Size() == asset.Size && info.Mode().Perm()&0o111 != 0 {
		log.Write("using cached asset %s", dstPath)
	} else {
		log.Write("downloading asset to %s", dstPath)
		if err := p.client.DownloadAsset(ctx, asset.URL, dstPath); err != nil {
			return "", fmt.Errorf("download failed: %w", err)
		}
		if err := os.Chmod(dstPath, 0o755); err != nil {
			return "", fmt.Errorf("chmod failed: %w", err)
		}
	}
	return dstPath, nil
}

// runDownloadScript runs a user-supplied bash script in place of downloading a
// release asset. The target tag is $1 and GitHub credentials are provided only
// through the environment so the token is not written to the prepare log.
func (p *Preparer) runDownloadScript(ctx context.Context, gh apigen.GithubReleaseConfig, ownerRepo, tag, script string, log *preparerlog.Log) (string, apigen.PreparationStatus) {
	dstDir, err := p.prepareReleaseDir(ownerRepo, tag)
	if err != nil {
		log.Error("creating release directory: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}

	scriptFile, err := os.CreateTemp("", "opendeploy-download-*.sh")
	if err != nil {
		log.Error("creating download script: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	scriptPath := scriptFile.Name()
	defer os.Remove(scriptPath)
	if _, writeErr := scriptFile.WriteString(script); writeErr != nil {
		scriptFile.Close()
		log.Error("writing download script: %v", writeErr)
		return "", apigen.PreparationStatus_FAILED
	}
	if closeErr := scriptFile.Close(); closeErr != nil {
		log.Error("closing download script: %v", closeErr)
		return "", apigen.PreparationStatus_FAILED
	}

	log.Write("running custom download script in %s with version %s", dstDir, tag)
	cmd := exec.CommandContext(ctx, "bash", scriptPath, tag)
	cmd.Dir = dstDir
	cmd.Stdout = log.Output()
	cmd.Stderr = log.Output()
	cmd.Env = os.Environ()
	creds, err := p.credentials.LoadCredentials(ctx)
	if err != nil {
		log.Error("loading GitHub credentials: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	if creds.Token != "" {
		cmd.Env = append(cmd.Env, "GITHUB_TOKEN="+creds.Token)
	}
	if runErr := cmd.Run(); runErr != nil {
		log.Error("running download script: %v", runErr)
		return "", apigen.PreparationStatus_FAILED
	}

	artifact, err := resolveScriptArtifact(dstDir, gh.Asset)
	if err != nil {
		log.Error("locating download script output: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	if err := os.Chmod(artifact, 0o755); err != nil {
		log.Error("making downloaded artifact executable: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}

	log.Write("download script complete: %s", artifact)
	return artifact, apigen.PreparationStatus_READY
}

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
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		files = append(files, filepath.Join(dstDir, entry.Name()))
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
func (p *Preparer) prepareReleaseDir(ownerRepo, tag string) (string, error) {
	dstDir := filepath.Join(p.releasesDir, ownerRepo, tag)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return "", err
	}
	for dir := dstDir; ; dir = filepath.Dir(dir) {
		if err := ensureReleaseDirMode(dir); err != nil {
			return "", err
		}
		if dir == p.releasesDir {
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
	if info.Mode().Perm()&0o755 == 0o755 {
		return nil
	}
	return os.Chmod(dir, 0o755)
}

func pickAsset(assets []github.Asset, requested string) *github.Asset {
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

func assetNames(assets []github.Asset) []string {
	names := make([]string, len(assets))
	for i, asset := range assets {
		names[i] = asset.Name
	}
	return names
}
