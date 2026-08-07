package webuihandler

import (
	"context"
	"errors"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type fakeGitSourceProvider struct {
	branches          []string
	commits           []*apigen.Version
	defaultCommit     string
	defaultBranch     string
	defaultErr        error
	validateCommitErr error
	sourceCommitValid bool
	sourceErr         error

	listBranchesCalls     int
	listCommitsCalls      int
	discoverVersionsCalls int
	defaultCommitCalls    int
	validateCalls         []fakeNixSourceCall
	validateCommitIDs     []string
}

type fakeNixSourceCall struct {
	repo   string
	commit string
	flake  string
}

func (f *fakeGitSourceProvider) ListBranches(context.Context, string) ([]string, error) {
	f.listBranchesCalls++
	return f.branches, nil
}

func (f *fakeGitSourceProvider) ListCommits(context.Context, string, string, int) ([]*apigen.Version, error) {
	f.listCommitsCalls++
	return f.commits, nil
}

func (f *fakeGitSourceProvider) DiscoverVersions(_ context.Context, _ string, requestedBranch string, _ int) ([]string, string, []*apigen.Version, error) {
	f.discoverVersionsCalls++
	branch := requestedBranch
	if branch == "" {
		for _, candidate := range []string{"main", "master", "prod"} {
			for _, available := range f.branches {
				if available == candidate {
					branch = candidate
					break
				}
			}
			if branch != "" {
				break
			}
		}
		if branch == "" && len(f.branches) > 0 {
			branch = f.branches[0]
		}
	}
	return f.branches, branch, f.commits, nil
}

func (f *fakeGitSourceProvider) DefaultCommit(context.Context, string) (string, string, error) {
	f.defaultCommitCalls++
	return f.defaultCommit, f.defaultBranch, f.defaultErr
}

func (f *fakeGitSourceProvider) ValidateCommit(_ context.Context, _ string, commit string) error {
	f.validateCommitIDs = append(f.validateCommitIDs, commit)
	return f.validateCommitErr
}

func (f *fakeGitSourceProvider) ValidateNixSource(_ context.Context, repo, commit, flake string) (bool, error) {
	f.validateCalls = append(f.validateCalls, fakeNixSourceCall{repo: repo, commit: commit, flake: flake})
	return f.sourceCommitValid, f.sourceErr
}

func TestRepoValidateChecksExactCommitWithoutDiscoveryList(t *testing.T) {
	commit := "0123456789abcdef0123456789abcdef01234567"
	provider := &fakeGitSourceProvider{}
	h := &Handler{GitVersions: provider}
	res, err := h.PostV1ReposValidate(apigen.Context{Ctx: context.Background()}, &apigen.RepoValidateRequest{
		NixDockerBuild: &apigen.ValidateNixDockerBuildSource{
			RepoUrl:        "github.com/acme/app",
			SelectedCommit: &apigen.Version{ID: commit},
			CheckCommit:    true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.NixDockerBuild.CommitCheck.Ok {
		t.Fatalf("commit check = %+v, want success", res.NixDockerBuild.CommitCheck)
	}
	if provider.listCommitsCalls != 0 || len(provider.validateCommitIDs) != 1 || provider.validateCommitIDs[0] != commit {
		t.Fatalf("provider calls: list=%d exact=%v", provider.listCommitsCalls, provider.validateCommitIDs)
	}
}

func TestRepoValidateCombinesCommitAndFlakeValidation(t *testing.T) {
	commit := "0123456789abcdef0123456789abcdef01234567"
	provider := &fakeGitSourceProvider{sourceCommitValid: true}
	h := &Handler{GitVersions: provider}
	res, err := h.PostV1ReposValidate(apigen.Context{Ctx: context.Background()}, &apigen.RepoValidateRequest{
		NixDockerBuild: &apigen.ValidateNixDockerBuildSource{
			RepoUrl:           "github.com/acme/app",
			SelectedCommit:    &apigen.Version{ID: commit},
			SelectedFlakePath: "nix/app/flake.nix",
			CheckRepo:         true,
			CheckCommit:       true,
			CheckFlakePath:    true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.NixDockerBuild.GitRepository.Ok || !res.NixDockerBuild.CommitCheck.Ok || !res.NixDockerBuild.NixFlakeFile.Ok {
		t.Fatalf("validation response = %+v", res.NixDockerBuild)
	}
	if len(provider.validateCalls) != 1 || len(provider.validateCommitIDs) != 0 || provider.listBranchesCalls != 0 || provider.listCommitsCalls != 0 {
		t.Fatalf("provider calls: source=%v commit=%v branches=%d commits=%d", provider.validateCalls, provider.validateCommitIDs, provider.listBranchesCalls, provider.listCommitsCalls)
	}
}

func TestRepoValidateUsesRemoteHeadForDefaultFlakeCommit(t *testing.T) {
	commit := "0123456789abcdef0123456789abcdef01234567"
	provider := &fakeGitSourceProvider{defaultCommit: commit, defaultBranch: "trunk", sourceCommitValid: true}
	h := &Handler{GitVersions: provider}
	res, err := h.PostV1ReposValidate(apigen.Context{Ctx: context.Background()}, &apigen.RepoValidateRequest{
		NixDockerBuild: &apigen.ValidateNixDockerBuildSource{
			RepoUrl:           "github.com/acme/app",
			SelectedFlakePath: "flake.nix",
			CheckFlakePath:    true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.NixDockerBuild.NixFlakeFile.Ok {
		t.Fatalf("flake check = %+v", res.NixDockerBuild.NixFlakeFile)
	}
	if provider.defaultCommitCalls != 1 || provider.listBranchesCalls != 0 || provider.listCommitsCalls != 0 {
		t.Fatalf("default/list calls = %d/%d/%d", provider.defaultCommitCalls, provider.listBranchesCalls, provider.listCommitsCalls)
	}
	if len(provider.validateCalls) != 1 || provider.validateCalls[0].commit != commit {
		t.Fatalf("source calls = %+v", provider.validateCalls)
	}
}

func TestRepoValidateUsesSelectedBranchHeadForFlakeCommit(t *testing.T) {
	commit := "0123456789abcdef0123456789abcdef01234567"
	provider := &fakeGitSourceProvider{commits: []*apigen.Version{{ID: commit}}, sourceCommitValid: true}
	h := &Handler{GitVersions: provider}
	res, err := h.PostV1ReposValidate(apigen.Context{Ctx: context.Background()}, &apigen.RepoValidateRequest{
		NixDockerBuild: &apigen.ValidateNixDockerBuildSource{
			RepoUrl:           "github.com/acme/app",
			SelectedBranch:    "release",
			SelectedFlakePath: "flake.nix",
			CheckFlakePath:    true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.NixDockerBuild.NixFlakeFile.Ok {
		t.Fatalf("flake check = %+v", res.NixDockerBuild.NixFlakeFile)
	}
	if provider.defaultCommitCalls != 0 || provider.listCommitsCalls != 1 {
		t.Fatalf("default/commit calls = %d/%d", provider.defaultCommitCalls, provider.listCommitsCalls)
	}
	if len(provider.validateCalls) != 1 || provider.validateCalls[0].commit != commit {
		t.Fatalf("source calls = %+v", provider.validateCalls)
	}
}

func TestRepoValidateReturnsInteractiveFailuresInResponse(t *testing.T) {
	provider := &fakeGitSourceProvider{sourceCommitValid: true, sourceErr: errors.New("not a regular Git file")}
	h := &Handler{GitVersions: provider}
	res, err := h.PostV1ReposValidate(apigen.Context{Ctx: context.Background()}, &apigen.RepoValidateRequest{
		NixDockerBuild: &apigen.ValidateNixDockerBuildSource{
			RepoUrl:           "github.com/acme/app",
			SelectedCommit:    &apigen.Version{ID: "0123456789abcdef0123456789abcdef01234567"},
			SelectedFlakePath: "flake.nix",
			CheckCommit:       true,
			CheckFlakePath:    true,
		},
	})
	if err != nil {
		t.Fatalf("interactive failure returned HTTP error: %v", err)
	}
	if !res.NixDockerBuild.CommitCheck.Ok || res.NixDockerBuild.NixFlakeFile.Ok {
		t.Fatalf("validation response = %+v", res.NixDockerBuild)
	}
}

func TestRepoValidatePreservesMalformedRequestErrors(t *testing.T) {
	h := &Handler{GitVersions: &fakeGitSourceProvider{}}
	_, err := h.PostV1ReposValidate(apigen.Context{Ctx: context.Background()}, &apigen.RepoValidateRequest{
		NixDockerBuild: &apigen.ValidateNixDockerBuildSource{RepoUrl: "github.com/acme/app", CheckCommit: true},
	})
	var apiErr apigen.ApiErr
	if !errors.As(err, &apiErr) || apiErr.Code != 400 || apiErr.InternalErr != "missing_selected_commit" {
		t.Fatalf("malformed request error = %v", err)
	}
}

func TestRepoValidateRejectsInvalidFlakePathBeforeGit(t *testing.T) {
	provider := &fakeGitSourceProvider{}
	h := &Handler{GitVersions: provider}
	_, err := h.PostV1ReposValidate(apigen.Context{Ctx: context.Background()}, &apigen.RepoValidateRequest{
		NixDockerBuild: &apigen.ValidateNixDockerBuildSource{
			RepoUrl:           "github.com/acme/app",
			SelectedFlakePath: "../flake.nix",
			CheckFlakePath:    true,
		},
	})
	var apiErr apigen.ApiErr
	if !errors.As(err, &apiErr) || apiErr.InternalErr != "invalid_selected_flake_path" {
		t.Fatalf("invalid flake error = %v", err)
	}
	if provider.defaultCommitCalls != 0 || len(provider.validateCalls) != 0 {
		t.Fatalf("provider calls = default %d, validation %v", provider.defaultCommitCalls, provider.validateCalls)
	}
}
