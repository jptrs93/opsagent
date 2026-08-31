// Package opendeployrelease prepares the two opendeploy system deployments
// from an opendeploy binary published in a GitHub release: the host binary for
// the self deployment and the opendeploy-net container image.
package opendeployrelease

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/preparerlog"
	"github.com/jptrs93/opsagent/backend/lib/repo/github"
)

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

func (p *Preparer) PrepareBinary(ctx context.Context, dep *apigen.Deployment, log *preparerlog.Log) (string, apigen.ImageStatus) {
	assetPath, err := p.downloadReleaseBinary(ctx, dep.WorkloadVersion(), log)
	if err != nil {
		log.Error("downloading release asset: %v", err)
		return "", apigen.ImageStatus_IMAGE_FAILED
	}
	log.Write("download complete: %s", assetPath)
	return assetPath, apigen.ImageStatus_IMAGE_READY
}

func (p *Preparer) PrepareImage(ctx context.Context, dep *apigen.Deployment, log *preparerlog.Log) (string, apigen.ImageStatus) {
	version := dep.WorkloadVersion()
	assetPath, err := p.downloadReleaseBinary(ctx, version, log)
	if err != nil {
		log.Error("downloading OpenDeploy release binary: %v", err)
		return "", apigen.ImageStatus_IMAGE_FAILED
	}
	binary, err := os.ReadFile(assetPath)
	if err != nil {
		log.Error("reading OpenDeploy release binary: %v", err)
		return "", apigen.ImageStatus_IMAGE_FAILED
	}

	ref := internaldeploy.NetproxyImage + ":" + imageTag(version)
	reader, err := opendeployBinaryOCI(ref, binary)
	if err != nil {
		log.Error("building opendeploy-net image: %v", err)
		return "", apigen.ImageStatus_IMAGE_FAILED
	}
	resolved, err := ctrd.Default.Import(ctx, ctrd.ImageStream{Reader: reader, Ref: ref})
	if err != nil {
		log.Error("importing opendeploy-net image: %v", err)
		return "", apigen.ImageStatus_IMAGE_FAILED
	}
	log.Write("imported opendeploy-net image %s from %s", resolved, assetPath)
	return resolved, apigen.ImageStatus_IMAGE_READY
}

func (p *Preparer) downloadReleaseBinary(ctx context.Context, version string, log *preparerlog.Log) (string, error) {
	ownerRepo, err := github.RepoOwnerName(internaldeploy.Repo)
	if err != nil {
		return "", fmt.Errorf("parsing repo: %w", err)
	}

	log.Write("fetching release %s tag %s", ownerRepo, version)
	release, err := p.client.ReleaseByTag(ctx, internaldeploy.Repo, version)
	if err != nil {
		return "", fmt.Errorf("fetching release: %w", err)
	}

	asset := findAsset(release.Assets)
	if asset == nil {
		return "", fmt.Errorf("asset %q not found in release %s; available: %v", internaldeploy.ReleaseAsset, version, assetNames(release.Assets))
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
		return dstPath, nil
	}

	log.Write("downloading asset to %s", dstPath)
	if err := p.client.DownloadAsset(ctx, asset.URL, dstPath); err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	if err := os.Chmod(dstPath, 0o755); err != nil {
		return "", fmt.Errorf("chmod failed: %w", err)
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

func findAsset(assets []github.Asset) *github.Asset {
	for i := range assets {
		if assets[i].Name == internaldeploy.ReleaseAsset {
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
