package preparer

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/storage"
)

// ContainerImagePuller pulls a container image into containerd's content store
// and unpacks it into the snapshotter, so the container runner can create a
// container from it immediately. The resolved artifact is the image ref as
// stored by containerd (the same ref the runner uses to look it up).
//
// Phase 1 pulls anonymously — no registry credentials.
type ContainerImagePuller struct {
	client *ctrd.Client
	sem    chan struct{} // capacity 1: one pull at a time
}

func NewContainerImagePuller(client *ctrd.Client) *ContainerImagePuller {
	return &ContainerImagePuller{client: client, sem: make(chan struct{}, 1)}
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
	ref := imageRef(dep.Spec.Prepare.ContainerImage.Image, version)
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

// imageRef joins the configured image repository with the desired version. A
// version that looks like a digest is appended with '@'; otherwise it is treated
// as a ':tag'.
func imageRef(image, version string) string {
	image = stripImageTagOrDigest(image)
	if strings.HasPrefix(version, "sha256:") {
		return image + "@" + version
	}
	return image + ":" + version
}

func stripImageTagOrDigest(image string) string {
	if idx := strings.IndexByte(image, '@'); idx >= 0 {
		return image[:idx]
	}
	lastSlash := strings.LastIndexByte(image, '/')
	lastColon := strings.LastIndexByte(image, ':')
	if lastColon > lastSlash {
		return image[:lastColon]
	}
	return image
}
