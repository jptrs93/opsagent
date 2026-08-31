package webuihandler

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/clusterhandler"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare"
	"github.com/jptrs93/opsagent/backend/lib/engine/versionprovider"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var InvalidRequestBodyErr = apigen.NewApiErr("Invalid request body", "invalid_request_body", http.StatusBadRequest)
var MissingKeyErr = apigen.NewApiErr("Missing deployment identifier", "missing_key", http.StatusBadRequest)
var NoPrepareOutputErr = apigen.NewApiErr("No prepare output found", "prepare_output_not_found", http.StatusNotFound)
var DeploymentNotFoundErr = apigen.NewApiErr("Deployment not found", "deployment_not_found", http.StatusNotFound)

var DuplicateDeploymentErr = apigen.NewApiErr("A deployment with this name, space, and node already exists", "duplicate_deployment", http.StatusConflict)

var DeploymentAddressReferencedErr = apigen.NewApiErr("Deployment address is referenced by other deployments", "deployment_address_referenced", http.StatusConflict)

const githubReleaseVersionsDisplayErr = "Releases could not be loaded from GitHub. Please try again."

func (h *Handler) PostV1DeploymentsCreate(ctx apigen.Context, req *apigen.DeploymentCreateRequest) (*apigen.Deployment, error) {
	if req.Name == "" {
		return nil, invalidConfigErrf("name is required")
	}
	if req.NodeID <= 0 {
		return nil, invalidConfigErrf("nodeId is required")
	}
	_, err := h.Store.NodeIdentifierByID(req.NodeID)
	if err != nil {
		return nil, invalidConfigErrf("node is not registered")
	}
	if internaldeploy.IsInternalIdentity(req.SpaceID, req.Name) {
		return nil, invalidConfigErrf("opendeploy system deployment identity is internal-only")
	}
	if req.SpaceID < 0 || req.SpaceID > network.MaxSpaceID {
		return nil, invalidConfigErrf("spaceId must be between 0 and %d", network.MaxSpaceID)
	}
	if err := h.requireAccess(ctx, vCreate, eDeployment, int64(req.SpaceID), 0); err != nil {
		return nil, err
	}
	if err := h.validateNodeAllowsSpace(req.NodeID, req.SpaceID); err != nil {
		return nil, err
	}
	spec, err := h.validateDeploymentSpec(&req.Spec)
	if err != nil {
		return nil, err
	}
	if err := h.validateCrossDeploymentMountSources(spec, req.NodeID, 0, req.SpaceID); err != nil {
		return nil, err
	}

	snapshot := h.Store.FetchDeploymentSnapshot(nil)
	for _, cfg := range snapshot {
		if storage.DeploymentKeyMatches(cfg, req.NodeID, req.SpaceID, req.Name) && !cfg.Deleted {
			return nil, DuplicateDeploymentErr
		}
	}
	if err := h.validateNodeNetworkingClaims(req.NodeID, 0, spec); err != nil {
		return nil, err
	}
	if err := h.validateAddressEnvRefs(req.NodeID, 0, req.SpaceID, spec, snapshot); err != nil {
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

	// Locality check and write under the reference lock: PostV1SecretsMove
	// scans referencing deployments under the same lock before moving, so a
	// ref accepted here cannot be stranded by a concurrent secret space move.
	unlockReferences := h.ConfigService.LockReferences()
	defer unlockReferences()
	if err := h.validateRefSpaces(spec, req.SpaceID); err != nil {
		return nil, err
	}
	cfg := h.Store.MustCreateDeploymentForNode(ctx, req.SpaceID, req.Name, req.NodeID, spec)
	h.wakeAcme()
	return cfg, nil
}

func (h *Handler) wakeAcme() {
	if h.AcmeWake != nil {
		h.AcmeWake()
	}
}

func (h *Handler) PostV1DeploymentsUpdate(ctx apigen.Context, req *apigen.DeploymentUpdateRequest) (*apigen.Deployment, error) {
	if req.DeploymentID == 0 {
		return nil, MissingKeyErr
	}
	cfg := h.findConfigByID(req.DeploymentID)
	if cfg == nil || cfg.Deleted {
		return nil, DeploymentNotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vUpdate, eDeployment, int64(cfg.SpaceID), int64(cfg.ID), DeploymentNotFoundErr); err != nil {
		return nil, err
	}
	if req.Version != cfg.Version+1 {
		return nil, invalidConfigErrf("deployment version mismatch: got %d, want %d", req.Version, cfg.Version+1)
	}
	if !req.Spec.IsZero() && internaldeploy.IsInternalConfig(cfg) {
		return nil, invalidConfigErrf("opendeploy system deployment identity and spec are internal-only")
	}
	if req.Stop && internaldeploy.IsSelfConfig(cfg) {
		return nil, invalidConfigErrf("the opendeploy system deployment cannot be stopped")
	}

	var spec *apigen.DeploymentSpec
	if !req.Spec.IsZero() {
		validated, err := h.validateDeploymentSpec(&req.Spec)
		if err != nil {
			return nil, err
		}
		if err := h.validateNodeNetworkingClaims(cfg.NodeID, cfg.ID, validated); err != nil {
			return nil, err
		}
		if err := h.validateCrossDeploymentMountSources(validated, cfg.NodeID, cfg.ID, cfg.SpaceID); err != nil {
			return nil, err
		}
		if err := h.validateAddressEnvRefs(cfg.NodeID, cfg.ID, cfg.SpaceID, validated, h.Store.FetchDeploymentSnapshot(nil)); err != nil {
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

	if spec != nil || stateChanged {
		// Checked against the effective spec so spec changes and combined
		// writes all pass through the guard. Same lock discipline as create:
		// see PostV1DeploymentsCreate.
		unlockReferences := h.ConfigService.LockReferences()
		defer unlockReferences()
		if err := h.validateRefSpaces(effectiveSpec, cfg.SpaceID); err != nil {
			return nil, err
		}
		current, _, versionOK := h.Store.UpdateDeployment(ctx, req.DeploymentID, state.DeploymentConfigUpdate{
			ExpectedVersion: req.Version,
			Spec:            effectiveSpec,
			Deleted:         cfg.Deleted,
		})
		if !versionOK {
			return nil, invalidConfigErrf("deployment version mismatch: got %d, want %d", req.Version, current.Version+1)
		}
		h.wakeAcme()
		return current, nil
	}

	return cfg, nil
}

// PostV1DeploymentsMoveSpace moves a deployment to another space. The move is
// guarded by the deployment's space version rather than its config version, is
// validated like a create into the destination space, and rides the scheduler
// rollover path: live placements keep their pinned space until replaced.
func (h *Handler) PostV1DeploymentsMoveSpace(ctx apigen.Context, req *apigen.DeploymentSpaceMoveRequest) (*apigen.Deployment, error) {
	if req.DeploymentID == 0 {
		return nil, MissingKeyErr
	}
	cfg := h.findConfigByID(req.DeploymentID)
	if cfg == nil || cfg.Deleted {
		return nil, DeploymentNotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vUpdate, eDeployment, int64(cfg.SpaceID), int64(cfg.ID), DeploymentNotFoundErr); err != nil {
		return nil, err
	}
	if req.SpaceVersion != cfg.SpaceVersion+1 {
		return nil, invalidConfigErrf("deployment space version mismatch: got %d, want %d", req.SpaceVersion, cfg.SpaceVersion+1)
	}
	if internaldeploy.IsInternalConfig(cfg) {
		return nil, invalidConfigErrf("opendeploy system deployment identity and spec are internal-only")
	}
	if cfg.SpaceID == 0 {
		return nil, invalidConfigErrf("deployments in space 0 cannot be moved")
	}
	if req.SpaceID < 1 || req.SpaceID > network.MaxSpaceID {
		return nil, invalidConfigErrf("spaceId must be between 1 and %d", network.MaxSpaceID)
	}
	if req.SpaceID == cfg.SpaceID {
		return cfg, nil
	}
	if err := h.requireAccess(ctx, vCreate, eDeployment, int64(req.SpaceID), 0); err != nil {
		return nil, err
	}
	if err := h.validateNodeAllowsSpace(cfg.NodeID, req.SpaceID); err != nil {
		return nil, err
	}

	// Same lock discipline as create: see PostV1DeploymentsCreate.
	unlockReferences := h.ConfigService.LockReferences()
	defer unlockReferences()
	if err := h.validateRefSpaces(&cfg.Spec, req.SpaceID); err != nil {
		return nil, err
	}
	if err := h.validateAddressEnvRefs(cfg.NodeID, cfg.ID, req.SpaceID, &cfg.Spec, h.Store.FetchDeploymentSnapshot(nil)); err != nil {
		return nil, err
	}
	if err := h.validateCrossDeploymentMountSources(&cfg.Spec, cfg.NodeID, cfg.ID, req.SpaceID); err != nil {
		return nil, err
	}
	if h.deploymentUsesAddressID(int32Set([]int32{cfg.ID})) {
		return nil, DeploymentAddressReferencedErr
	}
	if req.SpaceID != state.DefaultSpaceID && h.referencesOutsideSpace(int32Set([]int32{cfg.ID}), crossDeploymentMountSourceIDs, req.SpaceID) {
		return nil, MoveReferencesOutsideSpaceErr
	}
	moved, err := h.Store.MoveDeploymentSpace(cfg.ID, req.SpaceID, req.SpaceVersion, ctx.AttributionUserID())
	if err != nil {
		switch {
		case errors.Is(err, state.DuplicateDeploymentIdentityErr):
			return nil, DuplicateDeploymentErr
		case errors.Is(err, state.SpaceVersionMismatchErr):
			return nil, invalidConfigErrf("deployment space version mismatch: got %d, want %d", req.SpaceVersion, cfg.SpaceVersion+1)
		default:
			return nil, DeploymentNotFoundErr
		}
	}
	return moved, nil
}

func (h *Handler) PostV1DeploymentsDelete(ctx apigen.Context, req *apigen.DeploymentDeleteRequest) error {
	if req.DeploymentID == 0 {
		return MissingKeyErr
	}
	cfg := h.findConfigByID(req.DeploymentID)
	if cfg == nil || cfg.Deleted {
		return DeploymentNotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vDelete, eDeployment, int64(cfg.SpaceID), int64(cfg.ID), DeploymentNotFoundErr); err != nil {
		return err
	}
	if req.Version != cfg.Version+1 {
		return invalidConfigErrf("deployment version mismatch: got %d, want %d", req.Version, cfg.Version+1)
	}
	statuses := h.deploymentStatuses(req.DeploymentID)
	if internaldeploy.IsInternalConfig(cfg) {
		if !h.canDeleteStaleDisconnectedSystemDeployment(cfg) {
			return invalidConfigErrf("opendeploy system deployment is internal-only")
		}
	} else if !h.canDeleteDeployment(cfg, statuses) {
		return invalidConfigErrf("deployment must be stopped before deletion")
	}
	if details := h.deploymentRefDetails(int32Set([]int32{cfg.ID}), addressRefIDs); len(details) > 0 {
		return referenceInUseDetailErr("Deployment address", details)
	}
	spec, err := cloneDeploymentSpec(&cfg.Spec)
	if err != nil {
		return err
	}
	if err := spec.SetWorkloadState(spec.WorkloadVersion(), false); err != nil {
		return invalidConfigErrf("spec: %v", err)
	}
	_, _, versionOK := h.Store.UpdateDeployment(ctx, req.DeploymentID, state.DeploymentConfigUpdate{
		ExpectedVersion: req.Version,
		Spec:            spec,
		Deleted:         true,
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

// PostV1DeploymentsRecentlyDeleted lists the deployments deleted most recently so
// the UI can offer to fork one back. Internal opendeploy deployments are omitted:
// they are recreated by the primary itself, not through the create API, so a
// tombstone for one is not something an operator can act on.
func (h *Handler) PostV1DeploymentsRecentlyDeleted(ctx apigen.Context, req *apigen.RecentlyDeletedDeploymentsRequest) (*apigen.RecentlyDeletedDeployments, error) {
	limit := int(req.Limit)
	if limit <= 0 || limit > recentlyDeletedMaxLimit {
		limit = recentlyDeletedDefaultLimit
	}
	configs := h.Store.FetchDeletedDeploymentSnapshot(func(cfg apigen.Deployment) bool {
		return !internaldeploy.IsInternalConfig(&cfg) &&
			h.canAccess(ctx, vView, eDeployment, int64(cfg.SpaceID), int64(cfg.ID))
	}, limit)
	items := make([]*apigen.Deployment, 0, len(configs))
	for i := range configs {
		items = append(items, &configs[i])
	}
	return &apigen.RecentlyDeletedDeployments{Items: items}, nil
}

func (h *Handler) PostV1DeploymentsVersions(ctx apigen.Context, req *apigen.DeploymentVersionsRequest) (*apigen.DeploymentVersions, error) {
	if req.DeploymentID == 0 {
		return nil, MissingKeyErr
	}

	cfg := h.findConfigByID(req.DeploymentID)
	if cfg == nil || cfg.Spec.IsZero() {
		return nil, DeploymentNotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vView, eDeployment, int64(cfg.SpaceID), int64(cfg.ID), DeploymentNotFoundErr); err != nil {
		return nil, err
	}
	if internaldeploy.IsInternalConfig(cfg) {
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

// logQueryTargetNode authorizes a log query and resolves the node hosting the
// requested logs: the deployment's node, or target_node_id for the system log
// (deployment_id = 0), which is gated on the node rather than any one
// deployment.
func (h *Handler) logQueryTargetNode(ctx apigen.Context, deploymentID, targetNodeID, configVersion int32) (int32, error) {
	if configVersion < 0 {
		return 0, invalidConfigErrf("configVersion must not be negative")
	}
	if deploymentID == 0 {
		if targetNodeID <= 0 {
			return 0, MissingKeyErr
		}
		if err := h.requireAccess(ctx, vViewLogs, eNode, 0, int64(targetNodeID)); err != nil {
			return 0, err
		}
		return targetNodeID, nil
	}
	cfg := h.findConfigByID(deploymentID)
	if cfg == nil {
		return 0, DeploymentNotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vViewLogs, eDeployment, int64(cfg.SpaceID), int64(cfg.ID), DeploymentNotFoundErr); err != nil {
		return 0, err
	}
	return cfg.NodeID, nil
}

func (h *Handler) PostV1DeploymentsLogQuery(ctx apigen.Context, req *apigen.LogQueryRequest) (*apigen.LogQueryResponse, error) {
	nodeID, err := h.logQueryTargetNode(ctx, req.DeploymentID, req.TargetNodeID, req.ConfigVersion)
	if err != nil {
		return nil, err
	}
	if nodeID > 0 && nodeID != h.NodeID && h.Cluster != nil {
		resp, err := h.Cluster.RequestLogQuery(ctx, nodeID, req)
		if err != nil {
			return nil, workerLogQueryErr(nodeID, err)
		}
		return resp, nil
	}
	if h.LogManager == nil {
		return nil, apigen.NewApiErr("Log manager is not running", "log_manager_unavailable", http.StatusInternalServerError)
	}
	return h.LogManager.Query(ctx, req)
}

func workerLogQueryErr(nodeID int32, err error) error {
	var notConnected *clusterhandler.NodeNotConnectedError
	if errors.As(err, &notConnected) {
		return apigen.NewApiErr(fmt.Sprintf("Worker node %d is not connected", nodeID), "worker_not_connected", http.StatusBadGateway)
	}
	return apigen.NewApiErr(fmt.Sprintf("Log query on worker node %d failed: %v", nodeID, err), "worker_log_query_failed", http.StatusBadGateway)
}

func (h *Handler) PostV1DeploymentsPrepareOutput(ctx apigen.Context, req *apigen.PrepareOutputRequest) iter.Seq2[*apigen.PrepareOutputChunk, error] {
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
		if err := h.requireEntityAccess(ctx, vViewLogs, eDeployment, int64(cfg.SpaceID), int64(cfg.ID), DeploymentNotFoundErr); err != nil {
			yield(nil, err)
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

func (h *Handler) findConfigByID(deploymentID int32) *apigen.Deployment {
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
		if p := statuses[i].Preparer; !p.IsZero() && prepare.InProgress(p) {
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
		if !p.IsZero() && p.DeploymentConfigVersion == version && prepare.InProgress(p) {
			return true
		}
	}
	return false
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
