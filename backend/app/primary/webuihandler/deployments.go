package webuihandler

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"iter"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/lib/engine/versionprovider"
	"github.com/jptrs93/opsagent/backend/lib/log/logfilter"
	"github.com/jptrs93/opsagent/backend/lib/log/logreader"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

var InvalidRequestBodyErr = apigen.NewApiErr("Invalid request body", "invalid_request_body", http.StatusBadRequest)
var MissingKeyErr = apigen.NewApiErr("Missing deployment identifier", "missing_key", http.StatusBadRequest)
var NoPrepareOutputErr = apigen.NewApiErr("No prepare output found", "prepare_output_not_found", http.StatusNotFound)
var DeploymentNotFoundErr = apigen.NewApiErr("Deployment not found", "deployment_not_found", http.StatusNotFound)

var DuplicateDeploymentErr = apigen.NewApiErr("A deployment with this name, space, and node already exists", "duplicate_deployment", http.StatusConflict)

const githubReleaseVersionsDisplayErr = "Releases could not be loaded from GitHub. Please try again."

func (h *Handler) PostV1DeploymentCreate(ctx apigen.Context, req *apigen.DeploymentCreateRequest) (*apigen.DeploymentConfig, error) {
	identity := req.Identity
	if identity.Name == "" {
		return nil, invalidConfigErrf("name is required")
	}
	if req.NodeID <= 0 {
		return nil, invalidConfigErrf("nodeId is required")
	}
	_, err := h.Store.NodeIdentifierByID(req.NodeID)
	if err != nil {
		return nil, invalidConfigErrf("node is not registered")
	}
	if internaldeploy.IsInternalIdentity(identity) {
		return nil, invalidConfigErrf("opendeploy system deployment identity is internal-only")
	}
	if identity.SpaceID < 0 || identity.SpaceID > network.MaxSpaceID {
		return nil, invalidConfigErrf("spaceId must be between 0 and %d", network.MaxSpaceID)
	}
	spec, err := h.validateDeploymentSpec(&req.Spec)
	if err != nil {
		return nil, err
	}
	if err := h.validateCrossDeploymentMountSources(spec, req.NodeID, 0); err != nil {
		return nil, err
	}

	// Check for duplicate before creating.
	snapshot := h.Store.FetchDeploymentSnapshot(nil)
	for _, cfg := range snapshot {
		if storage.DeploymentKeyMatches(cfg, req.NodeID, identity) && !cfg.Deleted {
			return nil, DuplicateDeploymentErr
		}
	}
	if err := h.validateNodeNetworkingClaims(req.NodeID, 0, spec); err != nil {
		return nil, err
	}
	if err := h.validateAddressEnvRefs(req.NodeID, 0, spec, snapshot); err != nil {
		return nil, err
	}
	if err := validateNixWorkloadVersion(spec); err != nil {
		return nil, err
	}
	if spec.WorkloadRunning() {
		if err := h.verifyRunningNixSource(ctx, spec); err != nil {
			return nil, err
		}
	}

	cfg := h.Store.MustCreateDeploymentForNode(ctx, &identity, req.NodeID, spec)
	return cfg, nil
}

func (h *Handler) PostV1DeploymentUpdate(ctx apigen.Context, req *apigen.DeploymentUpdateRequest) (*apigen.DeploymentConfig, error) {
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
	if (req.SpaceID != nil || !req.Spec.IsZero()) && sqlite.IsInternalDeploymentConfig(cfg) {
		return nil, invalidConfigErrf("opendeploy system deployment identity and spec are internal-only")
	}

	var spec *apigen.DeploymentSpec
	if req.SpaceID != nil {
		if *req.SpaceID < 0 || *req.SpaceID > network.MaxSpaceID {
			return nil, invalidConfigErrf("spaceId must be between 0 and %d", network.MaxSpaceID)
		}
		nextIdentity := cfg.Identity
		nextIdentity.SpaceID = *req.SpaceID
		if internaldeploy.IsInternalIdentity(nextIdentity) {
			return nil, invalidConfigErrf("opendeploy system deployment identity is internal-only")
		}
		if cfg.Identity.SpaceID != *req.SpaceID {
			for _, other := range h.Store.FetchDeploymentSnapshot(nil) {
				if other.ID != req.DeploymentID && storage.DeploymentKeyMatches(other, cfg.NodeID, nextIdentity) && !other.Deleted {
					return nil, DuplicateDeploymentErr
				}
			}
		}
		if cfg.Identity.SpaceID != *req.SpaceID && h.deploymentUsesAddressID(int32Set([]int32{cfg.ID})) {
			// TODO: Rewrite dependent address refs and roll their runners in one coordinated migration.
			return nil, invalidConfigErrf("deployment space cannot change while address references exist")
		}
	}

	if !req.Spec.IsZero() {
		validated, err := h.validateDeploymentSpec(&req.Spec)
		if err != nil {
			return nil, err
		}
		if err := h.validateNodeNetworkingClaims(cfg.NodeID, cfg.ID, validated); err != nil {
			return nil, err
		}
		if err := h.validateCrossDeploymentMountSources(validated, cfg.NodeID, cfg.ID); err != nil {
			return nil, err
		}
		if err := h.validateAddressEnvRefs(cfg.NodeID, cfg.ID, validated, h.Store.FetchDeploymentSnapshot(nil)); err != nil {
			return nil, err
		}
		if validated.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL && h.deploymentUsesAddressID(int32Set([]int32{cfg.ID})) {
			return nil, invalidConfigErrf("deployment networking cannot leave virtual mode while address references exist")
		}
		spec = validated
	}

	effectiveSpec, err := cloneDeploymentSpec(&cfg.Spec)
	if err != nil {
		return nil, err
	}
	if spec != nil {
		effectiveSpec = spec
	}
	stateChanged := false
	if req.Stop {
		// Preserve the existing version so a subsequent "start" can reuse it.
		if err := effectiveSpec.SetWorkloadState(cfg.WorkloadVersion(), false); err != nil {
			return nil, invalidConfigErrf("spec: %v", err)
		}
		stateChanged = true
	} else if req.TargetVersion != "" {
		if err := effectiveSpec.SetWorkloadState(req.TargetVersion, true); err != nil {
			return nil, invalidConfigErrf("spec: %v", err)
		}
		stateChanged = true
	}

	desiredVersionSourceChanged := !sameDesiredVersionSource(&cfg.Spec, effectiveSpec)
	if !effectiveSpec.WorkloadRunning() && desiredVersionSourceChanged {
		if err := effectiveSpec.SetWorkloadState("", false); err != nil {
			return nil, invalidConfigErrf("spec: %v", err)
		}
		stateChanged = true
	}
	nixChanged := !sameNixBuildConfig(nixSource(&cfg.Spec), nixSource(effectiveSpec))
	if nixSource(effectiveSpec) != nil && !req.Stop && (nixChanged || stateChanged || spec != nil) {
		if err := validateNixWorkloadVersion(effectiveSpec); err != nil {
			return nil, err
		}
	}
	if effectiveSpec.WorkloadRunning() && nixSource(effectiveSpec) != nil &&
		(!cfg.WorkloadRunning() || effectiveSpec.WorkloadVersion() != cfg.WorkloadVersion() || nixChanged) {
		if err := h.verifyRunningNixSource(ctx, effectiveSpec); err != nil {
			return nil, err
		}
	}

	if req.SpaceID != nil || spec != nil || stateChanged {
		current, _, versionOK := h.Store.UpdateDeploymentConfig(ctx, req.DeploymentID, sqlite.DeploymentConfigUpdate{
			ExpectedVersion: req.Version,
			SpaceID:         req.SpaceID,
			Spec:            effectiveSpec,
		})
		if !versionOK {
			return nil, invalidConfigErrf("deployment version mismatch: got %d, want %d", req.Version, current.Version+1)
		}
		return current, nil
	}

	return cfg, nil
}

func (h *Handler) PostV1DeploymentUpgradeAll(ctx apigen.Context, req *apigen.DeploymentUpgradeAllRequest) (*apigen.DeploymentConfig, error) {
	targetVersion := strings.TrimSpace(req.TargetVersion)
	if targetVersion == "" {
		return nil, invalidConfigErrf("targetVersion is required")
	}

	var primary *apigen.DeploymentConfig
	var secondaryAndNetproxy []*apigen.DeploymentConfig
	for _, cfg := range h.Store.ListActiveDeploymentConfigs() {
		if internaldeploy.IsSelfIdentity(cfg.Identity) && cfg.NodeID == h.NodeID {
			primary = cfg
			continue
		}
		if internaldeploy.IsSelfIdentity(cfg.Identity) || internaldeploy.IsNetproxyIdentity(cfg.Identity) {
			secondaryAndNetproxy = append(secondaryAndNetproxy, cfg)
		}
	}
	if primary == nil {
		return nil, DeploymentNotFoundErr
	}

	for _, cfg := range secondaryAndNetproxy {
		if _, err := h.updateInternalDeploymentVersion(ctx, cfg, targetVersion); err != nil {
			return nil, err
		}
	}

	// todo: wait for others to fully rollout
	return h.updateInternalDeploymentVersion(ctx, primary, targetVersion)
}

func (h *Handler) updateInternalDeploymentVersion(ctx apigen.Context, cfg *apigen.DeploymentConfig, targetVersion string) (*apigen.DeploymentConfig, error) {
	spec, err := cloneDeploymentSpec(&cfg.Spec)
	if err != nil {
		return nil, err
	}
	if err := spec.SetWorkloadState(targetVersion, true); err != nil {
		return nil, invalidConfigErrf("spec: %v", err)
	}
	current, _, versionOK := h.Store.UpdateDeploymentConfig(ctx, cfg.ID, sqlite.DeploymentConfigUpdate{
		ExpectedVersion: cfg.Version + 1,
		Spec:            spec,
	})
	if !versionOK {
		return nil, invalidConfigErrf("deployment version mismatch: got %d, want %d", cfg.Version+1, current.Version+1)
	}
	return current, nil
}

func (h *Handler) PostV1DeploymentDelete(ctx apigen.Context, req *apigen.DeploymentDeleteRequest) error {
	if req.DeploymentID == 0 {
		return MissingKeyErr
	}
	cfg := h.findConfigByID(req.DeploymentID)
	if cfg == nil || cfg.Deleted {
		return DeploymentNotFoundErr
	}
	if req.Version != cfg.Version+1 {
		return invalidConfigErrf("deployment version mismatch: got %d, want %d", req.Version, cfg.Version+1)
	}
	statuses := h.deploymentStatuses(req.DeploymentID)
	if sqlite.IsInternalDeploymentConfig(cfg) {
		if !h.canDeleteStaleDisconnectedSystemDeployment(cfg) {
			return invalidConfigErrf("opendeploy system deployment is internal-only")
		}
	} else if !h.canDeleteDeployment(cfg, statuses) {
		return invalidConfigErrf("deployment must be stopped before deletion")
	}
	if h.deploymentUsesAddressID(int32Set([]int32{cfg.ID})) {
		return ReferenceInUseErr
	}
	deleted := true
	spec, err := cloneDeploymentSpec(&cfg.Spec)
	if err != nil {
		return err
	}
	if err := spec.SetWorkloadState(spec.WorkloadVersion(), false); err != nil {
		return invalidConfigErrf("spec: %v", err)
	}
	_, _, versionOK := h.Store.UpdateDeploymentConfig(ctx, req.DeploymentID, sqlite.DeploymentConfigUpdate{
		ExpectedVersion: req.Version,
		Spec:            spec,
		Deleted:         &deleted,
	})
	if !versionOK {
		return invalidConfigErrf("deployment version mismatch: got %d, want %d", req.Version, cfg.Version+1)
	}
	return nil
}

// recentlyDeletedDefaultLimit and recentlyDeletedMaxLimit bound the tombstone
// listing. Deleted configs are never pruned, so an unbounded list would grow
// without limit over the lifetime of an install.
const (
	recentlyDeletedDefaultLimit = 25
	recentlyDeletedMaxLimit     = 200
)

// PostV1DeploymentRecentlyDeleted lists the deployments deleted most recently so
// the UI can offer to fork one back. Internal opendeploy deployments are omitted:
// they are recreated by the primary itself, not through the create API, so a
// tombstone for one is not something an operator can act on.
func (h *Handler) PostV1DeploymentRecentlyDeleted(ctx apigen.Context, req *apigen.RecentlyDeletedDeploymentsRequest) (*apigen.RecentlyDeletedDeployments, error) {
	limit := int(req.Limit)
	if limit <= 0 || limit > recentlyDeletedMaxLimit {
		limit = recentlyDeletedDefaultLimit
	}
	configs := h.Store.FetchDeletedDeploymentSnapshot(func(cfg apigen.DeploymentConfig) bool {
		return !sqlite.IsInternalDeploymentConfig(&cfg)
	}, limit)
	items := make([]*apigen.DeploymentConfig, 0, len(configs))
	for i := range configs {
		items = append(items, &configs[i])
	}
	return &apigen.RecentlyDeletedDeployments{Items: items}, nil
}

func (h *Handler) PostV1DeploymentVersions(ctx apigen.Context, req *apigen.DeploymentVersionsRequest) (*apigen.DeploymentVersions, error) {
	if req.DeploymentID == 0 {
		return nil, MissingKeyErr
	}

	cfg := h.findConfigByID(req.DeploymentID)
	if cfg == nil || cfg.Spec.IsZero() {
		return nil, DeploymentNotFoundErr
	}
	if sqlite.IsNetproxyDeploymentConfig(cfg) {
		if h.GithubReleaseVersions == nil {
			return nil, githubReleaseVersionsErr(fmt.Errorf("github release version loading is not configured"))
		}
		releases, err := h.GithubReleaseVersions.ListReleases(ctx, internaldeploy.Repo)
		if err != nil {
			return nil, githubReleaseVersionsErr(fmt.Errorf("listing releases: %w", err))
		}
		return &apigen.DeploymentVersions{
			DeploymentID:  req.DeploymentID,
			GithubRelease: &apigen.DeploymentGithubReleaseVersions{Releases: releases},
		}, nil
	}

	container := cfg.Spec.Container()
	switch {
	case container != nil && container.Source.NixDockerBuild != nil:
		if h.GitVersions == nil {
			return nil, fmt.Errorf("git version loading is not configured")
		}
		repo := container.Source.NixDockerBuild.Repo
		branches, branch, commits, err := h.GitVersions.DiscoverVersions(ctx, repo, req.SelectedBranch, 25)
		if err != nil {
			return nil, fmt.Errorf("discovering versions: %w", err)
		}
		return &apigen.DeploymentVersions{
			DeploymentID: req.DeploymentID,
			NixDockerBuild: &apigen.DeploymentNixDockerBuildVersions{
				Branches:       branches,
				SelectedBranch: branch,
				Commits:        commits,
			},
		}, nil
	case cfg.Spec.SystemdSpec != nil && cfg.Spec.SystemdSpec.Source != nil:
		if h.GithubReleaseVersions == nil {
			return nil, githubReleaseVersionsErr(fmt.Errorf("github release version loading is not configured"))
		}
		releases, err := h.GithubReleaseVersions.ListReleases(ctx, cfg.Spec.SystemdSpec.Source.Repo)
		if err != nil {
			return nil, githubReleaseVersionsErr(fmt.Errorf("listing releases: %w", err))
		}
		return &apigen.DeploymentVersions{
			DeploymentID:  req.DeploymentID,
			GithubRelease: &apigen.DeploymentGithubReleaseVersions{Releases: releases},
		}, nil
	case container != nil && container.Source.RemoteImage != nil:
		tags, err := versionprovider.ListContainerImageTags(ctx, container.Source.RemoteImage.Image)
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

func githubReleaseVersionsErr(err error) apigen.ApiErr {
	return apigen.NewApiErr(githubReleaseVersionsDisplayErr, err.Error(), http.StatusBadGateway)
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
			if req.TargetNodeID <= 0 {
				yield(nil, MissingKeyErr)
				return
			}
			if req.TargetNodeID != h.NodeID && h.Cluster != nil {
				stream, err := h.Cluster.RequestLogSearch(req.TargetNodeID, &apigen.MsgToWorker{LogSearchRequest: req})
				if err != nil {
					yield(nil, apigen.NewApiErr(fmt.Sprintf("Worker node %d is not connected", req.TargetNodeID), "worker_not_connected", 502))
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
		if cfg.NodeID > 0 && cfg.NodeID != h.NodeID && h.Cluster != nil {
			stream, err := h.Cluster.RequestLogSearch(cfg.NodeID, &apigen.MsgToWorker{LogSearchRequest: req})
			if err != nil {
				yield(nil, apigen.NewApiErr(fmt.Sprintf("Worker node %d is not connected", cfg.NodeID), "worker_not_connected", 502))
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
		if cfg.NodeID > 0 && cfg.NodeID != h.NodeID && h.Cluster != nil {
			reader, err := h.Cluster.RequestLogs(cfg.NodeID, &apigen.MsgToWorker{
				DeploymentLogRequest: &apigen.DeploymentLogRequest{PreparerOutput: req},
			})
			if err != nil {
				yield(nil, apigen.NewApiErr(fmt.Sprintf("Worker node %d is not connected", cfg.NodeID), "worker_not_connected", 502))
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
			localReq.Version = preparerOutputVersion(h.deploymentStatuses(localReq.DeploymentID))
			if localReq.Version == 0 {
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
			if !preparingVersion(h.deploymentStatuses(req.DeploymentID), req.Version) {
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
	for _, cfg := range h.Store.FetchDeploymentSnapshot(nil) {
		if cfg.ID == deploymentID {
			copy := cfg
			return &copy
		}
	}
	return nil
}

// deploymentStatuses returns the observed status of every live scheduled
// instance for a deployment, newest instance first, falling back to the last
// instance an ordinal ran once it has none. A deployment mid-rollover has more
// than one, so callers must not treat the newest as speaking for all of them: it
// can be STOPPED or STARTING while an older instance still serves. The retained
// entries are what keep prepare output and logs reachable after a stop.
func (h *Handler) deploymentStatuses(deploymentID int32) []apigen.ScheduledInstanceStatus {
	states := make([]apigen.ScheduledInstanceState, 0, 2)
	for _, state := range h.Store.FetchScheduledSnapshotWithLatestFinal(nil) {
		if state.Instance.DeploymentID != deploymentID {
			continue
		}
		states = append(states, state)
	}
	slices.SortFunc(states, func(a, b apigen.ScheduledInstanceState) int {
		return cmp.Compare(b.Instance.ID, a.Instance.ID)
	})
	out := make([]apigen.ScheduledInstanceStatus, 0, len(states))
	for _, state := range states {
		out = append(out, state.Status)
	}
	return out
}

// preparerOutputVersion picks the config version a caller asking for the
// "latest" prepare output wants: an in-flight prepare if there is one, else the
// newest instance that has prepared anything. Returns 0 when none has.
func preparerOutputVersion(statuses []apigen.ScheduledInstanceStatus) int32 {
	for i := range statuses {
		if p := statuses[i].Preparer; !p.IsZero() && isPrepareInProgress(p.Status) {
			return p.DeploymentConfigVersion
		}
	}
	for i := range statuses {
		if p := statuses[i].Preparer; !p.IsZero() {
			return p.DeploymentConfigVersion
		}
	}
	return 0
}

// preparingVersion reports whether any live instance is still preparing the
// given config version. An output stream follows one version, not one instance,
// so a rollover starting or finishing a different instance's prepare must not
// terminate it.
func preparingVersion(statuses []apigen.ScheduledInstanceStatus, version int32) bool {
	for i := range statuses {
		p := statuses[i].Preparer
		if !p.IsZero() && p.DeploymentConfigVersion == version && isPrepareInProgress(p.Status) {
			return true
		}
	}
	return false
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
