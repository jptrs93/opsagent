package webuihandler

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
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

func (h *Handler) deploymentService() *deployments.Service {
	s := &deployments.Service{Store: h.Store, Secrets: h.Secrets, GitVersions: h.GitVersions, Reservations: h.webUIReservations, PrimaryNodeID: h.NodeID}
	if h.Cluster != nil {
		s.Cluster = h.Cluster
	}
	return s
}

const githubReleaseVersionsDisplayErr = "Releases could not be loaded from GitHub. Please try again."

func (h *Handler) PostV1DeploymentsCreate(ctx apigen.Context, req *apigen.DeploymentCreateRequest) (*apigen.DeploymentEvent, error) {
	newDep := &apigen.DeploymentEvent{Value: apigen.Deployment{NodeID: req.NodeID, SpaceID: req.SpaceID, Name: req.Name, Spec: req.Spec}}
	if err := h.requireAccess(ctx, vCreate, eDeployment, int64(req.SpaceID), 0); err != nil {
		return nil, err
	}
	if err := h.requireDeploymentHostAccess(ctx, nil, &req.Spec, int64(req.SpaceID), 0); err != nil {
		return nil, err
	}
	return h.deploymentService().Create(ctx, &newDep.Value)
}

func (h *Handler) PostV2DeploymentsUpdate(ctx apigen.Context, req *apigen.DeploymentUpdateRequestV2) (*apigen.DeploymentEvent, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	cfg := h.deploymentByID(req.DeploymentID)
	if cfg == nil {
		return nil, deployments.NotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vUpdate, eDeployment, int64(cfg.Value.SpaceID), int64(cfg.DeploymentID), deployments.NotFoundErr); err != nil {
		return nil, err
	}
	var proposed *apigen.DeploymentSpec
	if req.SpecUpdate != nil {
		proposed = &req.SpecUpdate.Spec
	}
	// Authorize the saved spec even when this update removes host access or
	// only changes the workload version/running state.
	if err := h.requireDeploymentHostAccess(ctx, &cfg.Value.Spec, proposed, int64(cfg.Value.SpaceID), int64(cfg.DeploymentID)); err != nil {
		return nil, err
	}
	if req.AssignedSpaceUpdate != nil {
		if err := h.requireAccess(ctx, vCreate, eDeployment, int64(req.AssignedSpaceUpdate.SpaceID), 0); err != nil {
			return nil, err
		}
		if err := h.requireDeploymentHostAccess(ctx, &cfg.Value.Spec, nil, int64(req.AssignedSpaceUpdate.SpaceID), int64(cfg.DeploymentID)); err != nil {
			return nil, err
		}
	}

	return h.deploymentService().Update(ctx, cfg, req)
}

// Host access is derived from the spec, and is additional to ordinary
// deployment permissions. Managed volumes and assets do not use raw host paths.
func (h *Handler) requireDeploymentHostAccess(ctx apigen.Context, saved, proposed *apigen.DeploymentSpec, spaceID, deploymentID int64) error {
	for _, spec := range []*apigen.DeploymentSpec{saved, proposed} {
		if spec == nil {
			continue
		}
		if container := spec.Container(); container != nil && len(container.Runtime.Mounts) > 0 {
			if !h.canAccess(ctx, apigen.AuthzVerb_AUTHZ_VERB_USE_HOST_MOUNTS, eDeployment, spaceID, deploymentID) {
				err := AccessDeniedErr
				err.DisplayErr = fmt.Sprintf("Creating or updating a deployment with custom host mounts requires use_host_mounts permission in space %d", spaceID)
				return err
			}
		}
	}
	// New specs default unspecified networking to virtual during validation.
	// Saved specs follow the runner: any non-virtual mode joins the host netns,
	// including legacy specs with no explicit mode. Gate those updates too.
	hostNetwork := saved != nil && saved.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL ||
		proposed != nil && proposed.Networking.Mode == apigen.NetworkingMode_NETWORKING_MODE_HOST
	if hostNetwork {
		if !h.canAccess(ctx, apigen.AuthzVerb_AUTHZ_VERB_USE_HOST_NETWORK, eDeployment, spaceID, deploymentID) {
			err := AccessDeniedErr
			err.DisplayErr = fmt.Sprintf("Creating or updating a deployment with host networking requires use_host_network permission in space %d", spaceID)
			return err
		}
	}
	return nil
}

func (h *Handler) PostV1DeploymentsDelete(ctx apigen.Context, req *apigen.DeploymentDeleteRequest) error {
	if req.DeploymentID == 0 {
		return MissingKeyErr
	}
	cfg := h.deploymentByID(req.DeploymentID)
	if cfg == nil || cfg.Deleted() {
		return deployments.NotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vDelete, eDeployment, int64(cfg.Value.SpaceID), int64(cfg.DeploymentID), deployments.NotFoundErr); err != nil {
		return err
	}

	return h.deploymentService().Delete(ctx, req.DeploymentID, req.Version-1)
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
	configs := deployments.Deleted(h.Queries, func(cfg apigen.DeploymentEvent) bool {
		return !internaldeploy.IsInternalConfig(&cfg) &&
			h.canAccess(ctx, vView, eDeployment, int64(cfg.Value.SpaceID), int64(cfg.DeploymentID))
	}, limit)
	items := make([]*apigen.DeploymentEvent, 0, len(configs))
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
	if cfg == nil || cfg.Value.Spec.IsZero() {
		return nil, deployments.NotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vView, eDeployment, int64(cfg.Value.SpaceID), int64(cfg.DeploymentID), deployments.NotFoundErr); err != nil {
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

	container := cfg.Value.Spec.Container()
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
		tags, err := versionprovider.ListContainerImageTags(ctx, container.Source.RemoteImage.Image, h.GithubCredentials)
		if err != nil {
			return nil, fmt.Errorf("listing container image tags: %w", err)
		}
		return &apigen.DeploymentVersions{
			DeploymentID:   req.DeploymentID,
			ContainerImage: &apigen.DeploymentContainerImageVersions{Tags: tags},
		}, nil
	default:
		return nil, deployments.NotFoundErr
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
		return 0, deployments.InvalidConfigErrf("specVersion must not be negative")
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
		return 0, deployments.NotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vViewLogs, eDeployment, int64(cfg.Value.SpaceID), int64(cfg.DeploymentID), deployments.NotFoundErr); err != nil {
		return 0, err
	}
	return cfg.Value.NodeID, nil
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
			yield(nil, deployments.NotFoundErr)
			return
		}
		if err := h.requireEntityAccess(ctx, vViewLogs, eDeployment, int64(cfg.Value.SpaceID), int64(cfg.DeploymentID), deployments.NotFoundErr); err != nil {
			yield(nil, err)
			return
		}
		if cfg.Value.NodeID > 0 && cfg.Value.NodeID != h.NodeID && h.Cluster != nil {
			reader, err := h.Cluster.RequestLogs(cfg.Value.NodeID, &apigen.MsgToSecondary{
				DeploymentLogRequest: &apigen.DeploymentLogRequest{PreparerOutput: req},
			})
			if err != nil {
				yield(nil, apigen.NewApiErr(fmt.Sprintf("Secondary node %d is not connected", cfg.Value.NodeID), "secondary_not_connected", 502))
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
func (h *Handler) findConfigByID(deploymentID int32) *apigen.DeploymentEvent {
	cfg := h.deploymentByID(deploymentID)
	if cfg == nil || cfg.Deleted() {
		return nil
	}
	return cfg
}

func (h *Handler) deploymentByID(deploymentID int32) *apigen.DeploymentEvent {
	event, err := h.Queries.GetLatestDeploymentEvent(context.Background(), int64(deploymentID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return erru.Must(event, err)
}

// deploymentStatuses returns the observed status of every live scheduled
// instance for a deployment, newest instance first, falling back to the last
// instance an ordinal ran once it has none. A deployment mid-rollover has more
// than one, so callers must not treat the newest as speaking for all of them: it
// can be STOPPED or STARTING while an older instance still serves. The retained
// entries are what keep prepare output and logs reachable after a stop.
func (h *Handler) deploymentStatuses(deploymentID int32) []apigen.ScheduledInstanceStatus {
	states := make([]apigen.ScheduledInstanceState, 0, 2)
	for _, state := range h.Store.FetchScheduledSnapshot(nil) {
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
