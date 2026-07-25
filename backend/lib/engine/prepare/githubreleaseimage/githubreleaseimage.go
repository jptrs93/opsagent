// Package githubreleaseimage prepares OpenDeploy's internal netproxy image
// from an opendeploy binary published in a GitHub release.
package githubreleaseimage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/preparerlog"
	"github.com/jptrs93/opsagent/backend/lib/repo/github"
)

// Preparer downloads an OpenDeploy release binary and imports it as the
// internal opendeploy-net container image.
type Preparer struct {
	releasesDir string
	github      *github.Client
}

func New(releasesDir string, githubClient *github.Client) *Preparer {
	return &Preparer{
		releasesDir: filepath.Clean(releasesDir),
		github:      githubClient,
	}
}

func (p *Preparer) Prepare(ctx context.Context, dep *apigen.DeploymentConfig, log *preparerlog.Log) (string, apigen.PreparationStatus) {
	version := dep.WorkloadVersion()
	if strings.TrimSpace(version) == "" {
		log.Error("opendeploy-net requires an explicit desired version")
		return "", apigen.PreparationStatus_FAILED
	}

	assetPath, err := p.downloadReleaseAsset(ctx, version, log)
	if err != nil {
		log.Error("downloading OpenDeploy release binary: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	binary, err := os.ReadFile(assetPath)
	if err != nil {
		log.Error("reading OpenDeploy release binary: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}

	ref := internaldeploy.NetproxyImage + ":" + imageTag(version)
	reader, err := opendeployBinaryOCI(ref, binary)
	if err != nil {
		log.Error("building opendeploy-net image: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	resolved, err := ctrd.Default.Import(ctx, ctrd.ImageStream{Reader: reader, Ref: ref})
	if err != nil {
		log.Error("importing opendeploy-net image: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	log.Write("imported opendeploy-net image %s from %s", resolved, assetPath)
	return resolved, apigen.PreparationStatus_READY
}

func (p *Preparer) downloadReleaseAsset(ctx context.Context, version string, log *preparerlog.Log) (string, error) {
	ownerRepo, err := github.RepoOwnerName(internaldeploy.Repo)
	if err != nil {
		return "", fmt.Errorf("parsing repo: %w", err)
	}
	log.Write("fetching release %s tag %s", ownerRepo, version)
	release, err := p.github.ReleaseByTag(ctx, internaldeploy.Repo, version)
	if err != nil {
		return "", fmt.Errorf("fetching release: %w", err)
	}
	assetName := "opendeploy-linux-" + runtime.GOARCH
	asset := pickAsset(release.Assets)
	if asset == nil {
		return "", fmt.Errorf("asset %q not found in release %s; available: %v", assetName, version, assetNames(release.Assets))
	}
	log.Write("selected asset %s (%d bytes)", asset.Name, asset.Size)

	dstDir := filepath.Join(p.releasesDir, ownerRepo, version)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return "", fmt.Errorf("creating release dir: %w", err)
	}
	dstPath := filepath.Join(dstDir, asset.Name)
	unlock := p.github.LockAssetDownload()
	defer unlock()
	if info, err := os.Stat(dstPath); err == nil && info.Size() == asset.Size && info.Mode().Perm()&0o111 != 0 {
		log.Write("using cached asset %s", dstPath)
		return dstPath, nil
	}

	log.Write("downloading asset to %s", dstPath)
	if err := p.github.DownloadAsset(ctx, asset.URL, dstPath); err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	if err := os.Chmod(dstPath, 0o755); err != nil {
		return "", fmt.Errorf("chmod failed: %w", err)
	}
	return dstPath, nil
}

func pickAsset(assets []github.Asset) *github.Asset {
	name := "opendeploy-linux-" + runtime.GOARCH
	for i := range assets {
		if assets[i].Name == name {
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

func imageTag(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range version {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}
