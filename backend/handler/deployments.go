package handler

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine/versionprovider"
)

var InvalidRequestBodyErr = apigen.NewApiErr("Invalid request body", "invalid_request_body", http.StatusBadRequest)
var MissingKeyErr = apigen.NewApiErr("Missing deployment identifier", "missing_key", http.StatusBadRequest)
var NoPrepareLogErr = apigen.NewApiErr("No prepare log found", "prepare_log_not_found", http.StatusNotFound)
var NoRunOutputErr = apigen.NewApiErr("No run output found", "run_output_not_found", http.StatusNotFound)
var InvalidConfigErr = apigen.NewApiErr("", "invalid_config", http.StatusBadRequest)
var DeploymentNotFoundErr = apigen.NewApiErr("Deployment not found", "deployment_not_found", http.StatusNotFound)

var DuplicateDeploymentErr = apigen.NewApiErr("A deployment with this name, environment, and machine already exists", "duplicate_deployment", http.StatusConflict)

func (h *Handler) PostV1DeploymentCreate(ctx apigen.Context, req *apigen.DeploymentCreateRequest) (*apigen.DeploymentConfig, error) {
	cid := req.ConfigID
	if cid.Name == "" {
		return nil, invalidConfigErrf("name is required")
	}
	if cid.Machine == "" {
		return nil, invalidConfigErrf("machine is required")
	}
	spec, err := h.validateDeploymentSpec(&req.Spec)
	if err != nil {
		return nil, err
	}

	// Check for duplicate before creating.
	snapshot := h.Store.FetchDeploymentSnapshot("")
	for _, dws := range snapshot {
		if dws.Config.ConfigID == cid && !dws.Config.Deleted {
			return nil, DuplicateDeploymentErr
		}
	}

	cfg := h.Store.MustCreateDeployment(ctx, &cid, spec)
	return cfg, nil
}

func (h *Handler) PostV1DeploymentUpdate(ctx apigen.Context, req *apigen.DeploymentUpdateRequest) (*apigen.DesiredState, error) {
	if req.DeploymentID == 0 {
		return nil, MissingKeyErr
	}

	if !req.Spec.IsZero() {
		spec, err := h.validateDeploymentSpec(&req.Spec)
		if err != nil {
			return nil, err
		}
		h.Store.MustUpdateDeploymentSpec(ctx, req.DeploymentID, spec)
	}

	desired := apigen.DesiredState{}
	if req.Stop {
		desired.Running = false
		// Preserve the existing version so a subsequent "start" can reuse it.
		if cfg := h.findConfigByID(req.DeploymentID); cfg != nil {
			desired.Version = cfg.DesiredState.Version
		}
	} else if req.TargetVersion != "" {
		desired.Version = req.TargetVersion
		desired.Running = true
	}

	if req.TargetVersion != "" || req.Stop {
		h.Store.MustSetDeploymentDesiredState(ctx, req.DeploymentID, desired)
	}

	return &desired, nil
}

func (h *Handler) PostV1DeploymentVersions(ctx apigen.Context, req *apigen.DeploymentVersionsRequest) (*apigen.DeploymentVersions, error) {
	if req.DeploymentID == 0 {
		return nil, MissingKeyErr
	}

	cfg := h.findConfigByID(req.DeploymentID)
	if cfg == nil || cfg.Spec.Prepare.IsZero() {
		return nil, DeploymentNotFoundErr
	}

	provider, err := versionprovider.ForConfig(&cfg.Spec.Prepare)
	if err != nil {
		return nil, DeploymentNotFoundErr
	}

	scopes, err := provider.ListScopes(ctx, &cfg.Spec.Prepare)
	if err != nil {
		return nil, fmt.Errorf("listing scopes: %w", err)
	}

	versionsByScope := make(map[string]*apigen.ScopedVersions)

	if req.Scope != "" {
		// Fetch specific scope only.
		vs, err := provider.ListVersions(ctx, &cfg.Spec.Prepare, req.Scope)
		if err != nil {
			return nil, fmt.Errorf("listing versions: %w", err)
		}
		versionsByScope[req.Scope] = &apigen.ScopedVersions{Versions: vs}
	} else if len(scopes) == 0 {
		// GitHub releases: no scopes, single version list.
		vs, err := provider.ListVersions(ctx, &cfg.Spec.Prepare, "")
		if err != nil {
			return nil, fmt.Errorf("listing versions: %w", err)
		}
		versionsByScope[""] = &apigen.ScopedVersions{Versions: vs}
	} else {
		// Default to main or first scope.
		defaultScope := "main"
		if !containsString(scopes, "main") {
			defaultScope = scopes[0]
		}
		vs, err := provider.ListVersions(ctx, &cfg.Spec.Prepare, defaultScope)
		if err != nil {
			return nil, fmt.Errorf("listing versions: %w", err)
		}
		versionsByScope[defaultScope] = &apigen.ScopedVersions{Versions: vs}
	}

	return &apigen.DeploymentVersions{
		DeploymentID:    req.DeploymentID,
		Scopes:          scopes,
		VersionsByScope: versionsByScope,
	}, nil
}

var RepoRequiredErr = apigen.NewApiErr("Repository is required", "missing_repo", http.StatusBadRequest)
var ImageRequiredErr = apigen.NewApiErr("Image is required", "missing_image", http.StatusBadRequest)
var InvalidSourceTypeErr = apigen.NewApiErr("Invalid source type", "invalid_source_type", http.StatusBadRequest)

type validateSourceInput struct {
	prepare   *apigen.PrepareConfig
	source    string
	repo      string
	scope     string
	commit    string
	flakePath string
}

// PostV1RepoValidate checks that a binary source is reachable and authorized.
// It uses remote metadata APIs instead of cloning: git ls-remote for branches,
// the GitHub API for commits/releases, and GitHub contents for optional flake
// path validation.
func (h *Handler) PostV1RepoValidate(ctx apigen.Context, req *apigen.ValidateSourceRequest) (*apigen.ValidateSourceResponse, error) {
	in, err := validateSourceFromRequest(req)
	if err != nil {
		return nil, err
	}

	provider, err := versionprovider.ForConfig(in.prepare)
	if err != nil {
		return validationResponse(in, validationErr("Unsupported source type."), validationErr(""), nil, "", nil), nil
	}

	scopes, err := provider.ListScopes(ctx, in.prepare)
	if err != nil {
		slog.Warn("source validation failed", "repo", in.repo, "err", err)
		return validationResponse(in, validationErr(sourceAccessErrorMessage(in)), validationErr(""), nil, "", nil), nil
	}

	scope := selectedValidationScope(scopes, in.scope)
	versions, err := provider.ListVersions(ctx, in.prepare, scope)
	if err != nil {
		slog.Warn("source validation failed", "repo", in.repo, "scope", scope, "err", err)
		return validationResponse(in, validationErr(sourceAccessErrorMessage(in)), validationErr(""), scopes, scope, nil), nil
	}
	gitResult := validationOK(sourceAccessOKMessage(in))
	flakeResult := validationErr("")

	if in.commit != "" {
		exists, err := versionprovider.Git.CommitExists(ctx, in.repo, in.commit)
		if err != nil {
			slog.Warn("source commit validation failed", "repo", in.repo, "commit", in.commit, "err", err)
			return validationResponse(in, validationErr("Unable to validate selected commit."), flakeResult, scopes, scope, versions), nil
		}
		if !exists {
			return validationResponse(in, validationErr("Selected commit not found."), flakeResult, scopes, scope, versions), nil
		}
	}

	if in.flakePath != "" {
		ref := in.commit
		if ref == "" {
			ref = scope
		}
		exists, err := versionprovider.Git.PathExists(ctx, in.repo, in.flakePath, ref)
		if err != nil {
			slog.Warn("source flake path validation failed", "repo", in.repo, "flakePath", in.flakePath, "ref", ref, "err", err)
			return validationResponse(in, gitResult, validationErr("Unable to validate flake path."), scopes, scope, versions), nil
		}
		if !exists {
			return validationResponse(in, gitResult, validationErr("Flake path not found at selected revision."), scopes, scope, versions), nil
		}
		flakeResult = validationOK("Path verified")
	}

	return validationResponse(in, gitResult, flakeResult, scopes, scope, versions), nil
}

func validationOK(message string) apigen.ValidationResult {
	return apigen.ValidationResult{Ok: true, Message: message}
}

func validationErr(message string) apigen.ValidationResult {
	return apigen.ValidationResult{Ok: false, Message: message}
}

func sourceAccessOKMessage(in *validateSourceInput) string {
	if in != nil && in.source == "containerImage" {
		return "Image accessible: " + containerImageRepoURL(in.repo)
	}
	return "Repo accessible."
}

func sourceAccessErrorMessage(in *validateSourceInput) string {
	if in != nil && in.source == "containerImage" {
		return "Image not accessible: " + containerImageRepoURL(in.repo)
	}
	return "Git repository not accessible."
}

func containerImageRepoURL(image string) string {
	repoURL, err := versionprovider.ContainerImageRepositoryURL(image)
	if err != nil {
		return image
	}
	return repoURL
}

func validationResponse(in *validateSourceInput, gitResult apigen.ValidationResult, flakeResult apigen.ValidationResult, scopes []string, scope string, versions []*apigen.Version) *apigen.ValidateSourceResponse {
	if in == nil {
		return &apigen.ValidateSourceResponse{}
	}
	switch in.source {
	case "nixBuild":
		return &apigen.ValidateSourceResponse{NixBuild: apigen.ValidateNixBuildSourceResponse{
			GitRepository: gitResult,
			NixFlakeFile:  flakeResult,
			Scopes:        scopes,
			Scope:         scope,
			Versions:      versions,
		}}
	case "nixDockerBuild":
		return &apigen.ValidateSourceResponse{NixDockerBuild: apigen.ValidateNixDockerBuildSourceResponse{
			GitRepository: gitResult,
			NixFlakeFile:  flakeResult,
			Scopes:        scopes,
			Scope:         scope,
			Versions:      versions,
		}}
	case "githubRelease":
		return &apigen.ValidateSourceResponse{GithubRelease: apigen.ValidateGithubReleaseSourceResponse{
			GitRepository: gitResult,
			Scopes:        scopes,
			Scope:         scope,
			Versions:      versions,
		}}
	case "containerImage":
		return &apigen.ValidateSourceResponse{ContainerImage: apigen.ValidateContainerImageSourceResponse{Image: gitResult, Versions: versions}}
	default:
		return &apigen.ValidateSourceResponse{}
	}
}

func validateSourceFromRequest(req *apigen.ValidateSourceRequest) (*validateSourceInput, error) {
	if req == nil {
		return nil, InvalidRequestBodyErr
	}
	if countValidationSources(req) != 1 {
		return nil, InvalidSourceTypeErr
	}

	if !req.NixBuild.IsZero() {
		repo := strings.TrimSpace(req.NixBuild.RepoUrl)
		flakePath := strings.TrimSpace(req.NixBuild.FlakePath)
		if repo == "" {
			return nil, RepoRequiredErr
		}
		return &validateSourceInput{
			prepare:   &apigen.PrepareConfig{NixBuild: apigen.NixBuildConfig{Repo: repo, Flake: flakePath}},
			source:    "nixBuild",
			repo:      repo,
			scope:     strings.TrimSpace(req.NixBuild.Branch),
			commit:    strings.TrimSpace(req.NixBuild.Commit),
			flakePath: flakePath,
		}, nil
	}

	if !req.NixDockerBuild.IsZero() {
		repo := strings.TrimSpace(req.NixDockerBuild.RepoUrl)
		flakePath := strings.TrimSpace(req.NixDockerBuild.FlakePath)
		if repo == "" {
			return nil, RepoRequiredErr
		}
		return &validateSourceInput{
			prepare:   &apigen.PrepareConfig{NixDockerBuild: apigen.NixDockerBuildConfig{Repo: repo, Flake: flakePath}},
			source:    "nixDockerBuild",
			repo:      repo,
			scope:     strings.TrimSpace(req.NixDockerBuild.Branch),
			commit:    strings.TrimSpace(req.NixDockerBuild.Commit),
			flakePath: flakePath,
		}, nil
	}

	if !req.GithubRelease.IsZero() {
		repo := strings.TrimSpace(req.GithubRelease.RepoUrl)
		if repo == "" {
			return nil, RepoRequiredErr
		}
		return &validateSourceInput{
			prepare: &apigen.PrepareConfig{GithubRelease: apigen.GithubReleaseConfig{Repo: repo}},
			source:  "githubRelease",
			repo:    repo,
		}, nil
	}

	image := strings.TrimSpace(req.ContainerImage.Image)
	if image == "" {
		return nil, ImageRequiredErr
	}
	return &validateSourceInput{
		prepare: &apigen.PrepareConfig{ContainerImage: apigen.ContainerImageConfig{Image: image}},
		source:  "containerImage",
		repo:    image,
	}, nil
}

func countValidationSources(req *apigen.ValidateSourceRequest) int {
	count := 0
	if !req.NixBuild.IsZero() {
		count++
	}
	if !req.NixDockerBuild.IsZero() {
		count++
	}
	if !req.GithubRelease.IsZero() {
		count++
	}
	if !req.ContainerImage.IsZero() {
		count++
	}
	return count
}

func selectedValidationScope(scopes []string, requested string) string {
	scope := strings.TrimSpace(requested)
	if len(scopes) == 0 {
		return ""
	}
	if scope == "" {
		scope = "main"
		if !containsString(scopes, scope) {
			scope = scopes[0]
		}
	}
	return scope
}

// PostV1GithubAssetValidate checks that a named release asset exists in at least
// one of the repo's published releases. As with repo validation, the message is
// generic and underlying errors are logged server-side only.
func (h *Handler) PostV1GithubAssetValidate(ctx apigen.Context, req *apigen.GithubAssetValidateRequest) (*apigen.ValidateSourceResponse, error) {
	repo := strings.TrimSpace(req.Repo)
	asset := strings.TrimSpace(req.Asset)
	if repo == "" {
		return nil, RepoRequiredErr
	}
	// An empty asset means "use the release's only asset" — nothing to check.
	if asset == "" {
		return &apigen.ValidateSourceResponse{GithubRelease: apigen.ValidateGithubReleaseSourceResponse{ReleaseAsset: validationOK("")}}, nil
	}

	prepare := &apigen.PrepareConfig{GithubRelease: apigen.GithubReleaseConfig{Repo: repo}}
	found, err := versionprovider.GHRel.AssetExists(ctx, prepare, asset)
	if err != nil {
		slog.Warn("github asset validation failed", "repo", repo, "asset", asset, "err", err)
		return &apigen.ValidateSourceResponse{
			GithubRelease: apigen.ValidateGithubReleaseSourceResponse{ReleaseAsset: validationErr("Could not check releases. Verify the repository and that the configured GitHub token grants access.")},
		}, nil
	}
	if !found {
		return &apigen.ValidateSourceResponse{
			GithubRelease: apigen.ValidateGithubReleaseSourceResponse{ReleaseAsset: validationErr("No published release has an asset with this name.")},
		}, nil
	}
	return &apigen.ValidateSourceResponse{GithubRelease: apigen.ValidateGithubReleaseSourceResponse{ReleaseAsset: validationOK("Asset found in a published release.")}}, nil
}

func (h *Handler) PostV1DeploymentLogs(ctx apigen.Context, r *http.Request, w http.ResponseWriter) error {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("reading deployment log request body: %w", err)
	}

	req, err := apigen.DecodeDeploymentLogRequest(bodyBytes)
	if err != nil {
		respondErr(w, InvalidRequestBodyErr)
		return nil
	}

	var deploymentID int32
	if req.RunnerOutput != nil {
		deploymentID = req.RunnerOutput.DeploymentID
	} else if req.PreparerOutput != nil {
		deploymentID = req.PreparerOutput.DeploymentID
	}
	if deploymentID == 0 {
		respondErr(w, MissingKeyErr)
		return nil
	}

	// Check if the deployment lives on a remote machine.
	cfg := h.findConfigByID(deploymentID)
	if cfg != nil && cfg.ConfigID.Machine != "" && cfg.ConfigID.Machine != h.MachineName && h.ClusterPrimary != nil {
		clusterReq := &apigen.MsgToWorker{DeploymentLogRequest: req}
		return h.proxyRemoteLogs(ctx, w, cfg.ConfigID.Machine, clusterReq)
	}

	// Resolve seqNo=0 to latest from local status.
	if req.RunnerOutput != nil {
		if req.RunnerOutput.Version == 0 {
			st := h.Store.FetchDeploymentStatus(deploymentID)
			if st != nil && !st.Runner.IsZero() {
				req.RunnerOutput.Version = st.Runner.DeploymentConfigVersion
			}
		}
		return h.streamRunLog(ctx, w, req.RunnerOutput)
	}
	if req.PreparerOutput.Version == 0 {
		st := h.Store.FetchDeploymentStatus(deploymentID)
		if st != nil && !st.Preparer.IsZero() {
			req.PreparerOutput.Version = st.Preparer.DeploymentConfigVersion
		}
	}
	return h.streamPrepareLog(ctx, w, req.PreparerOutput)
}

func (h *Handler) streamRunLog(ctx apigen.Context, w http.ResponseWriter, req *apigen.RunOutputRequest) error {
	logPath := req.OutputPath()
	f, err := waitForFile(ctx, logPath)
	if err != nil {
		respondErr(w, NoRunOutputErr)
		return nil
	}
	defer f.Close()
	return streamLogFile(ctx, w, f, func() bool {
		st := h.Store.FetchDeploymentStatus(req.DeploymentID)
		return st != nil && !st.Runner.IsZero() && isRunnerActive(st.Runner.Status)
	})
}

func (h *Handler) streamPrepareLog(ctx apigen.Context, w http.ResponseWriter, req *apigen.PrepareOutputRequest) error {
	logPath := req.OutputPath()
	f, err := waitForFile(ctx, logPath)
	if err != nil {
		respondErr(w, NoPrepareLogErr)
		return nil
	}
	defer f.Close()
	return streamLogFile(ctx, w, f, func() bool {
		st := h.Store.FetchDeploymentStatus(req.DeploymentID)
		return st != nil && !st.Preparer.IsZero() && isPrepareInProgress(st.Preparer.Status)
	})
}

func waitForFile(ctx apigen.Context, path string) (*os.File, error) {
	f, err := os.Open(path)
	if err == nil {
		return f, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, os.ErrNotExist
		case <-ticker.C:
			f, err = os.Open(path)
			if err == nil {
				return f, nil
			}
			if !os.IsNotExist(err) {
				return nil, err
			}
		}
	}
}

func (h *Handler) proxyRemoteLogs(ctx apigen.Context, w http.ResponseWriter, machine string, req *apigen.MsgToWorker) error {
	reader, err := h.ClusterPrimary.RequestLogs(machine, req)
	if err != nil {
		respondErr(w, apigen.NewApiErr("Worker not connected: "+machine, "worker_not_connected", 502))
		return nil
	}
	defer reader.Close()

	// Close the reader when the client disconnects so the worker is told
	// to stop tailing and the session's stream channel is cleaned up.
	go func() {
		<-ctx.Done()
		reader.Close()
	}()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	flusher, canFlush := w.(http.Flusher)

	buf := make([]byte, 32*1024)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return nil
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			return nil
		}
	}
}

func streamLogFile(ctx apigen.Context, w http.ResponseWriter, f *os.File, keepTailing func() bool) error {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	flusher, canFlush := w.(http.Flusher)

	buf := make([]byte, 4096)
	drain := func() (eof bool, err error) {
		for {
			n, readErr := f.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return false, werr
				}
			}
			if readErr == io.EOF {
				return true, nil
			}
			if readErr != nil {
				return false, readErr
			}
		}
	}

	if _, err := drain(); err != nil {
		return nil
	}
	if canFlush {
		flusher.Flush()
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := drain(); err != nil {
				return nil
			}
			if canFlush {
				flusher.Flush()
			}
			if !keepTailing() {
				return nil
			}
		}
	}
}

// findConfigByID looks up a deployment config from the store's snapshot by integer ID.
func (h *Handler) findConfigByID(deploymentID int32) *apigen.DeploymentConfig {
	snapshot := h.Store.FetchDeploymentSnapshot("")
	for _, dws := range snapshot {
		if dws.Config.ID == deploymentID {
			cfg := dws.Config
			return &cfg
		}
	}
	return nil
}

func isPrepareInProgress(status apigen.PreparationStatus) bool {
	return status == apigen.PreparationStatus_PREPARING ||
		status == apigen.PreparationStatus_DOWNLOADING
}

func isRunnerActive(status apigen.RunningStatus) bool {
	return status == apigen.RunningStatus_RUNNING ||
		status == apigen.RunningStatus_STARTING
}

func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

type deploymentAssetResolver interface {
	GetAsset(key string, version int32) (*apigen.Asset, bool)
}

func (h *Handler) validateDeploymentSpec(spec *apigen.DeploymentSpec) (*apigen.DeploymentSpec, error) {
	return validateDeploymentSpecWithAssets(spec, h.Store)
}

func validateDeploymentSpecWithAssets(spec *apigen.DeploymentSpec, assets deploymentAssetResolver) (*apigen.DeploymentSpec, error) {
	if spec == nil {
		return nil, invalidConfigErrf("prepare is required")
	}
	out := *spec
	if err := validatePrepareConfig(&out.Prepare); err != nil {
		return nil, err
	}
	if err := validateRunnerConfig(&out.Runner, &out.Prepare, assets); err != nil {
		return nil, err
	}
	if err := validateContainerPairing(&out.Prepare, &out.Runner); err != nil {
		return nil, err
	}
	return &out, nil
}

func validatePrepareConfig(prepare *apigen.PrepareConfig) error {
	if prepare == nil || prepare.IsZero() {
		return invalidConfigErrf("prepare is required")
	}
	hasNix := !prepare.NixBuild.IsZero()
	hasNixDocker := !prepare.NixDockerBuild.IsZero()
	hasGH := !prepare.GithubRelease.IsZero()
	hasContainer := !prepare.ContainerImage.IsZero()
	set := 0
	for _, b := range []bool{hasNix, hasNixDocker, hasGH, hasContainer} {
		if b {
			set++
		}
	}
	if set == 0 {
		return invalidConfigErrf("prepare: one of nixBuild, nixDockerBuild, githubRelease or containerImage must be set")
	}
	if set > 1 {
		return invalidConfigErrf("prepare: only one of nixBuild, nixDockerBuild, githubRelease or containerImage may be set")
	}
	if hasNix {
		if prepare.NixBuild.Repo == "" {
			return invalidConfigErrf("prepare.nixBuild: repo is required")
		}
		if prepare.NixBuild.Flake == "" {
			return invalidConfigErrf("prepare.nixBuild: flake is required")
		}
	}
	if hasNixDocker {
		if prepare.NixDockerBuild.Repo == "" {
			return invalidConfigErrf("prepare.nixDockerBuild: repo is required")
		}
		if prepare.NixDockerBuild.Flake == "" {
			return invalidConfigErrf("prepare.nixDockerBuild: flake is required")
		}
	}
	if hasGH {
		if prepare.GithubRelease.Repo == "" {
			return invalidConfigErrf("prepare.githubRelease: repo is required")
		}
	}
	if hasContainer {
		if prepare.ContainerImage.Image == "" {
			return invalidConfigErrf("prepare.containerImage: image is required")
		}
	}
	return nil
}

// validateContainerPairing enforces that the container image prepare and the
// container runner are used together — an image can only be run as a container,
// and the container runner can only run an image.
func validateContainerPairing(prepare *apigen.PrepareConfig, runner *apigen.RunnerConfig) error {
	prepareIsContainer := prepare != nil && (!prepare.ContainerImage.IsZero() || !prepare.NixDockerBuild.IsZero())
	runnerIsContainer := runner != nil && !runner.Container.IsZero()
	runnerIsOther := runner != nil && (!runner.OsProcess.IsZero() || !runner.Systemd.IsZero())
	if prepareIsContainer && runnerIsOther {
		return invalidConfigErrf("container image prepare variants require the container runner (or no runner block)")
	}
	if runnerIsContainer && !prepareIsContainer {
		return invalidConfigErrf("runner.container requires prepare.containerImage or prepare.nixDockerBuild")
	}
	return nil
}

func validateRunnerConfig(runner *apigen.RunnerConfig, prepare *apigen.PrepareConfig, assets deploymentAssetResolver) error {
	if runner == nil || runner.IsZero() {
		return nil
	}
	hasOS := !runner.OsProcess.IsZero()
	hasSystemd := !runner.Systemd.IsZero()
	hasContainer := !runner.Container.IsZero()
	set := 0
	for _, b := range []bool{hasOS, hasSystemd, hasContainer} {
		if b {
			set++
		}
	}
	if set > 1 {
		return invalidConfigErrf("runner: only one of osProcess, systemd or container may be set")
	}
	if hasOS {
		if err := validateEnvVars("runner.osProcess.env", runner.OsProcess.Env); err != nil {
			return err
		}
	}
	if hasSystemd {
		if runner.Systemd.Name == "" {
			return invalidConfigErrf("runner.systemd: name is required")
		}
		if runner.Systemd.BinPath == "" {
			return invalidConfigErrf("runner.systemd: binPath is required")
		}
	}
	if hasContainer {
		if err := validateEnvVars("runner.container.env", runner.Container.Env); err != nil {
			return err
		}
		for _, m := range runner.Container.Mounts {
			if m == nil || m.Host == "" || m.Container == "" {
				return invalidConfigErrf("runner.container.mounts: host and container are both required")
			}
		}
		assetMounts, err := resolveAssetMounts(runner.Container.AssetMounts, assets)
		if err != nil {
			return err
		}
		runner.Container.AssetMounts = assetMounts
	}
	return nil
}

func resolveAssetMounts(in []*apigen.ContainerAssetMount, assets deploymentAssetResolver) ([]*apigen.ContainerAssetMount, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if assets == nil {
		return nil, invalidConfigErrf("runner.container.assetMounts: assets cannot be resolved here")
	}
	out := make([]*apigen.ContainerAssetMount, 0, len(in))
	for _, m := range in {
		if m == nil {
			return nil, invalidConfigErrf("runner.container.assetMounts: asset and path are both required")
		}
		key := strings.TrimSpace(m.Asset)
		path := strings.TrimSpace(m.Path)
		if key == "" || path == "" {
			return nil, invalidConfigErrf("runner.container.assetMounts: asset and path are both required")
		}
		if !filepath.IsAbs(path) {
			return nil, invalidConfigErrf("runner.container.assetMounts: path must be absolute")
		}
		cleanPath := filepath.Clean(path)
		if cleanPath != path || cleanPath == "/" || strings.HasSuffix(path, "/") {
			return nil, invalidConfigErrf("runner.container.assetMounts: path must be an absolute file path")
		}
		asset, ok := assets.GetAsset(key, m.Version)
		if !ok {
			if m.Version > 0 {
				return nil, invalidConfigErrf("runner.container.assetMounts: asset %q version %d not found", key, m.Version)
			}
			return nil, invalidConfigErrf("runner.container.assetMounts: asset %q not found", key)
		}
		out = append(out, &apigen.ContainerAssetMount{
			Asset:   asset.Key,
			Version: asset.Version,
			Path:    cleanPath,
			Format:  asset.Format,
			AssetID: asset.ID,
		})
	}
	return out, nil
}

// validateEnvVars trims and validates env keys. Duplicate keys are rejected so
// the resulting process environment is unambiguous.
func validateEnvVars(scope string, in []*apigen.EnvVar) error {
	seen := make(map[string]struct{}, len(in))
	for _, e := range in {
		if e == nil {
			return invalidConfigErrf("%s: key is required", scope)
		}
		key := strings.TrimSpace(e.Key)
		if key == "" {
			return invalidConfigErrf("%s: key is required", scope)
		}
		if _, dup := seen[key]; dup {
			return invalidConfigErrf("%s: duplicate key %q", scope, key)
		}
		seen[key] = struct{}{}
		e.Key = key
	}
	return nil
}

func invalidConfigErrf(format string, args ...any) error {
	e := InvalidConfigErr
	msg := fmt.Sprintf(format, args...)
	e.InternalErr = msg
	e.DisplayErr = msg
	return e
}
