// Package containerimage pulls registry images into OpenDeploy's containerd
// image store.
package containerimage

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/lib/engine/imageref"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/preparerlog"
)

// Prepare pulls and unpacks a registry image for immediate use by the
// container runner.
func Prepare(ctx context.Context, dep *apigen.Deployment, log *preparerlog.Log) (string, apigen.ImageStatus) {
	container := dep.Spec.Container()
	version := dep.WorkloadVersion()
	logPath := dep.PrepareOutputPath()
	ref, err := imageref.Ref(container.Source.RemoteImage.Image, version)
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("container image ref %q invalid", container.Source.RemoteImage.Image), "err", err)
		return "", apigen.ImageStatus_IMAGE_FAILED
	}
	slog.InfoContext(ctx, fmt.Sprintf("image pull of %s starting, logging to %s", ref, logPath))

	log.Write("pulling image %s", ref)

	resolved, err := ctrd.Default.Pull(ctx, ref)
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("image pull of %s failed", ref), "err", err)
		log.Error("pulling image: %v", err)
		return "", apigen.ImageStatus_IMAGE_FAILED
	}
	log.Write("pulled image %s", resolved)
	return resolved, apigen.ImageStatus_IMAGE_READY
}
