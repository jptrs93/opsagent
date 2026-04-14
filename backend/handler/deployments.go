package handler

import (
	"fmt"
	"io"
	"net/http"
	"os"
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
		if cfg := h.findConfigByID(req.DeploymentID); cfg != nil && cfg.DesiredState != nil {
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
	if cfg == nil || cfg.Spec == nil || cfg.Spec.Prepare == nil {
		return nil, DeploymentNotFoundErr
	}

	provider, err := versionprovider.ForConfig(cfg.Spec.Prepare)
	if err != nil {
		return nil, DeploymentNotFoundErr
	}

	scopes, err := provider.ListScopes(ctx, cfg.Spec.Prepare)
	if err != nil {
		return nil, fmt.Errorf("listing scopes: %w", err)
	}

	versionsByScope := make(map[string]*apigen.ScopedVersions)

	if req.Scope != "" {
		// Fetch specific scope only.
		vs, err := provider.ListVersions(ctx, cfg.Spec.Prepare, req.Scope)
		if err != nil {
			return nil, fmt.Errorf("listing versions: %w", err)
		}
		versionsByScope[req.Scope] = &apigen.ScopedVersions{Versions: vs}
	} else if len(scopes) == 0 {
		// GitHub releases: no scopes, single version list.
		vs, err := provider.ListVersions(ctx, cfg.Spec.Prepare, "")
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
		vs, err := provider.ListVersions(ctx, cfg.Spec.Prepare, defaultScope)
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
	if cfg != nil && cfg.ConfigID != nil && cfg.ConfigID.Machine != "" && cfg.ConfigID.Machine != h.MachineName && h.ClusterPrimary != nil {
		clusterReq := &apigen.MsgToWorker{DeploymentLogRequest: req}
		return h.proxyRemoteLogs(ctx, w, cfg.ConfigID.Machine, clusterReq)
	}

	// Resolve seqNo=0 to latest from local status.
	if req.RunnerOutput != nil {
		if req.RunnerOutput.Version == 0 {
			st := h.Store.FetchDeploymentStatus(deploymentID)
			if st != nil && st.Runner != nil {
				req.RunnerOutput.Version = st.Runner.DeploymentConfigVersion
			}
		}
		return h.streamRunLog(ctx, w, req.RunnerOutput)
	}
	if req.PreparerOutput.Version == 0 {
		st := h.Store.FetchDeploymentStatus(deploymentID)
		if st != nil && st.Preparer != nil {
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
		return st != nil && st.Runner != nil && isRunnerActive(st.Runner.Status)
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
		return st != nil && st.Preparer != nil && isPrepareInProgress(st.Preparer.Status)
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
	snapshot, _ := h.Store.MustFetchSnapshotAndSubscribe(nil, "")
	for _, dws := range snapshot {
		if dws.Config.ID == deploymentID {
			return dws.Config
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
	NixBuild      *yamlNixBuild      `yaml:"nixBuild,omitempty"`
	GithubRelease *yamlGithubRelease `yaml:"githubRelease,omitempty"`
}

type yamlNixBuild struct {
	Repo             string `yaml:"repo"`
	Flake            string `yaml:"flake"`
	OutputExecutable string `yaml:"outputExecutable,omitempty"`
}

type yamlGithubRelease struct {
	Repo  string `yaml:"repo"`
	Asset string `yaml:"asset,omitempty"`
	Tag   string `yaml:"tag,omitempty"`
}

type yamlRunner struct {
	OsProcess *yamlOsProcess `yaml:"osProcess,omitempty"`
	Systemd   *yamlSystemd   `yaml:"systemd,omitempty"`
}

type yamlOsProcess struct {
	WorkingDir string `yaml:"workingDir,omitempty"`
	RunAs      string `yaml:"runAs,omitempty"`
	Strategy   string `yaml:"strategy,omitempty"`
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

	return &apigen.DeploymentSpec{
		Prepare: prepare,
		Runner:  runnerCfg,
	}, nil
}

func toPrepareConfig(yp *yamlPrepare) (*apigen.PrepareConfig, error) {
	if yp == nil {
		return nil, invalidConfigErrf("prepare is required")
	}
	hasNix := yp.NixBuild != nil
	hasGH := yp.GithubRelease != nil
	if !hasNix && !hasGH {
		return nil, invalidConfigErrf("prepare: one of nixBuild or githubRelease must be set")
	}
	if hasNix && hasGH {
		return nil, invalidConfigErrf("prepare: only one of nixBuild or githubRelease may be set")
	}
	out := &apigen.PrepareConfig{}
	if hasNix {
		if yp.NixBuild.Repo == "" {
			return nil, invalidConfigErrf("prepare.nixBuild: repo is required")
		}
		if yp.NixBuild.Flake == "" {
			return nil, invalidConfigErrf("prepare.nixBuild: flake is required")
		}
		out.NixBuild = &apigen.NixBuildConfig{
			Repo:             yp.NixBuild.Repo,
			Flake:            yp.NixBuild.Flake,
			OutputExecutable: yp.NixBuild.OutputExecutable,
		}
	}
	if hasGH {
		if yp.GithubRelease.Repo == "" {
			return nil, invalidConfigErrf("prepare.githubRelease: repo is required")
		}
		out.GithubRelease = &apigen.GithubReleaseConfig{
			Repo:  yp.GithubRelease.Repo,
			Asset: yp.GithubRelease.Asset,
			Tag:   yp.GithubRelease.Tag,
		}
	}
	return out, nil
}

func toRunnerConfig(yr *yamlRunner) (*apigen.RunnerConfig, error) {
	if yr == nil {
		return nil, nil
	}
	hasOS := yr.OsProcess != nil
	hasSystemd := yr.Systemd != nil
	if hasOS && hasSystemd {
		return nil, invalidConfigErrf("runner: only one of osProcess or systemd may be set")
	}
	out := &apigen.RunnerConfig{}
	if hasOS {
		out.OsProcess = &apigen.OsProcessRunnerConfig{
			WorkingDir: yr.OsProcess.WorkingDir,
			RunAs:      yr.OsProcess.RunAs,
			Strategy:   yr.OsProcess.Strategy,
		}
	}
	if hasSystemd {
		if yr.Systemd.Name == "" {
			return nil, invalidConfigErrf("runner.systemd: name is required")
		}
		if yr.Systemd.BinPath == "" {
			return nil, invalidConfigErrf("runner.systemd: binPath is required")
		}
		out.Systemd = &apigen.SystemdRunnerConfig{
			Name:    yr.Systemd.Name,
			BinPath: yr.Systemd.BinPath,
		}
	}
	return out, nil
}

func invalidConfigErrf(format string, args ...any) error {
	e := InvalidConfigErr
	e.InternalErr = fmt.Sprintf(format, args...)
	return e
}

// deploymentConfigToYaml converts a DeploymentConfig to per-deployment YAML.
func deploymentConfigToYaml(cfg *apigen.DeploymentConfig) string {
	dep := yamlDeployment{}
	if cfg.ConfigID != nil {
		dep.Name = cfg.ConfigID.Name
		dep.Environment = cfg.ConfigID.Environment
		dep.Machine = cfg.ConfigID.Machine
	}
	if cfg.Spec != nil {
		if cfg.Spec.Prepare != nil {
			dep.Prepare = &yamlPrepare{}
			if cfg.Spec.Prepare.NixBuild != nil {
				dep.Prepare.NixBuild = &yamlNixBuild{
					Repo:             cfg.Spec.Prepare.NixBuild.Repo,
					Flake:            cfg.Spec.Prepare.NixBuild.Flake,
					OutputExecutable: cfg.Spec.Prepare.NixBuild.OutputExecutable,
				}
			}
			if cfg.Spec.Prepare.GithubRelease != nil {
				dep.Prepare.GithubRelease = &yamlGithubRelease{
					Repo:  cfg.Spec.Prepare.GithubRelease.Repo,
					Asset: cfg.Spec.Prepare.GithubRelease.Asset,
					Tag:   cfg.Spec.Prepare.GithubRelease.Tag,
				}
			}
		}
		if cfg.Spec.Runner != nil {
			dep.Runner = &yamlRunner{}
			if cfg.Spec.Runner.OsProcess != nil {
				dep.Runner.OsProcess = &yamlOsProcess{
					WorkingDir: cfg.Spec.Runner.OsProcess.WorkingDir,
					RunAs:      cfg.Spec.Runner.OsProcess.RunAs,
					Strategy:   cfg.Spec.Runner.OsProcess.Strategy,
				}
			}
			if cfg.Spec.Runner.Systemd != nil {
				dep.Runner.Systemd = &yamlSystemd{
					Name:    cfg.Spec.Runner.Systemd.Name,
					BinPath: cfg.Spec.Runner.Systemd.BinPath,
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
