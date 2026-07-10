package webuihandler

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/versionprovider"
)

var RepoRequiredErr = apigen.NewApiErr("Repository is required", "missing_repo", http.StatusBadRequest)
var ImageRequiredErr = apigen.NewApiErr("Image is required", "missing_image", http.StatusBadRequest)
var InvalidSourceTypeErr = apigen.NewApiErr("Invalid source type", "invalid_source_type", http.StatusBadRequest)
var InvalidValidateRequestErr = apigen.NewApiErr("Invalid validate request", "invalid_validate_request", http.StatusBadRequest)

// PostV1RepoValidate checks that a deployment source is reachable and authorized.
func (h *Handler) PostV1RepoValidate(ctx apigen.Context, req *apigen.ValidateSourceRequest) (*apigen.ValidateSourceResponse, error) {
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

func (h *Handler) validateNixDockerBuildSource(ctx apigen.Context, src *apigen.ValidateNixDockerBuildSource) (*apigen.ValidateSourceResponse, error) {
	repo := src.RepoUrl
	if h.GitVersions == nil {
		return &apigen.ValidateSourceResponse{NixDockerBuild: &apigen.ValidateNixDockerBuildSourceResponse{GitRepository: validationErr("Git validation is not configured.")}}, nil
	}

	res := apigen.ValidateNixDockerBuildSourceResponse{}
	selectedBranch := src.SelectedBranch
	needToResolveDefaultCommit := src.CheckFlakePath && selectedCommitID(src.SelectedCommit) == ""
	commitsBranch := selectedBranch

	if src.CheckRepo || src.CheckBranch || src.RefreshAvailableBranches || needToResolveDefaultCommit {
		branches, err := h.GitVersions.ListBranches(ctx, repo)
		if err != nil {
			slog.Warn("listing git branches", "repo", repo, "err", err)
			errMessage := fmt.Sprintf("Failed checking repo '%v' branches: %v", repo, err)
			res.AvailableBranches = apigen.AvailableBranches{Loaded: true, Errormessage: &errMessage}
			if src.CheckRepo {
				res.CheckedRepoUrl = repo
				res.GitRepository = validationErr(errMessage)
			}
			if src.CheckBranch {
				res.CheckedBranch = selectedBranch
				res.BranchCheck = validationErr(errMessage)
			}
			if src.CheckCommit {
				res.CheckedCommit = src.SelectedCommit
				res.CommitCheck = validationErr(errMessage)
			}
			if src.CheckFlakePath {
				res.CheckedFlakePath = src.SelectedFlakePath
				res.NixFlakeFile = validationErr(errMessage)
			}
			return &apigen.ValidateSourceResponse{NixDockerBuild: &res}, nil
		}

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
		if commitsBranch == "" {
			commitsBranch = selectedValidationBranch(branches, "")
		}
	}

	defaultCommitID := ""
	if src.CheckCommit || src.RefreshAvailableCommits || needToResolveDefaultCommit {
		commits, err := h.GitVersions.ListCommits(ctx, repo, commitsBranch, 25)
		if err != nil {
			slog.Warn("listing git repo commits", "branch", commitsBranch, "repo", repo, "err", err)
			errMessage := fmt.Sprintf("Failed listing branch '%v' commits: %v", commitsBranch, err)
			if src.CheckCommit {
				res.CheckedCommit = src.SelectedCommit
				res.CommitCheck = validationErr(errMessage)
			}
			if src.CheckFlakePath && needToResolveDefaultCommit {
				res.CheckedFlakePath = src.SelectedFlakePath
				res.NixFlakeFile = validationErr(errMessage)
			}
			res.AvailableCommits = apigen.AvailableCommits{Loaded: true, Branch: commitsBranch, Errormessage: &errMessage}
		} else {
			res.AvailableCommits = apigen.AvailableCommits{Loaded: true, Branch: commitsBranch, Commits: commits}
			if src.CheckCommit {
				res.CheckedCommit = src.SelectedCommit
				commitID := selectedCommitID(src.SelectedCommit)
				if slices.ContainsFunc(commits, func(version *apigen.Version) bool { return version != nil && version.ID == commitID }) {
					res.CommitCheck = validationOK("Commit exists.")
				} else {
					slog.Warn(fmt.Sprintf("commit %v doesn't exist on branch %v", commitID, commitsBranch))
					res.CommitCheck = validationErr(fmt.Sprintf("Could not find commit '%v' in branch '%v'.", commitID, commitsBranch))
				}
			}
			if needToResolveDefaultCommit && len(commits) > 0 && commits[0] != nil {
				defaultCommitID = strings.TrimSpace(commits[0].ID)
			}
			// TODO: this only checks the first page of branch commits. If an older
			// selected commit should remain valid, replace this with an exact branch
			// membership check in GitManager.
		}
	}

	if src.CheckFlakePath && !res.NixFlakeFile.Checked {
		commitID := defaultCommitID
		if src.SelectedCommit != nil {
			commitID = selectedCommitID(src.SelectedCommit)
		}
		res.CheckedFlakePath = src.SelectedFlakePath
		if commitID == "" {
			res.NixFlakeFile = validationErr("No commits found for selected branch.")
			return &apigen.ValidateSourceResponse{NixDockerBuild: &res}, nil
		}
		exists, err := h.GitVersions.PathExists(ctx, repo, src.SelectedFlakePath, commitID)
		if err != nil {
			slog.Warn("checking flake path", "repo", repo, "commit", commitID, "err", err)
			errMessage := fmt.Sprintf("Error checking path '%v' on commit '%v': %v", src.SelectedFlakePath, commitID, err)
			res.NixFlakeFile = validationErr(errMessage)
		} else if !exists {
			res.NixFlakeFile = validationErr(fmt.Sprintf("Flake path '%v' doesn't exist on commit '%v'", src.SelectedFlakePath, commitID))
		} else {
			res.NixFlakeFile = validationOK(fmt.Sprintf("Flake path '%v' exists", src.SelectedFlakePath))
		}
	}

	return &apigen.ValidateSourceResponse{NixDockerBuild: &res}, nil
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
	return nil
}

func (h *Handler) validateContainerImageSource(ctx apigen.Context, src *apigen.ValidateContainerImageSource) (*apigen.ValidateSourceResponse, error) {
	image := src.Image
	tags, err := (versionprovider.ContainerImageVersionProvider{}).ListTags(ctx, image)
	if err != nil {
		slog.Warn("container image validation failed", "image", image, "err", err)
		return &apigen.ValidateSourceResponse{ContainerImage: &apigen.ValidateContainerImageSourceResponse{Image: validationErr("Image not accessible: " + containerImageRef(image))}}, nil
	}
	res := apigen.ValidateContainerImageSourceResponse{Image: validationOK("Image accessible: " + containerImageRef(image))}
	if src.RefreshVersions {
		res.Tags = tags
	}
	return &apigen.ValidateSourceResponse{ContainerImage: &res}, nil
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

func countValidationSources(req *apigen.ValidateSourceRequest) int {
	count := 0
	if req.NixDockerBuild != nil {
		count++
	}
	if req.ContainerImage != nil {
		count++
	}
	return count
}

func selectedValidationBranch(branches []string, requested string) string {
	branch := strings.TrimSpace(requested)
	if len(branches) == 0 {
		return ""
	}
	if branch == "" {
		branch = "main"
		if !containsString(branches, branch) {
			branch = branches[0]
		}
	}
	return branch
}
