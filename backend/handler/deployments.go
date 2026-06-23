package handler

import (
	"context"
	"fmt"
	"io"
	"iter"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/engine/logfilter"
	"github.com/jptrs93/opsagent/backend/engine/logreader"
	"github.com/jptrs93/opsagent/backend/engine/preparer"
	"github.com/jptrs93/opsagent/backend/engine/versionprovider"
	"github.com/jptrs93/opsagent/backend/secrets"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

var InvalidRequestBodyErr = apigen.NewApiErr("Invalid request body", "invalid_request_body", http.StatusBadRequest)
var MissingKeyErr = apigen.NewApiErr("Missing deployment identifier", "missing_key", http.StatusBadRequest)
var NoPrepareOutputErr = apigen.NewApiErr("No prepare output found", "prepare_output_not_found", http.StatusNotFound)
var InvalidConfigErr = apigen.NewApiErr("", "invalid_config", http.StatusBadRequest)
var DeploymentNotFoundErr = apigen.NewApiErr("Deployment not found", "deployment_not_found", http.StatusNotFound)

var DuplicateDeploymentErr = apigen.NewApiErr("A deployment with this name, space, and machine already exists", "duplicate_deployment", http.StatusConflict)

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

	cfg := h.Store.MustCreateDeployment(ctx, &cid, spec, req.DesiredState)
	return cfg, nil
}

func (h *Handler) PostV1DeploymentUpdate(ctx apigen.Context, req *apigen.DeploymentUpdateRequest) (*apigen.DesiredState, error) {
	if req.DeploymentID == 0 {
		return nil, MissingKeyErr
	}
	cfg := h.findConfigByID(req.DeploymentID)
	if cfg == nil || cfg.Deleted {
		return nil, DeploymentNotFoundErr
	}
	if req.Version != cfg.Version+1 {
		return nil, invalidConfigErrf("deployment version mismatch: got %d, want %d", req.Version, cfg.Version+1)
	}
	if (req.SpaceID != nil || !req.Spec.IsZero()) && sqlite.IsSystemDeploymentConfig(cfg) {
		return nil, invalidConfigErrf("opendeploy system deployment identity and spec are internal-only")
	}

	var spec *apigen.DeploymentSpec
	if req.SpaceID != nil {
		if cfg.ConfigID.SpaceID != *req.SpaceID {
			nextID := cfg.ConfigID
			nextID.SpaceID = *req.SpaceID
			for _, dws := range h.Store.FetchDeploymentSnapshot("") {
				if dws.Config.ID != req.DeploymentID && dws.Config.ConfigID == nextID && !dws.Config.Deleted {
					return nil, DuplicateDeploymentErr
				}
			}
		}
	}

	if !req.Spec.IsZero() {
		validated, err := h.validateDeploymentSpec(&req.Spec)
		if err != nil {
			return nil, err
		}
		spec = validated
	}

	desired := apigen.DesiredState{}
	var desiredUpdate *apigen.DesiredState
	if req.Stop {
		desired.Running = false
		// Preserve the existing version so a subsequent "start" can reuse it.
		desired.Version = cfg.DesiredState.Version
		desiredUpdate = &desired
	} else if req.TargetVersion != "" {
		desired.Version = req.TargetVersion
		desired.Running = true
		desiredUpdate = &desired
	}

	if req.SpaceID != nil || spec != nil || desiredUpdate != nil {
		current, _, versionOK := h.Store.UpdateDeploymentConfig(ctx, req.DeploymentID, sqlite.DeploymentConfigUpdate{
			ExpectedVersion: req.Version,
			SpaceID:         req.SpaceID,
			Spec:            spec,
			DesiredState:    desiredUpdate,
		})
		if !versionOK {
			return nil, invalidConfigErrf("deployment version mismatch: got %d, want %d", req.Version, current.Version+1)
		}
	}

	return &desired, nil
}

func (h *Handler) PostV1DeploymentDelete(ctx apigen.Context, req *apigen.DeploymentDeleteRequest) error {
	if req.DeploymentID == 0 {
		return MissingKeyErr
	}
	cfg := h.findConfigByID(req.DeploymentID)
	if cfg == nil || cfg.Deleted {
		return DeploymentNotFoundErr
	}
	if sqlite.IsSystemDeploymentConfig(cfg) {
		return invalidConfigErrf("opendeploy system deployment is internal-only")
	}
	if req.Version != cfg.Version+1 {
		return invalidConfigErrf("deployment version mismatch: got %d, want %d", req.Version, cfg.Version+1)
	}
	status := h.Store.FetchDeploymentStatus(req.DeploymentID)
	if status == nil || status.Runner.Status != apigen.RunningStatus_STOPPED {
		return invalidConfigErrf("deployment must be stopped before deletion")
	}
	deleted := true
	desired := apigen.DesiredState{Version: cfg.DesiredState.Version, Running: false}
	_, _, versionOK := h.Store.UpdateDeploymentConfig(ctx, req.DeploymentID, sqlite.DeploymentConfigUpdate{
		ExpectedVersion: req.Version,
		DesiredState:    &desired,
		Deleted:         &deleted,
	})
	if !versionOK {
		return invalidConfigErrf("deployment version mismatch: got %d, want %d", req.Version, cfg.Version+1)
	}
	return nil
}

func (h *Handler) PostV1DeploymentVersions(ctx apigen.Context, req *apigen.DeploymentVersionsRequest) (*apigen.DeploymentVersions, error) {
	if req.DeploymentID == 0 {
		return nil, MissingKeyErr
	}

	cfg := h.findConfigByID(req.DeploymentID)
	if cfg == nil || cfg.Spec.Prepare.IsZero() {
		return nil, DeploymentNotFoundErr
	}

	switch {
	case cfg.Spec.Prepare.NixDockerBuild != nil:
		if versionprovider.Git == nil {
			return nil, fmt.Errorf("git version loading is not configured")
		}
		repo := cfg.Spec.Prepare.NixDockerBuild.Repo
		branches, err := versionprovider.Git.ListBranches(ctx, repo)
		if err != nil {
			return nil, fmt.Errorf("listing branches: %w", err)
		}
		branch := selectedValidationBranch(branches, req.SelectedBranch)
		commits := []*apigen.Version{}
		if branch != "" {
			commits, err = versionprovider.Git.ListCommits(ctx, repo, branch, 25)
			if err != nil {
				return nil, fmt.Errorf("listing commits: %w", err)
			}
		}
		return &apigen.DeploymentVersions{
			DeploymentID: req.DeploymentID,
			NixDockerBuild: &apigen.DeploymentNixDockerBuildVersions{
				Branches:       branches,
				SelectedBranch: branch,
				Commits:        commits,
			},
		}, nil
	case cfg.Spec.Prepare.GithubRelease != nil:
		if versionprovider.GHRel == nil {
			return nil, fmt.Errorf("github release version loading is not configured")
		}
		releases, err := versionprovider.GHRel.ListReleases(ctx, cfg.Spec.Prepare.GithubRelease.Repo)
		if err != nil {
			return nil, fmt.Errorf("listing releases: %w", err)
		}
		return &apigen.DeploymentVersions{
			DeploymentID:  req.DeploymentID,
			GithubRelease: &apigen.DeploymentGithubReleaseVersions{Releases: releases},
		}, nil
	case cfg.Spec.Prepare.ContainerImage != nil:
		tags, err := (versionprovider.ContainerImageVersionProvider{}).ListTags(ctx, cfg.Spec.Prepare.ContainerImage.Image)
		if err != nil {
			return nil, fmt.Errorf("listing container image tags: %w", err)
		}
		return &apigen.DeploymentVersions{
			DeploymentID:   req.DeploymentID,
			ContainerImage: &apigen.DeploymentContainerImageVersions{Tags: tags},
		}, nil
	default:
		return nil, DeploymentNotFoundErr
	}
}

func (h *Handler) PostV1DeploymentLogSearch(ctx apigen.Context, req *apigen.LogSearchRequest) iter.Seq2[*apigen.LogLineBatch, error] {
	return func(yield func(*apigen.LogLineBatch, error) bool) {
		if req.TimeStart.IsZero() {
			yield(nil, invalidConfigErrf("timeStart is required"))
			return
		}
		if req.ConfigVersion < 0 {
			yield(nil, invalidConfigErrf("configVersion must not be negative"))
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
				streamRemoteLogSearch(stream.Seq(), req, yield)
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
			streamRemoteLogSearch(stream.Seq(), req, yield)
			return
		}

		streamLocalLogSearch(req, till, yield)
	}
}

func streamRemoteLogSearch(seq iter.Seq2[*apigen.LogLineBatch, error], req *apigen.LogSearchRequest, yield func(*apigen.LogLineBatch, error) bool) {
	count := 0
	limit := logSearchLimit(req)
	for batch, err := range seq {
		if err != nil {
			yield(nil, err)
			return
		}
		if batch == nil || (len(batch.Lines) == 0 && batch.LogDir == "") {
			continue
		}
		if len(batch.Lines) == 0 {
			if !yield(batch, nil) {
				return
			}
			continue
		}
		batch = filterLogLineBatch(batch, req)
		if len(batch.Lines) == 0 {
			continue
		}
		if limit > 0 && count+len(batch.Lines) > limit {
			remaining := limit - count
			if remaining <= 0 {
				return
			}
			batch = &apigen.LogLineBatch{Lines: batch.Lines[:remaining]}
		}
		if !yield(batch, nil) {
			return
		}
		count += len(batch.Lines)
		if limit > 0 && count >= limit {
			return
		}
	}
}

func streamLocalLogSearch(req *apigen.LogSearchRequest, till *time.Time, yield func(*apigen.LogLineBatch, error) bool) {
	count := 0
	limit := logSearchLimit(req)
	if !yield(&apigen.LogLineBatch{LogDir: apigen.RunOutputDeploymentDir(req.DeploymentID)}, nil) {
		return
	}
	batch := make([]*apigen.LogLine, 0, logSearchBatchSize)
	flush := func() bool {
		if len(batch) == 0 {
			return true
		}
		lines := batch
		batch = make([]*apigen.LogLine, 0, logSearchBatchSize)
		return yield(&apigen.LogLineBatch{Lines: lines}, nil)
	}
	for line, err := range logreader.StreamLogs(int(req.DeploymentID), int(req.ConfigVersion), req.TimeStart, till) {
		if err != nil {
			yield(nil, err)
			return
		}
		if !logfilter.Match(line.Line, req.SearchStr, req.LevelMin) {
			continue
		}
		apiLine := toAPILogLine(line)
		batch = append(batch, apiLine)
		if len(batch) >= logSearchBatchSize && !flush() {
			return
		}
		count++
		if limit > 0 && count >= limit {
			flush()
			return
		}
	}
	flush()
}

func filterLogLineBatch(batch *apigen.LogLineBatch, req *apigen.LogSearchRequest) *apigen.LogLineBatch {
	if req == nil || (req.SearchStr == "" && req.LevelMin == "") || batch == nil || len(batch.Lines) == 0 {
		return batch
	}
	lines := make([]*apigen.LogLine, 0, len(batch.Lines))
	for _, line := range batch.Lines {
		if line != nil && logfilter.Match(line.Line, req.SearchStr, req.LevelMin) {
			lines = append(lines, line)
		}
	}
	return &apigen.LogLineBatch{Lines: lines, LogDir: batch.LogDir}
}

const logSearchBatchSize = 256

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

func toAPILogLine(line logreader.LogLine) *apigen.LogLine {
	return &apigen.LogLine{
		Time:    line.Time,
		Version: line.Version,
		Run:     line.Run,
		Stream:  int32(line.Stream),
		Line:    line.Line,
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

type deploymentSecretLister interface {
	List() []secrets.Meta
}

type deploymentConfigResolver interface {
	ResolveConfig(id int32) (string, bool)
}

func (h *Handler) validateDeploymentSpec(spec *apigen.DeploymentSpec) (*apigen.DeploymentSpec, error) {
	return validateDeploymentSpecWithResolvers(spec, h.Store, h.Secrets, h.Store)
}

func validateDeploymentSpecWithAssets(spec *apigen.DeploymentSpec, assets deploymentAssetResolver) (*apigen.DeploymentSpec, error) {
	return validateDeploymentSpecWithResolvers(spec, assets, nil, nil)
}

func validateDeploymentSpecWithResolvers(spec *apigen.DeploymentSpec, assets deploymentAssetResolver, secretStore deploymentSecretLister, configs deploymentConfigResolver) (*apigen.DeploymentSpec, error) {
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
	if err := validateRuntimeEnvRefs(&out, secretStore, configs); err != nil {
		return nil, err
	}
	return &out, nil
}

func validateRuntimeEnvRefs(spec *apigen.DeploymentSpec, secretStore deploymentSecretLister, configs deploymentConfigResolver) error {
	if spec == nil || spec.Runner.Container.IsZero() || len(spec.Runner.Container.EnvVars) == 0 {
		return nil
	}
	cfg := &apigen.DeploymentConfig{Spec: *spec}
	knownSecrets := map[int32]bool{}
	if secretStore != nil {
		for _, meta := range secretStore.List() {
			knownSecrets[meta.ID] = true
		}
	}
	for _, id := range preparer.SecretRefs(cfg) {
		if secretStore == nil {
			return invalidConfigErrf("runner.container.envVars: secrets cannot be resolved here")
		}
		if !knownSecrets[id] {
			return invalidConfigErrf("runner.container.envVars: unknown secret id %d", id)
		}
	}
	for _, id := range preparer.ConfigRefs(cfg) {
		if configs == nil {
			return invalidConfigErrf("runner.container.envVars: configs cannot be resolved here")
		}
		if _, ok := configs.ResolveConfig(id); !ok {
			return invalidConfigErrf("runner.container.envVars: unknown config id %d", id)
		}
	}
	return nil
}

func validatePrepareConfig(prepare *apigen.PrepareConfig) error {
	if prepare == nil || prepare.IsZero() {
		return invalidConfigErrf("prepare is required")
	}
	hasNixDocker := prepare.NixDockerBuild != nil
	hasGH := prepare.GithubRelease != nil
	hasContainer := prepare.ContainerImage != nil
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
		validateContainerCommand(&runner.Container)
		if err := validateEnvVars("runner.container.envVars", runner.Container.EnvVars); err != nil {
			return err
		}
		if err := validateContainerUpgrade(&runner.Container); err != nil {
			return err
		}
		if err := resolveEnvAssetRefs("runner.container.envVars", runner.Container.EnvVars, assets); err != nil {
			return err
		}
		if err := validateContainerMounts(runner.Container.Mounts); err != nil {
			return err
		}
		assetMounts, err := resolveAssetMounts(runner.Container.AssetMounts, assets)
		if err != nil {
			return err
		}
		runner.Container.AssetMounts = assetMounts
	}
	return nil
}

func validateContainerCommand(cfg *apigen.ContainerRunnerConfig) {
	if cfg == nil || len(cfg.Command) == 0 {
		return
	}
	out := make([]string, 0, len(cfg.Command))
	for _, arg := range cfg.Command {
		arg = strings.TrimSpace(arg)
		if arg != "" {
			out = append(out, arg)
		}
	}
	cfg.Command = out
}

func validateContainerUpgrade(cfg *apigen.ContainerRunnerConfig) error {
	if cfg == nil {
		return nil
	}
	switch cfg.UpgradeStrategy {
	case apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_UNSPECIFIED:
		cfg.UpgradeStrategy = apigen.ContainerUpgradeStrategy_RECREATE
	case apigen.ContainerUpgradeStrategy_RECREATE:
		cfg.ReadinessSignal = nil
	case apigen.ContainerUpgradeStrategy_ROLLOVER:
		if cfg.EnvVars != nil {
			if _, ok := cfg.EnvVars["OPENDEPLOY_READINESS_SOCK_PATH"]; ok {
				return invalidConfigErrf("runner.container.envVars: OPENDEPLOY_READINESS_SOCK_PATH is reserved for rollover readiness")
			}
		}
		if cfg.ReadinessSignal == nil {
			cfg.ReadinessSignal = &apigen.ContainerReadinessSignal{}
		}
		if cfg.ReadinessSignal.TimeoutSeconds < 0 {
			return invalidConfigErrf("runner.container.readinessSignal.timeoutSeconds must be non-negative")
		}
	default:
		return invalidConfigErrf("runner.container.upgradeStrategy: unsupported value %d", cfg.UpgradeStrategy)
	}
	return nil
}

func validateContainerMounts(mounts []*apigen.ContainerMount) error {
	for _, m := range mounts {
		if m == nil || strings.TrimSpace(m.Host) == "" || strings.TrimSpace(m.Container) == "" {
			return invalidConfigErrf("runner.container.mounts: host and container are both required")
		}
		host := strings.TrimSpace(m.Host)
		container := strings.TrimSpace(m.Container)
		if !filepath.IsAbs(host) {
			return invalidConfigErrf("runner.container.mounts: host path must be absolute")
		}
		if !filepath.IsAbs(container) {
			return invalidConfigErrf("runner.container.mounts: container path must be absolute")
		}
		if filepath.Clean(host) != host || host == "/" {
			return invalidConfigErrf("runner.container.mounts: host path must be a clean absolute path")
		}
		if filepath.Clean(container) != container || container == "/" {
			return invalidConfigErrf("runner.container.mounts: container path must be a clean absolute path")
		}
		m.Host = host
		m.Container = container
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
			Asset:      asset.Key,
			Version:    asset.Version,
			Path:       cleanPath,
			Format:     asset.Format,
			AssetID:    asset.ID,
			Executable: m.Executable,
		})
	}
	return out, nil
}

func resolveEnvAssetRefs(scope string, env map[string]*apigen.EnvVarValue, assets deploymentAssetResolver) error {
	for key, value := range env {
		assetKey := strings.TrimSpace(value.Asset)
		if assetKey == "" {
			continue
		}
		if assets == nil {
			return invalidConfigErrf("%s.%s: assets cannot be resolved here", scope, key)
		}
		asset, ok := assets.GetAsset(assetKey, value.Version)
		if !ok {
			if value.Version > 0 {
				return invalidConfigErrf("%s.%s: asset %q version %d not found", scope, key, assetKey, value.Version)
			}
			return invalidConfigErrf("%s.%s: asset %q not found", scope, key, assetKey)
		}
		value.Asset = asset.Key
		value.AssetID = asset.ID
		value.Version = asset.Version
	}
	return nil
}

// validateEnvVars trims and validates env keys and typed values. Duplicate keys
// after trimming are rejected so the resulting process environment is unambiguous.
func validateEnvVars(scope string, in map[string]*apigen.EnvVarValue) error {
	seen := make(map[string]struct{}, len(in))
	out := make(map[string]*apigen.EnvVarValue, len(in))
	for rawKey, value := range in {
		key := strings.TrimSpace(rawKey)
		if key == "" {
			return invalidConfigErrf("%s: key is required", scope)
		}
		if _, dup := seen[key]; dup {
			return invalidConfigErrf("%s: duplicate key %q", scope, key)
		}
		seen[key] = struct{}{}
		if value == nil {
			return invalidConfigErrf("%s.%s: value is required", scope, key)
		}
		set := 0
		if value.Value != nil {
			set++
		}
		if value.SecretID != nil {
			set++
			if *value.SecretID <= 0 {
				return invalidConfigErrf("%s.%s: secretId must be positive", scope, key)
			}
		}
		if value.ConfigID != nil {
			set++
			if *value.ConfigID <= 0 {
				return invalidConfigErrf("%s.%s: configId must be positive", scope, key)
			}
		}
		if strings.TrimSpace(value.Asset) != "" {
			set++
		}
		if set != 1 {
			return invalidConfigErrf("%s.%s: exactly one of value, secretId, configId, or asset is required", scope, key)
		}
		out[key] = value
	}
	for key := range in {
		delete(in, key)
	}
	for key, value := range out {
		in[key] = value
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
