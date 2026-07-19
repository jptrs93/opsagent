package versionprovider

import (
	"context"
	"fmt"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	gitrepo "github.com/jptrs93/opsagent/backend/lib/repo/git"
)

// GitVersionProvider lists branches and commits for repos that use Nix Docker
// image preparation.
type GitVersionProvider struct {
	git *gitrepo.Manager
}

func NewGitVersionProvider(git *gitrepo.Manager) *GitVersionProvider {
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

func (p *GitVersionProvider) DefaultCommit(ctx context.Context, repo string) (string, string, error) {
	if repo == "" {
		return "", "", fmt.Errorf("git repo missing")
	}
	info, err := p.git.FetchRepoInfo(ctx, repo)
	if err != nil {
		return "", "", err
	}
	if info.LatestCommit == "" {
		return "", info.Branch, fmt.Errorf("remote repository HEAD has no commit")
	}
	return info.LatestCommit, info.Branch, nil
}

func (p *GitVersionProvider) ValidateCommit(ctx context.Context, repo string, commit string) error {
	return p.git.ValidateExactCommit(ctx, repo, commit)
}

func (p *GitVersionProvider) ValidateNixSource(ctx context.Context, repo string, commit string, flakePath string) (bool, error) {
	return p.git.ValidateExactNixSource(ctx, repo, commit, flakePath)
}

func commitSubject(msg string) string {
	if idx := strings.IndexByte(msg, '\n'); idx >= 0 {
		return msg[:idx]
	}
	return msg
}
