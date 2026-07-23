// Package containerimage pulls registry images into OpenDeploy's containerd
// image store.
package containerimage

import (
	"context"
	"log/slog"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/lib/engine/imageref"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/preparerlog"
)

// Prepare pulls and unpacks a registry image for immediate use by the
// container runner.
func Prepare(ctx context.Context, dep *apigen.DeploymentConfig, log *preparerlog.Log) (string, apigen.PreparationStatus) {
	container := dep.Spec.Container()
	version := dep.WorkloadVersion()
	logPath := dep.PrepareOutputPath()
	ref, err := imageref.Ref(container.Source.RemoteImage.Image, version)
	if err != nil {
		slog.ErrorContext(ctx, "container image ref invalid", "image", container.Source.RemoteImage.Image, "err", err)
		return "", apigen.PreparationStatus_FAILED
	}
	slog.InfoContext(ctx, "image pull starting", "ref", ref, "log_path", logPath)

	log.Write("pulling image %s", ref)

	resolved, err := ctrd.Default.Pull(ctx, ref)
	if err != nil {
		slog.ErrorContext(ctx, "image pull failed", "ref", ref, "err", err)
		log.Error("pulling image: %v", err)
		return "", apigen.PreparationStatus_FAILED
	}
	log.Write("pulled image %s", resolved)
	return resolved, apigen.PreparationStatus_READY
}
