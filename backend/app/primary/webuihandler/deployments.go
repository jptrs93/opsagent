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
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
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

func (h *Handler) PostV1DeploymentsCreate(ctx apigen.Context, req *apigen.DeploymentCreateRequest) (*apigen.CoreWriteUpdate, error) {
	if req.SpaceID == internaldeploy.SpaceID {
		return nil, deployments.SystemSpaceErr()
	}
	newDep := apigen.Deployment{Scheduling: req.Scheduling, SpaceID: req.SpaceID, Name: req.Name, Spec: req.Spec}
	if err := h.requireAccess(ctx, vCreate, eDeployment, req.SpaceID, 0); err != nil {
		return nil, err
	}
	if err := h.requireDeploymentHostAccess(ctx, nil, &req.Spec, req.SpaceID, 0); err != nil {
		return nil, err
	}
	event, err := h.deploymentService().Create(ctx, &newDep)
	if err != nil {
		return nil, err
	}
	return h.written(ctx, pq.DeploymentMutation(event)), nil
}

func (h *Handler) PostV1DeploymentsUpdate(ctx apigen.Context, req *apigen.DeploymentUpdateRequest) (*apigen.CoreWriteUpdate, error) {
	if err := req.Validate(); err != nil {
		return nil, deployments.InvalidConfigErrf("%s", err.Error())
	}
	cfg := h.deploymentByID(req.DeploymentID)
	if cfg == nil {
		return nil, deployments.NotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vUpdate, eDeployment, cfg.Deployment.SpaceID, cfg.Deployment.ID, deployments.NotFoundErr); err != nil {
		return nil, err
	}
	var proposed *apigen.DeploymentSpec
	if req.Update.Spec != nil {
		proposed = &req.Update.Spec.Spec
	}
	// Authorize the saved spec even when this update removes host access or
	// only changes the workload version/running state.
	if err := h.requireDeploymentHostAccess(ctx, &cfg.Deployment.Spec, proposed, cfg.Deployment.SpaceID, cfg.Deployment.ID); err != nil {
		return nil, err
	}
	if move := req.Update.AssignedSpace; move != nil {
		if move.SpaceID == internaldeploy.SpaceID {
			return nil, deployments.SystemSpaceErr()
		}
		if err := h.requireAccess(ctx, vCreate, eDeployment, move.SpaceID, 0); err != nil {
			return nil, err
		}
		if err := h.requireDeploymentHostAccess(ctx, &cfg.Deployment.Spec, nil, move.SpaceID, cfg.Deployment.ID); err != nil {
			return nil, err
		}
	}

	event, err := h.deploymentService().Update(ctx, cfg, req)
	if err != nil {
		return nil, err
	}
	return h.written(ctx, pq.DeploymentMutation(event)), nil
}

// Host access is derived from the spec, and is additional to ordinary
// deployment permissions. Managed volumes and assets do not use raw host paths.
func (h *Handler) requireDeploymentHostAccess(ctx apigen.Context, saved, proposed *apigen.DeploymentSpec, spaceID, deploymentID uint64) error {
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
	hostNetwork := saved != nil && saved.Networking.Mode == apigen.NetworkingMode_NETWORKING_MODE_HOST ||
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
	if err := h.requireEntityAccess(ctx, vDelete, eDeployment, cfg.Deployment.SpaceID, cfg.Deployment.ID, deployments.NotFoundErr); err != nil {
		return err
	}

	return h.deploymentService().Delete(ctx, req.DeploymentID, req.ExpectedSeq)
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
	configs := deployments.Deleted(h.Queries, func(cfg apigen.DeploymentRecord) bool {
		return !internaldeploy.IsInternalConfig(&cfg) &&
			h.canAccess(ctx, vView, eDeployment, cfg.Deployment.SpaceID, cfg.Deployment.ID)
	}, limit)
	return &apigen.RecentlyDeletedDeployments{Items: configs}, nil
}

func (h *Handler) PostV1DeploymentsVersions(ctx apigen.Context, req *apigen.DeploymentVersionsRequest) (*apigen.DeploymentVersions, error) {
	if req.DeploymentID == 0 {
		return nil, MissingKeyErr
	}

	cfg := h.findConfigByID(req.DeploymentID)
	if cfg == nil || cfg.Deployment.Spec.IsZero() {
		return nil, deployments.NotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vView, eDeployment, cfg.Deployment.SpaceID, cfg.Deployment.ID, deployments.NotFoundErr); err != nil {
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
			DeploymentID: req.DeploymentID,
			Source:       apigen.DeploymentVersionsSourceOneof{GithubRelease: &apigen.DeploymentGithubReleaseVersions{Releases: derefVersions(releases)}},
		}, nil
	}

	container := cfg.Deployment.Spec.Container()
	switch {
	case container != nil && container.Source.Value.NixImageBuild != nil:
		if h.GitVersions == nil {
			return nil, fmt.Errorf("git version loading is not configured")
		}
		repo := container.Source.Value.NixImageBuild.Repo
		branches, branch, commits, err := h.GitVersions.DiscoverVersions(ctx, repo, req.SelectedBranch, 25)
		if err != nil {
			return nil, fmt.Errorf("discovering versions: %w", err)
		}
		return &apigen.DeploymentVersions{
			DeploymentID: req.DeploymentID,
			Source: apigen.DeploymentVersionsSourceOneof{NixImageBuild: &apigen.DeploymentNixImageBuildVersions{
				Branches:       branches,
				SelectedBranch: branch,
				Commits:        derefVersions(commits),
			}},
		}, nil
	case container != nil && container.Source.Value.RemoteImage != nil:
		tags, err := versionprovider.ListContainerImageTags(ctx, container.Source.Value.RemoteImage.Image, h.GithubCredentials)
		if err != nil {
			return nil, fmt.Errorf("listing container image tags: %w", err)
		}
		return &apigen.DeploymentVersions{
			DeploymentID: req.DeploymentID,
			Source:       apigen.DeploymentVersionsSourceOneof{ContainerImage: &apigen.DeploymentContainerImageVersions{Tags: derefVersions(tags)}},
		}, nil
	default:
		return nil, deployments.NotFoundErr
	}
}

func derefVersions(in []*apigen.Version) []apigen.Version {
	out := make([]apigen.Version, 0, len(in))
	for _, v := range in {
		if v != nil {
			out = append(out, *v)
		}
	}
	return out
}

func githubReleaseVersionsErr(err error) apigen.ApiErr {
	return apigen.NewApiErr(githubReleaseVersionsDisplayErr, err.Error(), http.StatusBadGateway)
}

// logQueryTargetNode authorizes a log query and resolves the node hosting the
// requested logs: the deployment's node, or target_node_id for the node's
// system log (deployment_id = 0). That log belongs to the opendeploy system
// deployment on the node, so it is gated on view_logs for that deployment in
// the system space, the same permission that reads any other deployment's
// logs; nodes themselves carry no log permission.
func (h *Handler) logQueryTargetNode(ctx apigen.Context, deploymentID, targetNodeID uint64) (uint64, error) {
	if deploymentID == 0 {
		if targetNodeID == 0 {
			return 0, MissingKeyErr
		}
		self := h.systemDeploymentForNode(targetNodeID)
		if self == nil {
			return 0, deployments.NotFoundErr
		}
		if err := h.requireEntityAccess(ctx, vViewLogs, eDeployment, self.Deployment.SpaceID, self.Deployment.ID, deployments.NotFoundErr); err != nil {
			return 0, err
		}
		return targetNodeID, nil
	}
	cfg := h.findConfigByID(deploymentID)
	if cfg == nil {
		return 0, deployments.NotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vViewLogs, eDeployment, cfg.Deployment.SpaceID, cfg.Deployment.ID, deployments.NotFoundErr); err != nil {
		return 0, err
	}
	return cfg.Deployment.PlacementNodeID(), nil
}

func (h *Handler) PostV1DeploymentsLogQuery(ctx apigen.Context, req *apigen.LogQueryRequest) (*apigen.LogQueryResponse, error) {
	nodeID, err := h.logQueryTargetNode(ctx, req.DeploymentID, req.TargetNodeID)
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

func secondaryLogQueryErr(nodeID uint64, err error) error {
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
		if err := h.requireEntityAccess(ctx, vViewLogs, eDeployment, cfg.Deployment.SpaceID, cfg.Deployment.ID, deployments.NotFoundErr); err != nil {
			yield(nil, err)
			return
		}
		if nodeID := cfg.Deployment.PlacementNodeID(); nodeID > 0 && nodeID != h.NodeID && h.Cluster != nil {
			reader, err := h.Cluster.RequestLogs(nodeID, &apigen.MsgToSecondary{
				DeploymentLogRequest: apigen.Some(apigen.DeploymentLogRequest{PreparerOutput: apigen.Some(*req)}),
			})
			if err != nil {
				yield(nil, apigen.NewApiErr(fmt.Sprintf("Secondary node %d is not connected", nodeID), "secondary_not_connected", 502))
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
				localReq.SpecVersion = cfg.Meta.SpecVersion
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
// systemDeploymentForNode finds the live opendeploy system deployment of a
// node, or nil when the node has none yet.
func (h *Handler) systemDeploymentForNode(nodeID uint64) *apigen.DeploymentRecord {
	events, err := h.Queries.ListActiveDeployments(context.Background())
	if err != nil {
		return nil
	}
	for _, cfg := range events {
		if internaldeploy.IsSelfConfig(cfg) && cfg.Deployment.PlacementNodeID() == nodeID {
			return cfg
		}
	}
	return nil
}

func (h *Handler) findConfigByID(deploymentID uint64) *apigen.DeploymentRecord {
	cfg := h.deploymentByID(deploymentID)
	if cfg == nil || cfg.Deleted() {
		return nil
	}
	return cfg
}

func (h *Handler) deploymentByID(deploymentID uint64) *apigen.DeploymentRecord {
	event, err := h.Queries.GetLatestDeployment(context.Background(), deploymentID)
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
func (h *Handler) deploymentStatuses(deploymentID uint64) []apigen.ScheduledInstanceStatus {
	states := make([]apigen.ScheduledInstanceState, 0, 2)
	for _, state := range h.Store.FetchScheduledSnapshot(nil) {
		if state.Instance.Deployment.DeploymentID != deploymentID {
			continue
		}
		states = append(states, state)
	}
	slices.SortFunc(states, func(a, b apigen.ScheduledInstanceState) int {
		return cmp.Compare(b.Instance.ID, a.Instance.ID)
	})
	out := make([]apigen.ScheduledInstanceStatus, 0, len(states))
	for _, state := range states {
		if state.Status.Present {
			out = append(out, state.Status.Value)
		}
	}
	return out
}

// preparerOutputVersion picks the spec version a caller asking for the
// "latest" prepare output wants: an in-flight prepare if there is one, else the
// newest instance that has prepared anything. Returns 0 when none has.
func preparerOutputVersion(statuses []apigen.ScheduledInstanceStatus) uint32 {
	for i := range statuses {
		if p := statuses[i].Preparer; p.Present && prepare.InProgress(p.Value) {
			return p.Value.DeploymentSpecVersion
		}
	}
	for i := range statuses {
		if p := statuses[i].Preparer; p.Present {
			return p.Value.DeploymentSpecVersion
		}
	}
	return 0
}

// preparingVersion reports whether any live instance is still preparing the
// given spec version. An output stream follows one version, not one instance,
// so a rollover starting or finishing a different instance's prepare must not
// terminate it.
func preparingVersion(statuses []apigen.ScheduledInstanceStatus, version uint32) bool {
	for i := range statuses {
		p := statuses[i].Preparer
		if p.Present && p.Value.DeploymentSpecVersion == version && prepare.InProgress(p.Value) {
			return true
		}
	}
	return false
}
