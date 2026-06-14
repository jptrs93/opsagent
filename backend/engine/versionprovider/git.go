package versionprovider

import (
	"context"
	"fmt"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine/preparer"
)

// GitVersionProvider lists branches and commits for repos that use Nix Docker
// image preparation (versions are git commit hashes).
type GitVersionProvider struct {
	git *preparer.GitManagerImpl
}

func NewGitVersionProvider(git *preparer.GitManagerImpl) *GitVersionProvider {
	return &GitVersionProvider{git: git}
}

func (p *GitVersionProvider) ListScopes(ctx context.Context, cfg *apigen.PrepareConfig) ([]string, error) {
	repo := gitRepo(cfg)
	if repo == "" {
		return nil, fmt.Errorf("git prepare config missing")
	}
	return p.git.ListBranches(ctx, repo)
}

func (p *GitVersionProvider) ListVersions(ctx context.Context, cfg *apigen.PrepareConfig, scope string) ([]*apigen.Version, error) {
	repo := gitRepo(cfg)
	if repo == "" {
		return nil, fmt.Errorf("git prepare config missing")
	}
	commits, err := p.git.GetCommitLog(ctx, repo, scope, 25)
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

func (p *GitVersionProvider) CommitExists(ctx context.Context, repo string, commit string) (bool, error) {
	return p.git.CommitExists(ctx, repo, commit)
}

func (p *GitVersionProvider) PathExists(ctx context.Context, repo string, repoPath string, ref string) (bool, error) {
	return p.git.PathExists(ctx, repo, repoPath, ref)
}

func gitRepo(cfg *apigen.PrepareConfig) string {
	if cfg == nil {
		return ""
	}
	if !cfg.NixDockerBuild.IsZero() {
		return cfg.NixDockerBuild.Repo
	}
	return ""
}

func commitSubject(msg string) string {
	if idx := strings.IndexByte(msg, '\n'); idx >= 0 {
		return msg[:idx]
	}
	return msg
}
