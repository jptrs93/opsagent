package webuihandler

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/versionprovider"
	gitrepo "github.com/jptrs93/opsagent/backend/lib/repo/git"
)

var RepoRequiredErr = apigen.NewApiErr("Repository is required", "missing_repo", http.StatusBadRequest)
var ImageRequiredErr = apigen.NewApiErr("Image is required", "missing_image", http.StatusBadRequest)
var InvalidSourceTypeErr = apigen.NewApiErr("Invalid source type", "invalid_source_type", http.StatusBadRequest)
var InvalidValidateRequestErr = apigen.NewApiErr("Invalid validate request", "invalid_validate_request", http.StatusBadRequest)

func (h *Handler) PostV1ReposValidate(ctx apigen.Context, req *apigen.RepoValidateRequest) (*apigen.RepoValidateResponse, error) {
	if !h.canCreateDeploymentSomewhere(ctx) {
		return nil, AccessDeniedErr
	}
	if countValidationSources(req) != 1 {
		return nil, InvalidSourceTypeErr
	}

	switch {
	case req.NixDockerBuild != nil:
		if err := validateNixDockerBuildValidateRequest(req.NixDockerBuild); err != nil {
			return nil, err
		}
		return h.validateNixDockerBuildSource(ctx, req.NixDockerBuild)
	case req.ContainerImage != nil:
		if err := validateContainerImageValidateRequest(req.ContainerImage); err != nil {
			return nil, err
		}
		return h.validateContainerImageSource(ctx, req.ContainerImage)
	default:
		return nil, apigen.NewApiErr("Bad request.", "bad_request", http.StatusBadRequest)
	}
}

func (h *Handler) validateNixDockerBuildSource(ctx apigen.Context, src *apigen.ValidateNixDockerBuildSource) (*apigen.RepoValidateResponse, error) {
	repo := src.RepoUrl
	if h.GitVersions == nil {
		return &apigen.RepoValidateResponse{NixDockerBuild: &apigen.ValidateNixDockerBuildSourceResponse{GitRepository: validationErr("Git validation is not configured.")}}, nil
	}

	res := apigen.ValidateNixDockerBuildSourceResponse{}
	selectedBranch := src.SelectedBranch
	needBranchListing := src.CheckBranch || src.RefreshAvailableBranches || (src.CheckRepo && !src.CheckCommit && !src.CheckFlakePath)
	if needBranchListing {
		branches, err := h.GitVersions.ListBranches(ctx, repo)
		if err != nil {
			slog.WarnContext(ctx, "listing git branches", "err", err)
			errMessage := fmt.Sprintf("Failed checking repository branches: %v", err)
			res.AvailableBranches = apigen.AvailableBranches{Loaded: true, Errormessage: &errMessage}
			if src.CheckRepo {
				res.CheckedRepoUrl = repo
				res.GitRepository = validationErr(errMessage)
			}
			if src.CheckBranch {
				res.CheckedBranch = selectedBranch
				res.BranchCheck = validationErr(errMessage)
			}
		} else {
			res.AvailableBranches = apigen.AvailableBranches{Loaded: true, Branches: branches}
			if src.CheckRepo {
				res.CheckedRepoUrl = repo
				res.GitRepository = validationOK("Repo accessible.")
			}
			if src.CheckBranch {
				res.CheckedBranch = selectedBranch
				if slices.Contains(branches, selectedBranch) {
					res.BranchCheck = validationOK("Branch exists.")
				} else {
					res.BranchCheck = validationErr(fmt.Sprintf("Branch '%v' doesn't exist.", selectedBranch))
				}
			}
		}
	}

	if src.RefreshAvailableCommits {
		commits, err := h.GitVersions.ListCommits(ctx, repo, selectedBranch, 25)
		if err != nil {
			slog.WarnContext(ctx, fmt.Sprintf("listing git repo commits branch=%s", selectedBranch), "err", err)
			errMessage := fmt.Sprintf("Failed listing branch '%v' commits: %v", selectedBranch, err)
			res.AvailableCommits = apigen.AvailableCommits{Loaded: true, Branch: selectedBranch, Errormessage: &errMessage}
		} else {
			res.AvailableCommits = apigen.AvailableCommits{Loaded: true, Branch: selectedBranch, Commits: commits}
		}
	}

	commitID := selectedCommitID(src.SelectedCommit)
	if src.CheckFlakePath && commitID == "" {
		res.CheckedFlakePath = src.SelectedFlakePath
		if selectedBranch != "" {
			commits, err := h.GitVersions.ListCommits(ctx, repo, selectedBranch, 1)
			if err != nil {
				slog.WarnContext(ctx, fmt.Sprintf("resolving selected branch head branch=%s", selectedBranch), "err", err)
				res.NixFlakeFile = validationErr(fmt.Sprintf("Failed resolving branch '%v' head: %v", selectedBranch, err))
			} else if len(commits) == 0 || commits[0] == nil {
				res.NixFlakeFile = validationErr(fmt.Sprintf("No commits found for branch '%v'.", selectedBranch))
			} else {
				commitID = commits[0].ID
			}
		} else {
			resolvedCommit, _, err := h.GitVersions.DefaultCommit(ctx, repo)
			if err != nil {
				slog.WarnContext(ctx, "resolving git remote HEAD", "err", err)
				res.NixFlakeFile = validationErr(fmt.Sprintf("Failed resolving the repository default commit: %v", err))
			} else {
				commitID = resolvedCommit
			}
		}
	}

	if src.CheckCommit && src.CheckFlakePath {
		res.CheckedRepoUrl = repo
		res.CheckedCommit = src.SelectedCommit
		res.CheckedFlakePath = src.SelectedFlakePath
		commitValid, err := h.GitVersions.ValidateNixSource(ctx, repo, commitID, src.SelectedFlakePath)
		if err != nil {
			slog.WarnContext(ctx, fmt.Sprintf("validating exact Nix source commit=%s", commitID), "err", err)
			if src.CheckRepo {
				if commitValid {
					res.GitRepository = validationOK("Repo accessible.")
				} else {
					res.GitRepository = validationErr(fmt.Sprintf("Could not verify repository access: %v", err))
				}
			}
			if commitValid {
				res.CommitCheck = validationOK("Commit exists.")
			} else {
				res.CommitCheck = validationErr(fmt.Sprintf("Could not validate commit '%v': %v", commitID, err))
			}
			res.NixFlakeFile = validationErr(fmt.Sprintf("Could not validate flake path '%v' at commit '%v': %v", src.SelectedFlakePath, commitID, err))
		} else {
			if src.CheckRepo {
				res.GitRepository = validationOK("Repo accessible.")
			}
			res.CommitCheck = validationOK("Commit exists.")
			res.NixFlakeFile = validationOK(fmt.Sprintf("Flake path '%v' is a regular file.", src.SelectedFlakePath))
		}
	} else if src.CheckCommit {
		res.CheckedRepoUrl = repo
		res.CheckedCommit = src.SelectedCommit
		if err := h.GitVersions.ValidateCommit(ctx, repo, commitID); err != nil {
			slog.WarnContext(ctx, fmt.Sprintf("validating exact git commit commit=%s", commitID), "err", err)
			if src.CheckRepo {
				res.GitRepository = validationErr(fmt.Sprintf("Could not verify repository access: %v", err))
			}
			res.CommitCheck = validationErr(fmt.Sprintf("Could not validate commit '%v': %v", commitID, err))
		} else {
			if src.CheckRepo {
				res.GitRepository = validationOK("Repo accessible.")
			}
			res.CommitCheck = validationOK("Commit exists.")
		}
	} else if src.CheckFlakePath && !res.NixFlakeFile.Checked {
		res.CheckedRepoUrl = repo
		res.CheckedFlakePath = src.SelectedFlakePath
		commitValid, err := h.GitVersions.ValidateNixSource(ctx, repo, commitID, src.SelectedFlakePath)
		if err != nil {
			slog.WarnContext(ctx, fmt.Sprintf("validating exact Nix source commit=%s", commitID), "err", err)
			if src.CheckRepo {
				if commitValid {
					res.GitRepository = validationOK("Repo accessible.")
				} else {
					res.GitRepository = validationErr(fmt.Sprintf("Could not verify repository access: %v", err))
				}
			}
			res.NixFlakeFile = validationErr(fmt.Sprintf("Could not validate flake path '%v' at commit '%v': %v", src.SelectedFlakePath, commitID, err))
		} else {
			if src.CheckRepo {
				res.GitRepository = validationOK("Repo accessible.")
			}
			res.NixFlakeFile = validationOK(fmt.Sprintf("Flake path '%v' is a regular file.", src.SelectedFlakePath))
		}
	}

	return &apigen.RepoValidateResponse{NixDockerBuild: &res}, nil
}

func validateNixDockerBuildValidateRequest(src *apigen.ValidateNixDockerBuildSource) error {
	if src.RepoUrl == "" {
		return RepoRequiredErr
	}
	if hasTrimmedWhitespace(src.RepoUrl) || hasTrimmedWhitespace(src.SelectedBranch) || hasTrimmedWhitespace(src.SelectedFlakePath) || hasTrimmedWhitespace(selectedCommitID(src.SelectedCommit)) {
		return apigen.NewApiErr("Validate request fields must not have leading or trailing whitespace", "invalid_validate_request_whitespace", http.StatusBadRequest)
	}
	if !src.RefreshAvailableBranches && !src.RefreshAvailableCommits && !src.CheckRepo && !src.CheckBranch && !src.CheckCommit && !src.CheckFlakePath {
		return InvalidValidateRequestErr
	}
	if src.RefreshAvailableCommits && src.SelectedBranch == "" {
		return apigen.NewApiErr("Selected branch is required to refresh commits", "missing_selected_branch", http.StatusBadRequest)
	}
	if src.CheckBranch && src.SelectedBranch == "" {
		return apigen.NewApiErr("Selected branch is required", "missing_selected_branch", http.StatusBadRequest)
	}
	if src.CheckCommit && selectedCommitID(src.SelectedCommit) == "" {
		return apigen.NewApiErr("Selected commit is required", "missing_selected_commit", http.StatusBadRequest)
	}
	if src.CheckFlakePath && src.SelectedFlakePath == "" {
		return apigen.NewApiErr("Selected flake path is required", "missing_selected_flake_path", http.StatusBadRequest)
	}
	if src.CheckFlakePath {
		if _, err := gitrepo.CleanFlakePath(src.SelectedFlakePath); err != nil {
			return apigen.NewApiErr(fmt.Sprintf("Invalid selected flake path: %v", err), "invalid_selected_flake_path", http.StatusBadRequest)
		}
	}
	return nil
}

func (h *Handler) validateContainerImageSource(ctx apigen.Context, src *apigen.ValidateContainerImageSource) (*apigen.RepoValidateResponse, error) {
	image := src.Image
	tags, err := versionprovider.ListContainerImageTags(ctx, image)
	if err != nil {
		slog.WarnContext(ctx, fmt.Sprintf("container image validation failed image=%s", image), "err", err)
		return &apigen.RepoValidateResponse{ContainerImage: &apigen.ValidateContainerImageSourceResponse{Image: validationErr("Image not accessible: " + containerImageRef(image))}}, nil
	}
	res := apigen.ValidateContainerImageSourceResponse{Image: validationOK("Image accessible: " + containerImageRef(image))}
	if src.RefreshVersions {
		res.Tags = tags
	}
	return &apigen.RepoValidateResponse{ContainerImage: &res}, nil
}

func validateContainerImageValidateRequest(src *apigen.ValidateContainerImageSource) error {
	if src.Image == "" {
		return ImageRequiredErr
	}
	if hasTrimmedWhitespace(src.Image) {
		return apigen.NewApiErr("Validate request fields must not have leading or trailing whitespace", "invalid_validate_request_whitespace", http.StatusBadRequest)
	}
	if !src.RefreshVersions {
		return InvalidValidateRequestErr
	}
	return nil
}

func validationOK(message string) apigen.ValidationResult {
	return apigen.ValidationResult{Checked: true, Ok: true, Message: message}
}

func validationErr(message string) apigen.ValidationResult {
	return apigen.ValidationResult{Checked: true, Ok: false, Message: message}
}

func selectedCommitID(commit *apigen.Version) string {
	if commit == nil {
		return ""
	}
	return commit.ID
}

func hasTrimmedWhitespace(s string) bool {
	return s != strings.TrimSpace(s)
}

func containerImageRef(image string) string {
	repo, err := versionprovider.ContainerImageRepositoryRef(image)
	if err != nil {
		return image
	}
	return repo
}

func countValidationSources(req *apigen.RepoValidateRequest) int {
	count := 0
	if req.NixDockerBuild != nil {
		count++
	}
	if req.ContainerImage != nil {
		count++
	}
	return count
}
