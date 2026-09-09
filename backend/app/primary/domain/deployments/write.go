package deployments

import (
	"context"
	"net/http"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
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
	if err := preLockValidateDeploymentCreate(s.Store, s.Secrets, s.GitVersions, ctx, newDep); err != nil {
		return nil, err
	}
	var event *apigen.DeploymentEvent
	err := s.Store.Commit(ctx, func(q *pq.Queries) error {
		return inLockValidateDeploymentCreate(ctx, q, s.reservations(), newDep)
	}, func(q *pq.Queries, seq int64) (*state.Update, error) {
		id, err := q.NextDeploymentID(ctx)
		if err != nil {
			return nil, err
		}
		event, err = q.WriteDeploymentCreate(ctx, id, seq, &newDep.Value)
		if err != nil {
			return nil, err
		}
		return &state.Update{DeploymentEvents: []*apigen.DeploymentEvent{event}}, nil
	})
	return event, err
}

func (s *Service) Update(ctx apigen.Context, existing *apigen.DeploymentEvent, req *apigen.DeploymentUpdateRequestV2) (*apigen.DeploymentEvent, error) {
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
	}
	if err := preLockValidateDeploymentUpdate(s.Store, s.Secrets, s.GitVersions, ctx, existing, req, updated); err != nil {
		return nil, err
	}
	var event *apigen.DeploymentEvent
	err = s.Store.Commit(ctx, func(q *pq.Queries) error {
		return inLockValidateDeploymentUpdate(ctx, q, s.reservations(), updated, req.ExpectedVersion-1)
	}, func(q *pq.Queries, seq int64) (*state.Update, error) {
		event, err = q.WriteDeploymentUpdate(ctx, int64(req.DeploymentID), seq, &updated.Value)
		if err != nil {
			return nil, err
		}
		return &state.Update{DeploymentEvents: []*apigen.DeploymentEvent{event}}, nil
	})
	return event, err
}

func (s *Service) Delete(ctx apigen.Context, deploymentID, expectedVersion int32) error {
	return s.Store.Commit(ctx, func(q *pq.Queries) error {
		return inLockValidateDeploymentDelete(ctx, q, s.Cluster, s.PrimaryNodeID, deploymentID, expectedVersion)
	}, func(q *pq.Queries, seq int64) (*state.Update, error) {
		event, err := q.WriteDeploymentDelete(ctx, int64(deploymentID), seq)
		if err != nil {
			return nil, err
		}
		return &state.Update{DeploymentEvents: []*apigen.DeploymentEvent{event}}, nil
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
