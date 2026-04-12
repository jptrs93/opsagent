package versionprovider

import (
	"context"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// Provider lists the available scopes (e.g. branches) and versions
// (e.g. commits, release tags) for a deployment's prepare config.
type Provider interface {
	ListScopes(ctx context.Context, cfg *apigen.PrepareConfig) ([]string, error)
	ListVersions(ctx context.Context, cfg *apigen.PrepareConfig, scope string) ([]*apigen.Version, error)
}

// ForConfig returns the Provider that matches the given prepare config.
func ForConfig(cfg *apigen.PrepareConfig) (Provider, error) {
	switch {
	case cfg.NixBuild != nil:
		return Git, nil
	case cfg.GithubRelease != nil:
		return GHRel, nil
	}
	return nil, fmt.Errorf("no version provider for config")
}

// Package-level instances, wired by the process bootstrap.
var (
	Git   *GitVersionProvider
	GHRel *GithubReleaseVersionProvider
)
