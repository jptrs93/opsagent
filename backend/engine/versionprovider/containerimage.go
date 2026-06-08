package versionprovider

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// ContainerImageVersionProvider is a no-op provider for container image
// deployments. Phase 1 does not enumerate registry tags (that needs the
// registry API and credentials), so it returns no scopes and no versions — the
// user supplies the tag/digest directly when deploying.
type ContainerImageVersionProvider struct{}

func (ContainerImageVersionProvider) ListScopes(ctx context.Context, cfg *apigen.PrepareConfig) ([]string, error) {
	return nil, nil
}

func (ContainerImageVersionProvider) ListVersions(ctx context.Context, cfg *apigen.PrepareConfig, scope string) ([]*apigen.Version, error) {
	return nil, nil
}
