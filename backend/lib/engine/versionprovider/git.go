package versionprovider

import (
	"context"
	"fmt"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/preparer"
)

// GitVersionProvider lists branches and commits for repos that use Nix Docker
// image preparation.
type GitVersionProvider struct {
	git *preparer.GitManager
}

func NewGitVersionProvider(git *preparer.GitManager) *GitVersionProvider {
	return &GitVersionProvider{git: git}
}

func (p *GitVersionProvider) ListBranches(ctx context.Context, repo string) ([]string, error) {
	if repo == "" {
		return nil, fmt.Errorf("git repo missing")
	}
	return p.git.ListBranches(ctx, repo)
}

func (p *GitVersionProvider) ListCommits(ctx context.Context, repo string, branch string, limit int) ([]*apigen.Version, error) {
	if repo == "" {
		return nil, fmt.Errorf("git repo missing")
	}
	if limit <= 0 {
		limit = 25
	}
	commits, err := p.git.GetCommitLog(ctx, repo, branch, limit)
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

func commitSubject(msg string) string {
	if idx := strings.IndexByte(msg, '\n'); idx >= 0 {
		return msg[:idx]
	}
	return msg
}
