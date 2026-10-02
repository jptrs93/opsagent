package deployments

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/lib/ingressplan"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var NotFoundErr = apigen.NewApiErr("Deployment not found", "deployment_not_found", http.StatusNotFound)
var DuplicateErr = apigen.NewApiErr("A deployment with this name, space, and node already exists", "duplicate_deployment", http.StatusConflict)
var AddressReferencedErr = apigen.NewApiErr("Deployment address is referenced by other deployments", "deployment_address_referenced", http.StatusConflict)

type NixSourceVerifier interface {
	ValidateNixSource(ctx context.Context, repo, commit, flakePath string) (bool, error)
}

type NodeConnectivity interface {
	ConnectedNodes() map[int32]time.Time
}

type Service struct {
	Store         *state.Service
	Secrets       *secrets.Manager
	GitVersions   NixSourceVerifier
	Reservations  func() []ingressplan.Reservation
	Cluster       NodeConnectivity
	PrimaryNodeID int32
}

func (s *Service) reservations() []ingressplan.Reservation {
	if s.Reservations == nil {
		return nil
	}
	return s.Reservations()
}

func (s *Service) Create(ctx apigen.Context, dep *apigen.Deployment) (*apigen.DeploymentEvent, error) {
	newDep := &apigen.DeploymentEvent{Value: *dep}
	var event *apigen.DeploymentEvent
	err := s.Store.Commit(ctx, func(q *pq.Queries) error {
		return preLockValidateDeploymentCreate(q, s.Secrets, s.GitVersions, ctx, newDep)
	}, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if err := inLockValidateDeploymentCreate(ctx, q, s.reservations(), newDep); err != nil {
			return nil, err
		}
		id, err := q.NextDeploymentID(ctx)
		if err != nil {
			return nil, err
		}
		event = pq.DeploymentCreateEvent(ctx, id, seq, time.Now(), &newDep.Value)
		return pq.NewUpdate(pq.DeploymentMutation(event)), nil
	})
	return event, err
}

// Update applies the request on top of existing, which the caller loaded and
// authorized. A non-zero expected_seq rejects the write when the deployment
// has an event newer than the one the caller saw; the same check runs again
// inside Commit. An update that changes nothing succeeds without writing.
func (s *Service) Update(ctx apigen.Context, existing *apigen.DeploymentEvent, req *apigen.DeploymentUpdateRequestV2) (*apigen.DeploymentEvent, error) {
	if req.ExpectedSeq != 0 && existing.Seq > req.ExpectedSeq {
		return nil, InvalidConfigErrf("deployment %d changed since it was loaded", existing.DeploymentID)
	}
	updated, err := cloneDeployment(existing)
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
			return nil, InvalidConfigErrf("spec: %v", err)
		}
	case req.SpecUpdate != nil:
		updated.Value.Spec = req.SpecUpdate.Spec
	case req.AssignedSpaceUpdate != nil:
		updated.Value.SpaceID = req.AssignedSpaceUpdate.SpaceID
	case req.RestartUpdate != nil:
		if internaldeploy.IsSelfConfig(existing) {
			return nil, InvalidConfigErrf("the opendeploy system deployment cannot be restarted")
		}
		if !existing.WorkloadRunning() {
			return nil, InvalidConfigErrf("deployment is not running")
		}
		updated.Value.Scheduling.Generation++
	}
	var event *apigen.DeploymentEvent
	err = s.Store.Commit(ctx, func(q *pq.Queries) error {
		return preLockValidateDeploymentUpdate(q, s.Secrets, s.GitVersions, ctx, existing, req, updated)
	}, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if err := inLockValidateDeploymentUpdate(ctx, q, s.reservations(), updated, req.ExpectedSeq); err != nil {
			return nil, err
		}
		event, err = q.DeploymentUpdateEvent(ctx, int64(req.DeploymentID), seq, time.Now(), &updated.Value)
		if errors.Is(err, pq.ErrDeploymentUnchanged) {
			event, err = q.GetLatestDeploymentEvent(ctx, int64(req.DeploymentID))
			return nil, err
		}
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.DeploymentMutation(event)), nil
	})
	return event, err
}

// Delete removes the deployment as long as it has no event newer than
// expectedSeq; zero skips the check.
func (s *Service) Delete(ctx apigen.Context, deploymentID int32, expectedSeq int64) error {
	return s.Store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if err := inLockValidateDeploymentDelete(ctx, q, s.Cluster, s.PrimaryNodeID, deploymentID, expectedSeq); err != nil {
			return nil, err
		}
		event, err := q.DeploymentDeleteEvent(ctx, int64(deploymentID), seq, time.Now())
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.DeploymentMutation(event)), nil
	})
}

func cloneDeployment(cfg *apigen.DeploymentEvent) (*apigen.DeploymentEvent, error) {
	spec, err := cloneDeploymentSpec(&cfg.Value.Spec)
	if err != nil {
		return nil, err
	}
	updated := *cfg
	updated.Value.Spec = *spec
	return &updated, nil
}

func setTargetVersion(updated *apigen.DeploymentEvent, targetVersion string) error {
	if err := updated.SetWorkloadState(targetVersion, true); err != nil {
		return InvalidConfigErrf("spec: %v", err)
	}
	return nil
}
