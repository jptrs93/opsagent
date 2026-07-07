package preparer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	ctrd2 "github.com/jptrs93/opsagent/backend/lib/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/lib/engine/imageref"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage"
)

// ContainerImagePuller pulls a container image into containerd's content store
// and unpacks it into the snapshotter, so the container runner can create a
// container from it immediately. The resolved artifact is the image ref as
// stored by containerd (the same ref the runner uses to look it up).
//
// Phase 1 pulls anonymously — no registry credentials.
type ContainerImagePuller struct {
	client   *ctrd2.Client
	releases *GithubReleaseDownloader
	sem      chan struct{} // capacity 1: one pull/import at a time
}

func NewContainerImagePuller(client *ctrd2.Client, releases *GithubReleaseDownloader) *ContainerImagePuller {
	return &ContainerImagePuller{client: client, releases: releases, sem: make(chan struct{}, 1)}
}

func (p *ContainerImagePuller) start(store storage.OperatorStore, dep *apigen.DeploymentConfig) Preparer {
	ctx, cancel := context.WithCancel(context.Background())
	ap := &activePreparer{cancel: cancel, done: make(chan struct{}), deploymentConfigVersion: dep.Version}

	version := desiredVersion(dep)
	if version == "" {
		cancel()
		writePrepareStatus(store, dep, "", apigen.PreparationStatus_FAILED)
		close(ap.done)
		return ap
	}

	go func() {
		defer close(ap.done)
		select {
		case p.sem <- struct{}{}:
			defer func() { <-p.sem }()
		case <-ctx.Done():
			writePrepareStatus(store, dep, "", apigen.PreparationStatus_FAILED)
			return
		}
		artifact, status := p.runPull(ctx, store, dep, version)
		writePrepareStatus(store, dep, artifact, status)
	}()

	return ap
}

func (p *ContainerImagePuller) runPull(ctx context.Context, store storage.OperatorStore, dep *apigen.DeploymentConfig, version string) (string, apigen.PreparationStatus) {
	logPath := dep.PrepareOutputPath()
	if dep.Spec.Prepare.ContainerImage.Image == internaldeploy.DataplaneImage {
		return p.importDataplane(ctx, store, dep, version)
	}
	ref, err := imageref.Ref(dep.Spec.Prepare.ContainerImage.Image, version)
	if err != nil {
		slog.ErrorContext(ctx, "container image ref invalid", "image", dep.Spec.Prepare.ContainerImage.Image, "err", err)
		return "", apigen.PreparationStatus_FAILED
	}
	slog.InfoContext(ctx, "image pull starting", "ref", ref, "log_path", logPath)

	logFile, logPath, err := createPrepareLog(dep)
	if err != nil {
		slog.ErrorContext(ctx, "creating prepare log file failed", "path", logPath, "err", err)
		return "", apigen.PreparationStatus_FAILED
	}
	defer logFile.Close()
	fmt.Fprintf(logFile, "Pulling image %s\n", ref)

	writePrepareStatus(store, dep, "", apigen.PreparationStatus_PULLING)

	if !p.client.Supported() {
		fmt.Fprintf(logFile, "containers are not supported on this platform (linux + containerd required)\n")
		return "", apigen.PreparationStatus_FAILED
	}
	if err := EnsureRuntimeInputsReady(ctx, dep); err != nil {
		fmt.Fprintf(logFile, "Runtime input preparation failed: %v\n", err)
		return "", apigen.PreparationStatus_FAILED
	}

	resolved, err := p.client.Pull(ctx, ref)
	if err != nil {
		slog.ErrorContext(ctx, "image pull failed", "ref", ref, "err", err)
		fmt.Fprintf(logFile, "Pull failed: %v\n", err)
		return "", apigen.PreparationStatus_FAILED
	}
	fmt.Fprintf(logFile, "Pulled %s\n", resolved)
	return resolved, apigen.PreparationStatus_READY
}

func (p *ContainerImagePuller) importDataplane(ctx context.Context, store storage.OperatorStore, dep *apigen.DeploymentConfig, version string) (string, apigen.PreparationStatus) {
	logFile, _, err := createPrepareLog(dep)
	if err != nil {
		slog.ErrorContext(ctx, "creating prepare log file failed", "err", err)
		return "", apigen.PreparationStatus_FAILED
	}
	defer logFile.Close()

	writePrepareStatus(store, dep, "", apigen.PreparationStatus_PREPARING)
	if !p.client.Supported() {
		fmt.Fprintf(logFile, "containers are not supported on this platform (linux + containerd required)\n")
		return "", apigen.PreparationStatus_FAILED
	}
	if p.releases == nil {
		fmt.Fprintf(logFile, "release downloader is not configured\n")
		return "", apigen.PreparationStatus_FAILED
	}
	if strings.TrimSpace(version) == "" {
		fmt.Fprintf(logFile, "opendeploy-net requires an explicit desired version\n")
		return "", apigen.PreparationStatus_FAILED
	}

	writeLog := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		slog.InfoContext(ctx, msg)
		fmt.Fprintf(logFile, "==> %s\n", msg)
	}
	assetName := "opendeploy-linux-" + runtime.GOARCH
	assetPath, err := p.releases.downloadReleaseAsset(ctx, internaldeploy.Repo, assetName, version, writeLog)
	if err != nil {
		fmt.Fprintf(logFile, "Download OpenDeploy release binary failed: %v\n", err)
		return "", apigen.PreparationStatus_FAILED
	}
	binary, err := os.ReadFile(assetPath)
	if err != nil {
		fmt.Fprintf(logFile, "Reading OpenDeploy release binary failed: %v\n", err)
		return "", apigen.PreparationStatus_FAILED
	}

	ref := internaldeploy.DataplaneImage + ":" + imageTag(version)
	reader, err := opendeployBinaryOCI(ref, binary)
	if err != nil {
		fmt.Fprintf(logFile, "Building opendeploy-net image failed: %v\n", err)
		return "", apigen.PreparationStatus_FAILED
	}
	resolved, err := p.client.Import(ctx, ctrd2.ImageStream{Reader: reader, Ref: ref})
	if err != nil {
		fmt.Fprintf(logFile, "Import opendeploy-net image failed: %v\n", err)
		return "", apigen.PreparationStatus_FAILED
	}
	fmt.Fprintf(logFile, "Imported opendeploy-net image %s from %s\n", resolved, assetPath)
	return resolved, apigen.PreparationStatus_READY
}

func imageTag(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range version {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
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
