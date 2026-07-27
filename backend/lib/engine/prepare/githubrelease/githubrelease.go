// Package githubrelease downloads executable artifacts from GitHub releases.
package githubrelease

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/preparerlog"
	"github.com/jptrs93/opsagent/backend/lib/repo/github"
)

// Preparer fetches a prebuilt artifact from a GitHub release and installs it
// in a predictable location. The target version is a release tag name.
type Preparer struct {
	releasesDir string
	client      *github.Client
}

func New(releasesDir string, client *github.Client) *Preparer {
	return &Preparer{
		releasesDir: filepath.Clean(releasesDir),
		client:      client,
	}
}

func (p *Preparer) Prepare(ctx context.Context, dep *apigen.DeploymentConfig, log *preparerlog.Log) (string, apigen.ImageStatus) {
	version := dep.WorkloadVersion()
	logPath := dep.PrepareOutputPath()
	slog.InfoContext(ctx, "github release download starting", "log_path", logPath)
	gh := dep.Spec.SystemdSpec.Source
	assetPath, err := p.downloadReleaseAsset(ctx, gh.Repo, gh.Asset, version, log)
	if err != nil {
		log.Error("downloading release asset: %v", err)
		return "", apigen.ImageStatus_IMAGE_FAILED
	}
	log.Write("download complete: %s", assetPath)
	return assetPath, apigen.ImageStatus_IMAGE_READY
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
	unlock := p.client.LockAssetDownload()
	defer unlock()

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
