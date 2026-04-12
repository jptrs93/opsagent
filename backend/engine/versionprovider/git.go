package versionprovider

import (
	"context"
	"fmt"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine/preparer"
)

// GitVersionProvider lists branches and commits for repos that use nix-build
// style preparation (versions are git commit hashes).
type GitVersionProvider struct {
	git *preparer.GitManagerImpl
}

func NewGitVersionProvider(git *preparer.GitManagerImpl) *GitVersionProvider {
	return &GitVersionProvider{git: git}
}

func (p *GitVersionProvider) ListScopes(_ context.Context, cfg *apigen.PrepareConfig) ([]string, error) {
	if cfg == nil || cfg.NixBuild == nil {
		return nil, fmt.Errorf("nixBuild config missing")
	}
	return p.git.ListBranches(cfg.NixBuild.Repo)
}

func (p *GitVersionProvider) ListVersions(_ context.Context, cfg *apigen.PrepareConfig, scope string) ([]*apigen.Version, error) {
	if cfg == nil || cfg.NixBuild == nil {
		return nil, fmt.Errorf("nixBuild config missing")
	}
	commits, err := p.git.GetCommitLog(cfg.NixBuild.Repo, scope, 25)
	if err != nil {
		return nil, err
	}
	out := make([]*apigen.Version, 0, len(commits))
	for _, c := range commits {
		out = append(out, &apigen.Version{
			ID:     c.Hash,
			Label:  commitSubject(c.Message),
			Author: c.Author,
			Time:   c.Time,
		})
	}
	return out, nil
}

func commitSubject(msg string) string {
	if idx := strings.IndexByte(msg, '\n'); idx >= 0 {
		return msg[:idx]
	}
	return msg
}
