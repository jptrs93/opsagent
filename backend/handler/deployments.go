package handler

import (
	"context"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine/logreader"
	"github.com/jptrs93/opsagent/backend/engine/versionprovider"
)

var InvalidRequestBodyErr = apigen.NewApiErr("Invalid request body", "invalid_request_body", http.StatusBadRequest)
var MissingKeyErr = apigen.NewApiErr("Missing deployment identifier", "missing_key", http.StatusBadRequest)
var NoPrepareOutputErr = apigen.NewApiErr("No prepare output found", "prepare_output_not_found", http.StatusNotFound)
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
	prepare         *apigen.PrepareConfig
	source          string
	repo            string
	scope           string
	commit          string
	flakePath       string
	refreshScopes   bool
	refreshVersions bool
	checkCommit     bool
	checkFlakePath  bool
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

	var scopes []string
	if in.refreshScopes {
		var err error
		scopes, err = provider.ListScopes(ctx, in.prepare)
		if err != nil {
			slog.Warn("source validation failed", "repo", in.repo, "err", err)
			return validationResponse(in, validationErr(sourceAccessErrorMessage(in)), validationErr(""), nil, "", nil), nil
		}
	}

	scope := strings.TrimSpace(in.scope)
	if in.refreshScopes {
		scope = selectedValidationScope(scopes, in.scope)
	}
	var versions []*apigen.Version
	gitResult := validationErr("")
	flakeResult := validationErr("")
	if in.refreshVersions {
		var err error
		versions, err = provider.ListVersions(ctx, in.prepare, scope)
		if err != nil {
			slog.Warn("source validation failed", "repo", in.repo, "scope", scope, "err", err)
			return validationResponse(in, validationErr(sourceAccessErrorMessage(in)), validationErr(""), scopes, scope, nil), nil
		}
		gitResult = validationOK(sourceAccessOKMessage(in))
	}

	if in.checkCommit && in.commit != "" {
		exists, err := versionprovider.Git.CommitExists(ctx, in.repo, in.commit)
		if err != nil {
			slog.Warn("source commit validation failed", "repo", in.repo, "commit", in.commit, "err", err)
			return validationResponse(in, validationErr("Unable to validate selected commit."), flakeResult, scopes, scope, versions), nil
		}
		if !exists {
			return validationResponse(in, validationErr("Selected commit not found."), flakeResult, scopes, scope, versions), nil
		}
		gitResult = validationOK(sourceAccessOKMessage(in))
	}

	if in.checkFlakePath && in.flakePath != "" {
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
		gitResult = validationOK(sourceAccessOKMessage(in))
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
	case "nixDockerBuild":
		return &apigen.ValidateSourceResponse{NixDockerBuild: apigen.ValidateNixDockerBuildSourceResponse{
			GitRepository: gitResult,
			NixFlakeFile:  flakeResult,
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

	if !req.NixDockerBuild.IsZero() {
		repo := strings.TrimSpace(req.NixDockerBuild.RepoUrl)
		flakePath := strings.TrimSpace(req.NixDockerBuild.FlakePath)
		commit := strings.TrimSpace(req.NixDockerBuild.Commit)
		if repo == "" {
			return nil, RepoRequiredErr
		}
		refreshScopes := req.NixDockerBuild.RefreshScopes
		refreshVersions := req.NixDockerBuild.RefreshVersions
		checkCommit := req.NixDockerBuild.CheckCommit
		checkFlakePath := req.NixDockerBuild.CheckFlakePath
		if !refreshScopes && !refreshVersions && !checkCommit && !checkFlakePath {
			refreshScopes = true
			refreshVersions = true
			checkCommit = commit != ""
			checkFlakePath = flakePath != ""
		}
		return &validateSourceInput{
			prepare:         &apigen.PrepareConfig{NixDockerBuild: apigen.NixDockerBuildConfig{Repo: repo, Flake: flakePath}},
			source:          "nixDockerBuild",
			repo:            repo,
			scope:           strings.TrimSpace(req.NixDockerBuild.Branch),
			commit:          commit,
			flakePath:       flakePath,
			refreshScopes:   refreshScopes,
			refreshVersions: refreshVersions,
			checkCommit:     checkCommit,
			checkFlakePath:  checkFlakePath,
		}, nil
	}

	image := strings.TrimSpace(req.ContainerImage.Image)
	if image == "" {
		return nil, ImageRequiredErr
	}
	return &validateSourceInput{
		prepare:         &apigen.PrepareConfig{ContainerImage: apigen.ContainerImageConfig{Image: image}},
		source:          "containerImage",
		repo:            image,
		refreshVersions: true,
	}, nil
}

func countValidationSources(req *apigen.ValidateSourceRequest) int {
	count := 0
	if !req.NixDockerBuild.IsZero() {
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

func (h *Handler) PostV1DeploymentLogSearch(ctx apigen.Context, req *apigen.LogSearchRequest) iter.Seq2[*apigen.LogLine, error] {
	return func(yield func(*apigen.LogLine, error) bool) {
		if req == nil {
			yield(nil, MissingKeyErr)
			return
		}
		if req.TimeStart.IsZero() {
			yield(nil, invalidConfigErrf("timeStart is required"))
			return
		}
		var till *time.Time
		if !req.TimeEnd.IsZero() {
			till = &req.TimeEnd
		}
		if req.DeploymentID == 0 {
			machine := strings.TrimSpace(req.SearchKeys["machine"])
			if machine == "" {
				yield(nil, MissingKeyErr)
				return
			}
			if machine != h.MachineName && h.ClusterPrimary != nil {
				stream, err := h.ClusterPrimary.RequestLogSearch(machine, &apigen.MsgToWorker{LogSearchRequest: req})
				if err != nil {
					yield(nil, apigen.NewApiErr("Worker not connected: "+machine, "worker_not_connected", 502))
					return
				}
				defer stream.Close()
				go func() {
					<-ctx.Done()
					stream.Close()
				}()
				count := 0
				limit := logSearchLimit(req)
				for line, err := range stream.Seq() {
					if !yield(line, err) || err != nil {
						return
					}
					count++
					if limit > 0 && count >= limit {
						return
					}
				}
				return
			}
			streamLocalLogSearch(req, till, yield)
			return
		}

		cfg := h.findConfigByID(req.DeploymentID)
		if cfg == nil {
			yield(nil, DeploymentNotFoundErr)
			return
		}
		if cfg.ConfigID.Machine != "" && cfg.ConfigID.Machine != h.MachineName && h.ClusterPrimary != nil {
			stream, err := h.ClusterPrimary.RequestLogSearch(cfg.ConfigID.Machine, &apigen.MsgToWorker{LogSearchRequest: req})
			if err != nil {
				yield(nil, apigen.NewApiErr("Worker not connected: "+cfg.ConfigID.Machine, "worker_not_connected", 502))
				return
			}
			defer stream.Close()
			go func() {
				<-ctx.Done()
				stream.Close()
			}()
			count := 0
			limit := logSearchLimit(req)
			for line, err := range stream.Seq() {
				if !yield(line, err) || err != nil {
					return
				}
				count++
				if limit > 0 && count >= limit {
					return
				}
			}
			return
		}

		streamLocalLogSearch(req, till, yield)
	}
}

func streamLocalLogSearch(req *apigen.LogSearchRequest, till *time.Time, yield func(*apigen.LogLine, error) bool) {
	count := 0
	limit := logSearchLimit(req)
	for line, err := range logreader.StreamLogs(int(req.DeploymentID), req.TimeStart, till) {
		if err != nil {
			yield(nil, err)
			return
		}
		if !matchesLogSearch(line, req) {
			continue
		}
		apiLine := toAPILogLine(line)
		if !yield(&apiLine, nil) {
			return
		}
		count++
		if limit > 0 && count >= limit {
			return
		}
	}
}

func logSearchLimit(req *apigen.LogSearchRequest) int {
	if req == nil || req.LogLineLimit <= 0 {
		return 0
	}
	return int(req.LogLineLimit)
}

func (h *Handler) PostV1DeploymentPrepareOutput(ctx apigen.Context, req *apigen.PrepareOutputRequest) iter.Seq2[*apigen.PrepareOutputChunk, error] {
	return func(yield func(*apigen.PrepareOutputChunk, error) bool) {
		if req == nil || req.DeploymentID == 0 {
			yield(nil, MissingKeyErr)
			return
		}

		cfg := h.findConfigByID(req.DeploymentID)
		if cfg == nil {
			yield(nil, DeploymentNotFoundErr)
			return
		}
		if cfg.ConfigID.Machine != "" && cfg.ConfigID.Machine != h.MachineName && h.ClusterPrimary != nil {
			reader, err := h.ClusterPrimary.RequestLogs(cfg.ConfigID.Machine, &apigen.MsgToWorker{
				DeploymentLogRequest: &apigen.DeploymentLogRequest{PreparerOutput: req},
			})
			if err != nil {
				yield(nil, apigen.NewApiErr("Worker not connected: "+cfg.ConfigID.Machine, "worker_not_connected", 502))
				return
			}
			defer reader.Close()
			go func() {
				<-ctx.Done()
				reader.Close()
			}()
			streamPrepareOutputReader(reader, yield)
			return
		}

		localReq := *req
		if localReq.Version == 0 {
			st := h.Store.FetchDeploymentStatus(localReq.DeploymentID)
			if st != nil && !st.Preparer.IsZero() {
				localReq.Version = st.Preparer.DeploymentConfigVersion
			} else {
				localReq.Version = cfg.Version
			}
		}
		if localReq.Version == 0 {
			yield(nil, NoPrepareOutputErr)
			return
		}
		streamLocalPrepareOutput(ctx, h, &localReq, yield)
	}
}

func streamPrepareOutputReader(r io.Reader, yield func(*apigen.PrepareOutputChunk, error) bool) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			if !yield(&apigen.PrepareOutputChunk{Data: data}, nil) {
				return
			}
		}
		if err == io.EOF {
			return
		}
		if err != nil {
			yield(nil, err)
			return
		}
	}
}

func streamLocalPrepareOutput(ctx apigen.Context, h *Handler, req *apigen.PrepareOutputRequest, yield func(*apigen.PrepareOutputChunk, error) bool) {
	f, err := waitForPrepareOutputFile(ctx, req.OutputPath())
	if err != nil {
		yield(nil, NoPrepareOutputErr)
		return
	}
	defer f.Close()

	buf := make([]byte, 32*1024)
	drain := func() bool {
		for {
			n, readErr := f.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				if !yield(&apigen.PrepareOutputChunk{Data: data}, nil) {
					return false
				}
			}
			if readErr == io.EOF {
				return true
			}
			if readErr != nil {
				yield(nil, readErr)
				return false
			}
		}
	}

	if !drain() {
		return
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !drain() {
				return
			}
			st := h.Store.FetchDeploymentStatus(req.DeploymentID)
			if st == nil || st.Preparer.IsZero() || !isPrepareInProgress(st.Preparer.Status) {
				_ = drain()
				return
			}
		}
	}
}

func waitForPrepareOutputFile(ctx context.Context, path string) (*os.File, error) {
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

func matchesLogSearch(line logreader.LogLine, req *apigen.LogSearchRequest) bool {
	if req.LevelMin != "" && levelRank(line.Level) < levelRank(req.LevelMin) {
		return false
	}
	for key, want := range req.SearchKeys {
		if req.DeploymentID == 0 && key == "machine" {
			continue
		}
		if logSearchValue(line, key) != want {
			return false
		}
	}
	return true
}

func logSearchValue(line logreader.LogLine, key string) string {
	switch key {
	case "time":
		return line.Time.Format(time.RFC3339Nano)
	case "level":
		return line.Level
	case "msg", "message":
		return line.Msg
	default:
		return line.Props[key]
	}
}

func levelRank(level string) int {
	switch strings.ToUpper(level) {
	case "TRACE":
		return 1
	case "DEBUG":
		return 2
	case "INFO":
		return 3
	case "WARN", "WARNING":
		return 4
	case "ERROR":
		return 5
	case "FATAL", "PANIC":
		return 6
	default:
		return 0
	}
}

func toAPILogLine(line logreader.LogLine) apigen.LogLine {
	return apigen.LogLine{
		Time:  line.Time,
		Level: line.Level,
		Msg:   line.Msg,
		Props: line.Props,
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
	hasNixDocker := !prepare.NixDockerBuild.IsZero()
	hasGH := !prepare.GithubRelease.IsZero()
	hasContainer := !prepare.ContainerImage.IsZero()
	set := 0
	for _, b := range []bool{hasNixDocker, hasGH, hasContainer} {
		if b {
			set++
		}
	}
	if set == 0 {
		return invalidConfigErrf("prepare: one of nixDockerBuild or containerImage must be set")
	}
	if set > 1 {
		return invalidConfigErrf("prepare: only one of nixDockerBuild or containerImage may be set")
	}
	if hasGH {
		return invalidConfigErrf("prepare.githubRelease is internal-only")
	}
	if hasNixDocker {
		if prepare.NixDockerBuild.Repo == "" {
			return invalidConfigErrf("prepare.nixDockerBuild: repo is required")
		}
		if prepare.NixDockerBuild.Flake == "" {
			return invalidConfigErrf("prepare.nixDockerBuild: flake is required")
		}
	}
	if hasContainer {
		if prepare.ContainerImage.Image == "" {
			return invalidConfigErrf("prepare.containerImage: image is required")
		}
	}
	return nil
}

// validateContainerPairing enforces that public deployments only use the
// container runner. An omitted runner means all-default container config.
func validateContainerPairing(_ *apigen.PrepareConfig, runner *apigen.RunnerConfig) error {
	if runner != nil && !runner.Systemd.IsZero() {
		return invalidConfigErrf("runner.systemd is internal-only")
	}
	return nil
}

func validateRunnerConfig(runner *apigen.RunnerConfig, prepare *apigen.PrepareConfig, assets deploymentAssetResolver) error {
	if runner == nil || runner.IsZero() {
		return nil
	}
	hasSystemd := !runner.Systemd.IsZero()
	hasContainer := !runner.Container.IsZero()
	if hasSystemd {
		return invalidConfigErrf("runner.systemd is internal-only")
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
