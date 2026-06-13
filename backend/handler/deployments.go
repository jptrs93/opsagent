package handler

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine/versionprovider"
	"gopkg.in/yaml.v3"
)

var InvalidRequestBodyErr = apigen.NewApiErr("Invalid request body", "invalid_request_body", http.StatusBadRequest)
var MissingKeyErr = apigen.NewApiErr("Missing deployment identifier", "missing_key", http.StatusBadRequest)
var NoPrepareLogErr = apigen.NewApiErr("No prepare log found", "prepare_log_not_found", http.StatusNotFound)
var NoRunOutputErr = apigen.NewApiErr("No run output found", "run_output_not_found", http.StatusNotFound)
var InvalidYAMLErr = apigen.NewApiErr("", "invalid_yaml", http.StatusBadRequest)
var InvalidConfigErr = apigen.NewApiErr("", "invalid_config", http.StatusBadRequest)
var DeploymentNotFoundErr = apigen.NewApiErr("Deployment not found", "deployment_not_found", http.StatusNotFound)

var DuplicateDeploymentErr = apigen.NewApiErr("A deployment with this name, environment, and machine already exists", "duplicate_deployment", http.StatusConflict)

func (h *Handler) PostV1DeploymentCreate(ctx apigen.Context, req *apigen.DeploymentCreateRequest) (*apigen.DeploymentConfig, error) {
	if req.YamlContent == "" {
		return nil, InvalidYAMLErr
	}

	cid, spec, err := parseCreateDeploymentYaml(req.YamlContent)
	if err != nil {
		return nil, err
	}

	// Check for duplicate before creating.
	snapshot := h.Store.FetchDeploymentSnapshot("")
	for _, dws := range snapshot {
		if dws.Config.ConfigID == *cid && !dws.Config.Deleted {
			return nil, DuplicateDeploymentErr
		}
	}

	cfg := h.Store.MustCreateDeployment(ctx, cid, spec)
	return cfg, nil
}

func (h *Handler) PostV1DeploymentUpdate(ctx apigen.Context, req *apigen.DeploymentUpdateRequest) (*apigen.DesiredState, error) {
	if req.DeploymentID == 0 {
		return nil, MissingKeyErr
	}

	// If yaml_content is provided, update the deployment spec first.
	if req.YamlContent != "" {
		spec, err := parseDeploymentYaml(req.YamlContent)
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
var InvalidSourceTypeErr = apigen.NewApiErr("Invalid source type", "invalid_source_type", http.StatusBadRequest)

// PostV1RepoValidate checks that a repo is reachable and authorized for the
// given source type. It reuses the version providers (git ls-remote / GitHub
// API), so a successful listing implies the configured credentials grant
// access. On success it also returns the first page of versions so the create UI
// can select an initial deployment version before the deployment exists. The
// returned message is intentionally generic — underlying errors are logged
// server-side rather than surfaced, since they can be noisy and the clone URL
// embeds the GitHub token.
func (h *Handler) PostV1RepoValidate(ctx apigen.Context, req *apigen.RepoValidateRequest) (*apigen.RepoValidateResponse, error) {
	repo := strings.TrimSpace(req.Repo)
	if repo == "" {
		return nil, RepoRequiredErr
	}

	prepare := &apigen.PrepareConfig{}
	switch req.SourceType {
	case "githubRelease":
		prepare.GithubRelease = apigen.GithubReleaseConfig{Repo: repo}
	case "nixBuild":
		prepare.NixBuild = apigen.NixBuildConfig{Repo: repo}
	case "nixDockerBuild":
		prepare.NixDockerBuild = apigen.NixDockerBuildConfig{Repo: repo}
	default:
		return nil, InvalidSourceTypeErr
	}

	provider, err := versionprovider.ForConfig(prepare)
	if err != nil {
		return &apigen.RepoValidateResponse{Ok: false, Message: "Unsupported source type."}, nil
	}

	scopes, err := provider.ListScopes(ctx, prepare)
	if err != nil {
		slog.Warn("repo validation failed", "repo", repo, "sourceType", req.SourceType, "err", err)
		return &apigen.RepoValidateResponse{
			Ok:      false,
			Message: "Repository not found or not accessible. Check the URL and that the configured GitHub token grants access.",
		}, nil
	}

	versionsByScope := make(map[string]*apigen.ScopedVersions)
	scope := strings.TrimSpace(req.Scope)
	if !prepare.GithubRelease.IsZero() {
		scope = ""
	} else if scope == "" {
		scope = "main"
		if !containsString(scopes, scope) && len(scopes) > 0 {
			scope = scopes[0]
		}
	}
	versions, err := provider.ListVersions(ctx, prepare, scope)
	if err != nil {
		slog.Warn("repo validation failed", "repo", repo, "sourceType", req.SourceType, "err", err)
		return &apigen.RepoValidateResponse{
			Ok:      false,
			Message: "Repository not found or not accessible. Check the URL and that the configured GitHub token grants access.",
		}, nil
	}
	versionsByScope[scope] = &apigen.ScopedVersions{Versions: versions}

	return &apigen.RepoValidateResponse{
		Ok:              true,
		Message:         "Repository is accessible.",
		Scopes:          scopes,
		VersionsByScope: versionsByScope,
	}, nil
}

// PostV1GithubAssetValidate checks that a named release asset exists in at least
// one of the repo's published releases. As with repo validation, the message is
// generic and underlying errors are logged server-side only.
func (h *Handler) PostV1GithubAssetValidate(ctx apigen.Context, req *apigen.GithubAssetValidateRequest) (*apigen.RepoValidateResponse, error) {
	repo := strings.TrimSpace(req.Repo)
	asset := strings.TrimSpace(req.Asset)
	if repo == "" {
		return nil, RepoRequiredErr
	}
	// An empty asset means "use the release's only asset" — nothing to check.
	if asset == "" {
		return &apigen.RepoValidateResponse{Ok: true, Message: ""}, nil
	}

	prepare := &apigen.PrepareConfig{GithubRelease: apigen.GithubReleaseConfig{Repo: repo}}
	found, err := versionprovider.GHRel.AssetExists(ctx, prepare, asset)
	if err != nil {
		slog.Warn("github asset validation failed", "repo", repo, "asset", asset, "err", err)
		return &apigen.RepoValidateResponse{
			Ok:      false,
			Message: "Could not check releases. Verify the repository and that the configured GitHub token grants access.",
		}, nil
	}
	if !found {
		return &apigen.RepoValidateResponse{
			Ok:      false,
			Message: "No published release has an asset with this name.",
		}, nil
	}
	return &apigen.RepoValidateResponse{Ok: true, Message: "Asset found in a published release."}, nil
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

// --- Per-deployment YAML parsing ---

type yamlDeployment struct {
	Name        string       `yaml:"name"`
	Environment string       `yaml:"environment"`
	Machine     string       `yaml:"machine"`
	Prepare     *yamlPrepare `yaml:"prepare,omitempty"`
	Runner      *yamlRunner  `yaml:"runner,omitempty"`
}

type yamlPrepare struct {
	NixBuild       *yamlNixBuild       `yaml:"nixBuild,omitempty"`
	NixDockerBuild *yamlNixDockerBuild `yaml:"nixDockerBuild,omitempty"`
	GithubRelease  *yamlGithubRelease  `yaml:"githubRelease,omitempty"`
	ContainerImage *yamlContainerImage `yaml:"containerImage,omitempty"`
}

type yamlContainerImage struct {
	Image string `yaml:"image"`
}

type yamlNixBuild struct {
	Repo             string `yaml:"repo"`
	Flake            string `yaml:"flake"`
	OutputExecutable string `yaml:"outputExecutable,omitempty"`
}

type yamlNixDockerBuild struct {
	Repo  string `yaml:"repo"`
	Flake string `yaml:"flake"`
}

type yamlGithubRelease struct {
	Repo           string `yaml:"repo"`
	Asset          string `yaml:"asset,omitempty"`
	Tag            string `yaml:"tag,omitempty"`
	DownloadScript string `yaml:"downloadScript,omitempty"`
}

type yamlRunner struct {
	OsProcess *yamlOsProcess `yaml:"osProcess,omitempty"`
	Systemd   *yamlSystemd   `yaml:"systemd,omitempty"`
	Container *yamlContainer `yaml:"container,omitempty"`
}

type yamlContainer struct {
	User              string               `yaml:"user,omitempty"`
	Env               []yamlEnvVar         `yaml:"env,omitempty"`
	Command           []string             `yaml:"command,omitempty"`
	WorkingDir        string               `yaml:"workingDir,omitempty"`
	DataMountPath     string               `yaml:"dataMountPath,omitempty"`
	DisableDataVolume bool                 `yaml:"disableDataVolume,omitempty"`
	Mounts            []yamlContainerMount `yaml:"mounts,omitempty"`
}

type yamlContainerMount struct {
	Host      string `yaml:"host"`
	Container string `yaml:"container"`
	Readonly  bool   `yaml:"readonly,omitempty"`
}

type yamlOsProcess struct {
	WorkingDir string       `yaml:"workingDir,omitempty"`
	RunAs      string       `yaml:"runAs,omitempty"`
	Strategy   string       `yaml:"strategy,omitempty"`
	Env        []yamlEnvVar `yaml:"env,omitempty"`
}

type yamlEnvVar struct {
	Key   string `yaml:"key"`
	Value string `yaml:"value"`
}

type yamlSystemd struct {
	Name    string `yaml:"name"`
	BinPath string `yaml:"binPath"`
}

// parseDeploymentYaml parses a single-deployment YAML into a DeploymentSpec.
func parseDeploymentYaml(yamlContent string) (*apigen.DeploymentSpec, error) {
	var dep yamlDeployment
	if err := yaml.Unmarshal([]byte(yamlContent), &dep); err != nil {
		return nil, InvalidYAMLErr
	}

	prepare, err := toPrepareConfig(dep.Prepare)
	if err != nil {
		return nil, err
	}
	runnerCfg, err := toRunnerConfig(dep.Runner)
	if err != nil {
		return nil, err
	}
	if err := validateContainerPairing(dep.Prepare, dep.Runner); err != nil {
		return nil, err
	}

	return &apigen.DeploymentSpec{
		Prepare: *prepare,
		Runner:  runnerConfigValue(runnerCfg),
	}, nil
}

func toPrepareConfig(yp *yamlPrepare) (*apigen.PrepareConfig, error) {
	if yp == nil {
		return nil, invalidConfigErrf("prepare is required")
	}
	hasNix := yp.NixBuild != nil
	hasNixDocker := yp.NixDockerBuild != nil
	hasGH := yp.GithubRelease != nil
	hasContainer := yp.ContainerImage != nil
	set := 0
	for _, b := range []bool{hasNix, hasNixDocker, hasGH, hasContainer} {
		if b {
			set++
		}
	}
	if set == 0 {
		return nil, invalidConfigErrf("prepare: one of nixBuild, nixDockerBuild, githubRelease or containerImage must be set")
	}
	if set > 1 {
		return nil, invalidConfigErrf("prepare: only one of nixBuild, nixDockerBuild, githubRelease or containerImage may be set")
	}
	out := &apigen.PrepareConfig{}
	if hasNix {
		if yp.NixBuild.Repo == "" {
			return nil, invalidConfigErrf("prepare.nixBuild: repo is required")
		}
		if yp.NixBuild.Flake == "" {
			return nil, invalidConfigErrf("prepare.nixBuild: flake is required")
		}
		out.NixBuild = apigen.NixBuildConfig{
			Repo:             yp.NixBuild.Repo,
			Flake:            yp.NixBuild.Flake,
			OutputExecutable: yp.NixBuild.OutputExecutable,
		}
	}
	if hasNixDocker {
		if yp.NixDockerBuild.Repo == "" {
			return nil, invalidConfigErrf("prepare.nixDockerBuild: repo is required")
		}
		if yp.NixDockerBuild.Flake == "" {
			return nil, invalidConfigErrf("prepare.nixDockerBuild: flake is required")
		}
		out.NixDockerBuild = apigen.NixDockerBuildConfig{
			Repo:  yp.NixDockerBuild.Repo,
			Flake: yp.NixDockerBuild.Flake,
		}
	}
	if hasGH {
		if yp.GithubRelease.Repo == "" {
			return nil, invalidConfigErrf("prepare.githubRelease: repo is required")
		}
		out.GithubRelease = apigen.GithubReleaseConfig{
			Repo:           yp.GithubRelease.Repo,
			Asset:          yp.GithubRelease.Asset,
			Tag:            yp.GithubRelease.Tag,
			DownloadScript: yp.GithubRelease.DownloadScript,
		}
	}
	if hasContainer {
		if yp.ContainerImage.Image == "" {
			return nil, invalidConfigErrf("prepare.containerImage: image is required")
		}
		out.ContainerImage = apigen.ContainerImageConfig{Image: yp.ContainerImage.Image}
	}
	return out, nil
}

// validateContainerPairing enforces that the container image prepare and the
// container runner are used together — an image can only be run as a container,
// and the container runner can only run an image.
func validateContainerPairing(yp *yamlPrepare, yr *yamlRunner) error {
	prepareIsContainer := yp != nil && (yp.ContainerImage != nil || yp.NixDockerBuild != nil)
	runnerIsContainer := yr != nil && yr.Container != nil
	runnerIsOther := yr != nil && (yr.OsProcess != nil || yr.Systemd != nil)
	if prepareIsContainer && runnerIsOther {
		return invalidConfigErrf("container image prepare variants require the container runner (or no runner block)")
	}
	if runnerIsContainer && !prepareIsContainer {
		return invalidConfigErrf("runner.container requires prepare.containerImage or prepare.nixDockerBuild")
	}
	return nil
}

func toRunnerConfig(yr *yamlRunner) (*apigen.RunnerConfig, error) {
	if yr == nil {
		return nil, nil
	}
	hasOS := yr.OsProcess != nil
	hasSystemd := yr.Systemd != nil
	hasContainer := yr.Container != nil
	set := 0
	for _, b := range []bool{hasOS, hasSystemd, hasContainer} {
		if b {
			set++
		}
	}
	if set > 1 {
		return nil, invalidConfigErrf("runner: only one of osProcess, systemd or container may be set")
	}
	out := &apigen.RunnerConfig{}
	if hasOS {
		env, err := toEnvVars(yr.OsProcess.Env)
		if err != nil {
			return nil, err
		}
		out.OsProcess = apigen.OsProcessRunnerConfig{
			WorkingDir: yr.OsProcess.WorkingDir,
			RunAs:      yr.OsProcess.RunAs,
			Strategy:   yr.OsProcess.Strategy,
			Env:        env,
		}
	}
	if hasSystemd {
		if yr.Systemd.Name == "" {
			return nil, invalidConfigErrf("runner.systemd: name is required")
		}
		if yr.Systemd.BinPath == "" {
			return nil, invalidConfigErrf("runner.systemd: binPath is required")
		}
		out.Systemd = apigen.SystemdRunnerConfig{
			Name:    yr.Systemd.Name,
			BinPath: yr.Systemd.BinPath,
		}
	}
	if hasContainer {
		env, err := toEnvVars(yr.Container.Env)
		if err != nil {
			return nil, err
		}
		var mounts []*apigen.ContainerMount
		for _, m := range yr.Container.Mounts {
			if m.Host == "" || m.Container == "" {
				return nil, invalidConfigErrf("runner.container.mounts: host and container are both required")
			}
			mounts = append(mounts, &apigen.ContainerMount{
				Host:      m.Host,
				Container: m.Container,
				Readonly:  m.Readonly,
			})
		}
		out.Container = apigen.ContainerRunnerConfig{
			User:              yr.Container.User,
			Env:               env,
			Command:           yr.Container.Command,
			WorkingDir:        yr.Container.WorkingDir,
			DataMountPath:     yr.Container.DataMountPath,
			DisableDataVolume: yr.Container.DisableDataVolume,
			Mounts:            mounts,
		}
	}
	return out, nil
}

// toEnvVars validates and converts YAML env entries. Keys are trimmed and
// required; duplicate keys are rejected so the resulting process environment is
// unambiguous.
func toEnvVars(in []yamlEnvVar) ([]*apigen.EnvVar, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]*apigen.EnvVar, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, e := range in {
		key := strings.TrimSpace(e.Key)
		if key == "" {
			return nil, invalidConfigErrf("runner.osProcess.env: key is required")
		}
		if _, dup := seen[key]; dup {
			return nil, invalidConfigErrf("runner.osProcess.env: duplicate key %q", key)
		}
		seen[key] = struct{}{}
		out = append(out, &apigen.EnvVar{Key: key, Value: e.Value})
	}
	return out, nil
}

func runnerConfigValue(cfg *apigen.RunnerConfig) apigen.RunnerConfig {
	if cfg == nil {
		return apigen.RunnerConfig{}
	}
	return *cfg
}

func invalidConfigErrf(format string, args ...any) error {
	e := InvalidConfigErr
	msg := fmt.Sprintf(format, args...)
	e.InternalErr = msg
	e.DisplayErr = msg
	return e
}

// deploymentConfigToYaml converts a DeploymentConfig to per-deployment YAML.
func deploymentConfigToYaml(cfg *apigen.DeploymentConfig) string {
	dep := yamlDeployment{}
	if !cfg.ConfigID.IsZero() {
		dep.Name = cfg.ConfigID.Name
		dep.Environment = cfg.ConfigID.Environment
		dep.Machine = cfg.ConfigID.Machine
	}
	if !cfg.Spec.IsZero() {
		if !cfg.Spec.Prepare.IsZero() {
			dep.Prepare = &yamlPrepare{}
			if !cfg.Spec.Prepare.NixBuild.IsZero() {
				dep.Prepare.NixBuild = &yamlNixBuild{
					Repo:             cfg.Spec.Prepare.NixBuild.Repo,
					Flake:            cfg.Spec.Prepare.NixBuild.Flake,
					OutputExecutable: cfg.Spec.Prepare.NixBuild.OutputExecutable,
				}
			}
			if !cfg.Spec.Prepare.NixDockerBuild.IsZero() {
				dep.Prepare.NixDockerBuild = &yamlNixDockerBuild{
					Repo:  cfg.Spec.Prepare.NixDockerBuild.Repo,
					Flake: cfg.Spec.Prepare.NixDockerBuild.Flake,
				}
			}
			if !cfg.Spec.Prepare.GithubRelease.IsZero() {
				dep.Prepare.GithubRelease = &yamlGithubRelease{
					Repo:  cfg.Spec.Prepare.GithubRelease.Repo,
					Asset: cfg.Spec.Prepare.GithubRelease.Asset,
					Tag:   cfg.Spec.Prepare.GithubRelease.Tag,
				}
			}
			if !cfg.Spec.Prepare.ContainerImage.IsZero() {
				dep.Prepare.ContainerImage = &yamlContainerImage{Image: cfg.Spec.Prepare.ContainerImage.Image}
			}
		}
		if !cfg.Spec.Runner.IsZero() {
			dep.Runner = &yamlRunner{}
			if !cfg.Spec.Runner.OsProcess.IsZero() {
				dep.Runner.OsProcess = &yamlOsProcess{
					WorkingDir: cfg.Spec.Runner.OsProcess.WorkingDir,
					RunAs:      cfg.Spec.Runner.OsProcess.RunAs,
					Strategy:   cfg.Spec.Runner.OsProcess.Strategy,
				}
			}
			if !cfg.Spec.Runner.Systemd.IsZero() {
				dep.Runner.Systemd = &yamlSystemd{
					Name:    cfg.Spec.Runner.Systemd.Name,
					BinPath: cfg.Spec.Runner.Systemd.BinPath,
				}
			}
			if !cfg.Spec.Runner.Container.IsZero() {
				dep.Runner.Container = &yamlContainer{
					User:              cfg.Spec.Runner.Container.User,
					Command:           cfg.Spec.Runner.Container.Command,
					WorkingDir:        cfg.Spec.Runner.Container.WorkingDir,
					DataMountPath:     cfg.Spec.Runner.Container.DataMountPath,
					DisableDataVolume: cfg.Spec.Runner.Container.DisableDataVolume,
				}
				for _, e := range cfg.Spec.Runner.Container.Env {
					dep.Runner.Container.Env = append(dep.Runner.Container.Env, yamlEnvVar{Key: e.Key, Value: e.Value})
				}
				for _, m := range cfg.Spec.Runner.Container.Mounts {
					dep.Runner.Container.Mounts = append(dep.Runner.Container.Mounts, yamlContainerMount{Host: m.Host, Container: m.Container, Readonly: m.Readonly})
				}
			}
		}
	}
	out, err := yaml.Marshal(dep)
	if err != nil {
		return ""
	}
	return string(out)
}

// parseCreateDeploymentYaml parses YAML into a DeploymentIdentifier and DeploymentSpec for creation.
func parseCreateDeploymentYaml(yamlContent string) (*apigen.DeploymentIdentifier, *apigen.DeploymentSpec, error) {
	var dep yamlDeployment
	if err := yaml.Unmarshal([]byte(yamlContent), &dep); err != nil {
		return nil, nil, InvalidYAMLErr
	}

	if dep.Name == "" {
		return nil, nil, invalidConfigErrf("name is required")
	}
	if dep.Machine == "" {
		return nil, nil, invalidConfigErrf("machine is required")
	}

	prepare, err := toPrepareConfig(dep.Prepare)
	if err != nil {
		return nil, nil, err
	}
	runnerCfg, err := toRunnerConfig(dep.Runner)
	if err != nil {
		return nil, nil, err
	}
	if err := validateContainerPairing(dep.Prepare, dep.Runner); err != nil {
		return nil, nil, err
	}

	cid := &apigen.DeploymentIdentifier{
		Name:        dep.Name,
		Environment: dep.Environment,
		Machine:     dep.Machine,
	}
	spec := &apigen.DeploymentSpec{
		Prepare: *prepare,
		Runner:  runnerConfigValue(runnerCfg),
	}
	return cid, spec, nil
}
