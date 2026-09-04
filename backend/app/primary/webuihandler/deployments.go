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
)

var InvalidRequestBodyErr = apigen.NewApiErr("Invalid request body", "invalid_request_body", http.StatusBadRequest)
var MissingKeyErr = apigen.NewApiErr("Missing deployment identifier", "missing_key", http.StatusBadRequest)
var NoPrepareOutputErr = apigen.NewApiErr("No prepare output found", "prepare_output_not_found", http.StatusNotFound)
var DeploymentNotFoundErr = apigen.NewApiErr("Deployment not found", "deployment_not_found", http.StatusNotFound)

var DuplicateDeploymentErr = apigen.NewApiErr("A deployment with this name, space, and node already exists", "duplicate_deployment", http.StatusConflict)

var DeploymentAddressReferencedErr = apigen.NewApiErr("Deployment address is referenced by other deployments", "deployment_address_referenced", http.StatusConflict)

const githubReleaseVersionsDisplayErr = "Releases could not be loaded from GitHub. Please try again."

func (h *Handler) PostV1DeploymentsCreate(ctx apigen.Context, req *apigen.DeploymentCreateRequest) (*apigen.Deployment, error) {
	newDep := &apigen.Deployment{Def: apigen.DeploymentDef{NodeID: req.NodeID, SpaceID: req.SpaceID, Name: req.Name, Spec: req.Spec}}
	if err := h.requireAccess(ctx, vCreate, eDeployment, int64(req.SpaceID), 0); err != nil {
		return nil, err
	}
	if err := preLockValidateDeploymentCreate(h.Store, h.Secrets, h.GitVersions, ctx, newDep); err != nil {
		return nil, err
	}
	h.Store.Mu.Lock()
	defer h.Store.Mu.Unlock()
	if err := inLockValidateDeploymentCreate(h.Store, h.Secrets, h.webUIReservations(), newDep, h.Store.LiveState()); err != nil {
		return nil, err
	}
	return h.Store.CreateDeploymentLocked(ctx, &newDep.Def), nil
}

func (h *Handler) PostV2DeploymentsUpdate(ctx apigen.Context, req *apigen.DeploymentUpdateRequestV2) (*apigen.Deployment, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	cfg := h.Store.FetchDeployment(req.DeploymentID)
	if cfg == nil {
		return nil, DeploymentNotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vUpdate, eDeployment, int64(cfg.Def.SpaceID), int64(cfg.ID), DeploymentNotFoundErr); err != nil {
		return nil, err
	}
	if req.AssignedSpaceUpdate != nil {
		if err := h.requireAccess(ctx, vCreate, eDeployment, int64(req.AssignedSpaceUpdate.SpaceID), 0); err != nil {
			return nil, err
		}
	}

	updated, err := cloneDeployment(cfg)
	if err != nil {
		return nil, err
	}
	switch {
	case req.VersionOnlyUpdate != nil:
		if err := setTargetVersion(updated, req.VersionOnlyUpdate.TargetVersion); err != nil {
			return nil, err
		}
	case req.RunningOnlyUpdate != nil:
		if err := updated.SetWorkloadState(updated.WorkloadVersion(), req.RunningOnlyUpdate.DesiredRunning); err != nil {
			return nil, invalidConfigErrf("spec: %v", err)
		}
	case req.SpecUpdate != nil:
		updated.Def.Spec = req.SpecUpdate.Spec
	case req.AssignedSpaceUpdate != nil:
		updated.Def.SpaceID = req.AssignedSpaceUpdate.SpaceID
	}

	if err := preLockValidateDeploymentUpdate(h.Store, h.Secrets, h.GitVersions, ctx, cfg, req, updated); err != nil {
		return nil, err
	}

	h.Store.Mu.Lock()
	defer h.Store.Mu.Unlock()
	live := h.Store.LiveState()
	if err := inLockValidateDeploymentUpdate(h.Store, h.Secrets, h.webUIReservations(), live.Deployments[req.DeploymentID], updated, req.ExpectedVersion, live); err != nil {
		return nil, err
	}
	return h.Store.UpdateDeploymentLocked(ctx, req.DeploymentID, &updated.Def), nil
}

func cloneDeployment(cfg *apigen.Deployment) (*apigen.Deployment, error) {
	spec, err := cloneDeploymentSpec(&cfg.Def.Spec)
	if err != nil {
		return nil, err
	}
	updated := *cfg
	updated.Def.Spec = *spec
	return &updated, nil
}

func setTargetVersion(updated *apigen.Deployment, targetVersion string) error {
	if err := updated.SetWorkloadState(targetVersion, true); err != nil {
		return invalidConfigErrf("spec: %v", err)
	}
	return nil
}

func (h *Handler) PostV1DeploymentsDelete(ctx apigen.Context, req *apigen.DeploymentDeleteRequest) error {
	if req.DeploymentID == 0 {
		return MissingKeyErr
	}
	cfg := h.Store.FetchDeployment(req.DeploymentID)
	if cfg == nil || cfg.Deleted() {
		return DeploymentNotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vDelete, eDeployment, int64(cfg.Def.SpaceID), int64(cfg.ID), DeploymentNotFoundErr); err != nil {
		return err
	}
	if err := preLockValidateDeploymentDelete(cfg, req.Version); err != nil {
		return err
	}

	h.Store.Mu.Lock()
	defer h.Store.Mu.Unlock()
	live := h.Store.LiveState()
	if err := inLockValidateDeploymentDelete(h.Store, h.Cluster, h.NodeID, live.Deployments[req.DeploymentID], req.Version, live); err != nil {
		return err
	}
	h.Store.DeleteDeploymentLocked(ctx, req.DeploymentID)
	return nil
}

func (h *Handler) PostV1DeploymentsRecentlyDeleted(ctx apigen.Context, req *apigen.RecentlyDeletedDeploymentsRequest) (*apigen.RecentlyDeletedDeployments, error) {
	const (
		recentlyDeletedDefaultLimit = 25
		recentlyDeletedMaxLimit     = 200
	)
	limit := int(req.Limit)
	if limit <= 0 || limit > recentlyDeletedMaxLimit {
		limit = recentlyDeletedDefaultLimit
	}
	configs := h.Store.FetchDeletedDeploymentSnapshot(func(cfg apigen.Deployment) bool {
		return !internaldeploy.IsInternalConfig(&cfg) &&
			h.canAccess(ctx, vView, eDeployment, int64(cfg.Def.SpaceID), int64(cfg.ID))
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
	if cfg == nil || cfg.Def.Spec.IsZero() {
		return nil, DeploymentNotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vView, eDeployment, int64(cfg.Def.SpaceID), int64(cfg.ID), DeploymentNotFoundErr); err != nil {
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

	container := cfg.Def.Spec.Container()
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
func (h *Handler) logQueryTargetNode(ctx apigen.Context, deploymentID, targetNodeID, specVersion int32) (int32, error) {
	if specVersion < 0 {
		return 0, invalidConfigErrf("specVersion must not be negative")
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
	if err := h.requireEntityAccess(ctx, vViewLogs, eDeployment, int64(cfg.Def.SpaceID), int64(cfg.ID), DeploymentNotFoundErr); err != nil {
		return 0, err
	}
	return cfg.Def.NodeID, nil
}

func (h *Handler) PostV1DeploymentsLogQuery(ctx apigen.Context, req *apigen.LogQueryRequest) (*apigen.LogQueryResponse, error) {
	nodeID, err := h.logQueryTargetNode(ctx, req.DeploymentID, req.TargetNodeID, req.SpecVersion)
	if err != nil {
		return nil, err
	}
	if nodeID > 0 && nodeID != h.NodeID && h.Cluster != nil {
		resp, err := h.Cluster.RequestLogQuery(ctx, nodeID, req)
		if err != nil {
			return nil, secondaryLogQueryErr(nodeID, err)
		}
		return resp, nil
	}
	if h.LogManager == nil {
		return nil, apigen.NewApiErr("Log manager is not running", "log_manager_unavailable", http.StatusInternalServerError)
	}
	return h.LogManager.Query(ctx, req)
}

func secondaryLogQueryErr(nodeID int32, err error) error {
	var notConnected *clusterhandler.NodeNotConnectedError
	if errors.As(err, &notConnected) {
		return apigen.NewApiErr(fmt.Sprintf("Secondary node %d is not connected", nodeID), "secondary_not_connected", http.StatusBadGateway)
	}
	return apigen.NewApiErr(fmt.Sprintf("Log query on secondary node %d failed: %v", nodeID, err), "secondary_log_query_failed", http.StatusBadGateway)
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
		if err := h.requireEntityAccess(ctx, vViewLogs, eDeployment, int64(cfg.Def.SpaceID), int64(cfg.ID), DeploymentNotFoundErr); err != nil {
			yield(nil, err)
			return
		}
		if cfg.Def.NodeID > 0 && cfg.Def.NodeID != h.NodeID && h.Cluster != nil {
			reader, err := h.Cluster.RequestLogs(cfg.Def.NodeID, &apigen.MsgToSecondary{
				DeploymentLogRequest: &apigen.DeploymentLogRequest{PreparerOutput: req},
			})
			if err != nil {
				yield(nil, apigen.NewApiErr(fmt.Sprintf("Secondary node %d is not connected", cfg.Def.NodeID), "secondary_not_connected", 502))
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
		if localReq.SpecVersion == 0 {
			localReq.SpecVersion = preparerOutputVersion(h.deploymentStatuses(localReq.DeploymentID))
			if localReq.SpecVersion == 0 {
				localReq.SpecVersion = cfg.SpecVersion
			}
		}
		if localReq.SpecVersion == 0 {
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
			if !preparingVersion(h.deploymentStatuses(req.DeploymentID), req.SpecVersion) {
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

// findConfigByID resolves a live deployment, hiding deleted tombstones from
// callers that treat existence as "queryable" (history, logs, versions).
func (h *Handler) findConfigByID(deploymentID int32) *apigen.Deployment {
	cfg := h.Store.FetchDeployment(deploymentID)
	if cfg == nil || cfg.Deleted() {
		return nil
	}
	return cfg
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

// preparerOutputVersion picks the spec version a caller asking for the
// "latest" prepare output wants: an in-flight prepare if there is one, else the
// newest instance that has prepared anything. Returns 0 when none has.
func preparerOutputVersion(statuses []apigen.ScheduledInstanceStatus) int32 {
	for i := range statuses {
		if p := statuses[i].Preparer; !p.IsZero() && prepare.InProgress(p) {
			return p.DeploymentSpecVersion
		}
	}
	for i := range statuses {
		if p := statuses[i].Preparer; !p.IsZero() {
			return p.DeploymentSpecVersion
		}
	}
	return 0
}

// preparingVersion reports whether any live instance is still preparing the
// given spec version. An output stream follows one version, not one instance,
// so a rollover starting or finishing a different instance's prepare must not
// terminate it.
func preparingVersion(statuses []apigen.ScheduledInstanceStatus, version int32) bool {
	for i := range statuses {
		p := statuses[i].Preparer
		if !p.IsZero() && p.DeploymentSpecVersion == version && prepare.InProgress(p) {
			return true
		}
	}
	return false
}
