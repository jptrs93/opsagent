package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jptrs93/goutil/erru"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func nonEmptySpec() *apigen.DeploymentSpec {
	return &apigen.DeploymentSpec{
		Container1Spec: &apigen.ContainerSpec{
			Source:  apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: "example/app"}},
			Runtime: apigen.ContainerRuntime{User: "1000"},
		},
		Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST},
	}
}

func testSpecWithVersion(version string) *apigen.DeploymentSpec {
	spec := nonEmptySpec()
	if err := spec.SetWorkloadVersion(version); err != nil {
		panic(err)
	}
	return spec
}

func createDeploymentForTest(s *Service, ctx apigen.Context, def *apigen.Deployment, inlockValidate func(*pq.Queries) error) (*apigen.DeploymentEvent, error) {
	var event *apigen.DeploymentEvent
	err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		if inlockValidate != nil {
			if err := inlockValidate(q); err != nil {
				return nil, err
			}
		}
		id, err := q.NextDeploymentID(ctx)
		if err != nil {
			return nil, err
		}
		event = pq.DeploymentCreateEvent(ctx, id, seq, time.Now(), def)
		return pq.NewUpdate(pq.DeploymentMutation(event)), nil
	})
	return event, err
}

func updateDeploymentForTest(s *Service, ctx apigen.Context, deploymentID int32, mutate func(def *apigen.Deployment, existing *apigen.DeploymentEvent) error) *apigen.DeploymentEvent {
	var event *apigen.DeploymentEvent
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		existing, err := q.GetLatestDeploymentEvent(ctx, int64(deploymentID))
		if err != nil {
			return nil, err
		}
		if existing.Deleted() {
			return nil, fmt.Errorf("deployment %d is deleted", deploymentID)
		}
		def := existing.Value
		if err := mutate(&def, existing); err != nil {
			return nil, err
		}
		event, err = q.DeploymentUpdateEvent(ctx, int64(deploymentID), seq, time.Now(), &def)
		if errors.Is(err, pq.ErrDeploymentUnchanged) {
			event = existing
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.DeploymentMutation(event)), nil
	}))
	return event
}

func mustCreateDeploymentForNode(s *Service, ctx apigen.Context, spaceID int32, name string, nodeID int32, spec *apigen.DeploymentSpec) *apigen.DeploymentEvent {
	return mustCreateDeploymentForNodeRunning(s, ctx, spaceID, name, nodeID, true, spec)
}

func mustCreateDeploymentForNodeRunning(s *Service, ctx apigen.Context, spaceID int32, name string, nodeID int32, running bool, spec *apigen.DeploymentSpec) *apigen.DeploymentEvent {
	stored := erru.Must(apigen.DecodeDeploymentSpec(spec.Encode()))
	return erru.Must(createDeploymentForTest(s, ctx, &apigen.Deployment{Scheduling: apigen.DedicatedScheduling(running, nodeID), SpaceID: spaceID, Name: name, Spec: *stored}, func(q *pq.Queries) error {
		events, err := q.ListActiveDeployments(ctx)
		if err != nil {
			return err
		}
		for _, cfg := range events {
			if storage.DeploymentKeyMatches(cfg.Value, nodeID, spaceID, name) {
				return fmt.Errorf("deployment node=%d space=%d name=%q already exists", nodeID, spaceID, name)
			}
		}
		return nil
	}))
}

func updateDeploymentSpec(s *Service, ctx apigen.Context, deploymentID int32, spec *apigen.DeploymentSpec) *apigen.DeploymentEvent {
	return updateDeploymentForTest(s, ctx, deploymentID, func(def *apigen.Deployment, _ *apigen.DeploymentEvent) error {
		def.Spec = *spec
		return nil
	})
}

func moveDeploymentSpace(s *Service, ctx apigen.Context, deploymentID, spaceID int32) *apigen.DeploymentEvent {
	return updateDeploymentForTest(s, ctx, deploymentID, func(def *apigen.Deployment, _ *apigen.DeploymentEvent) error {
		def.SpaceID = spaceID
		return nil
	})
}

func deleteDeployment(s *Service, ctx apigen.Context, deploymentID int32) *apigen.DeploymentEvent {
	var event *apigen.DeploymentEvent
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		var err error
		event, err = q.DeploymentDeleteEvent(ctx, int64(deploymentID), seq, time.Now())
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.DeploymentMutation(event)), nil
	}))
	return event
}

func mustSetDeploymentWorkloadState(s *Service, ctx apigen.Context, deploymentID int32, version string, running bool) {
	updateDeploymentForTest(s, ctx, deploymentID, func(def *apigen.Deployment, existing *apigen.DeploymentEvent) error {
		spec := erru.Must(apigen.DecodeDeploymentSpec(existing.Value.Spec.Encode()))
		if err := spec.SetWorkloadVersion(version); err != nil {
			return err
		}
		def.Spec = *spec
		def.Scheduling.Running = running
		return nil
	})
}

func mustUpdateDeploymentSpec(s *Service, ctx apigen.Context, deploymentID int32, spec *apigen.DeploymentSpec) {
	updateDeploymentForTest(s, ctx, deploymentID, func(def *apigen.Deployment, existing *apigen.DeploymentEvent) error {
		storedSpec := erru.Must(apigen.DecodeDeploymentSpec(spec.Encode()))
		if err := storedSpec.SetWorkloadVersion(existing.WorkloadVersion()); err != nil {
			return err
		}
		def.Spec = *storedSpec
		return nil
	})
}

func fetchDeploymentForTest(s *Service, deploymentID int32) *apigen.DeploymentEvent {
	event, err := s.q.GetLatestDeploymentEvent(context.Background(), int64(deploymentID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return erru.Must(event, err)
}

func createScheduledInstanceForTest(s *Service, deploymentID, deploymentVersion, nodeID, instanceOrdinal int32, target apigen.ScheduledInstanceTarget) *apigen.ScheduledInstance {
	ctx := context.Background()
	now := time.Now()
	inst := &apigen.ScheduledInstance{
		DeploymentID:      deploymentID,
		DeploymentVersion: deploymentVersion,
		NodeID:            nodeID,
		InstanceOrdinal:   instanceOrdinal,
		State:             target,
	}
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		cfg, err := q.GetDeploymentEventByVersion(ctx, pq.GetDeploymentEventByVersionParams{DeploymentID: int64(deploymentID), Version: int64(deploymentVersion)})
		if err != nil {
			return nil, err
		}
		inst.DeploymentSpecVersion = cfg.SpecVersion
		inst.SpaceID = cfg.Value.SpaceID
		inst.ID = erru.Must(q.NextScheduledInstanceID(ctx))
		event := pq.NewScheduledInstanceEvent(seq, inst, target, now)
		return pq.NewUpdate(pq.ScheduledInstanceMutation(apigen.AuthzVerb_AUTHZ_VERB_CREATE, event)), nil
	}))
	return inst
}

func setScheduledInstanceState(s *Service, instanceID int32, target apigen.ScheduledInstanceTarget) {
	ctx := context.Background()
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		current, err := q.GetScheduledInstance(ctx, instanceID)
		if errors.Is(err, sql.ErrNoRows) && target == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if current.Value.State == target {
			return nil, nil
		}
		event := pq.ScheduledInstanceTransition(seq, current, target, time.Now())
		return pq.NewUpdate(pq.ScheduledInstanceMutation(apigen.AuthzVerb_AUTHZ_VERB_UPDATE, event)), nil
	}))
}

func writeInstanceStatusForTest(s *Service, instanceID int32, f func(*apigen.ScheduledInstanceStatus)) {
	ctx := context.Background()
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		event, err := q.GetScheduledInstance(ctx, instanceID)
		if err != nil {
			return nil, err
		}
		current, err := q.GetLatestScheduledInstanceStatus(ctx, instanceID)
		if errors.Is(err, sql.ErrNoRows) {
			current = &apigen.ScheduledInstanceStatus{}
		} else if err != nil {
			return nil, err
		}
		current.ScheduledInstanceID = instanceID
		current.DeploymentID = event.Value.DeploymentID
		f(current)
		return pq.NewUpdate(pq.ScheduledInstanceStatusMutation(seq, time.Now().UnixMilli(), pq.CanonicalScheduledInstanceStatus(current))), nil
	}))
}

func envRefSpec(configs map[string]apigen.ValueRef, secrets map[string]apigen.ValueRef) *apigen.DeploymentSpec {
	spec := testSpecWithVersion("v1")
	spec.Container1Spec.Runtime.EnvVars = make(map[string]*apigen.EnvVarValue, len(configs)+len(secrets))
	for key, ref := range configs {
		ref := ref
		spec.Container1Spec.Runtime.EnvVars[key] = &apigen.EnvVarValue{Config: &ref}
	}
	for key, ref := range secrets {
		ref := ref
		spec.Container1Spec.Runtime.EnvVars[key] = &apigen.EnvVarValue{Secret: &ref}
	}
	return spec
}

func activeDeploymentsForTest(s *Service, predicate storage.DeploymentPredicate) []apigen.DeploymentEvent {
	events := erru.Must(s.q.ListActiveDeployments(context.Background()))
	out := make([]apigen.DeploymentEvent, 0, len(events))
	for _, cfg := range events {
		if predicate != nil && !predicate(*cfg) {
			continue
		}
		out = append(out, *cfg)
	}
	return out
}

type testNodeRef struct {
	ID         int32
	Identifier string
}

func testNode(s *Service, identifier string) testNodeRef {
	ctx := context.Background()
	if row, err := s.q.GetNodeRowByIdentifier(ctx, identifier); err == nil {
		return testNodeRef{ID: row.Event.NodeID, Identifier: identifier}
	}
	var row pq.CurrentNode
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		now := time.Now().UnixMilli()
		var err error
		row, err = q.NewNode(ctx, seq, now, apigen.Node{Status: apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL,
			Operator: apigen.NodeOperator{Name: identifier, EnrolledTime: now, Roles: []int32{0}}, Reported: apigen.NodeReported{Identifier: identifier}})
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.NodeMutation(apigen.AuthzVerb_AUTHZ_VERB_CREATE, &row.Event)), nil
	}))
	return testNodeRef{ID: row.Event.NodeID, Identifier: identifier}
}

func setNodeStatusForTest(s *Service, identifier string, connected bool, at time.Time) {
	ctx := context.Background()
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		status, err := q.NodeConnectionStatus(ctx, identifier, connected, time.UnixMilli(at.UnixMilli()))
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.NodeStatusMutation(seq, time.Now().UnixMilli(), status)), nil
	}))
}

func createSpaceForTest(s *Service, name string) *apigen.Space {
	ctx := context.Background()
	var space apigen.Space
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_SPACE)
		if err != nil {
			return nil, err
		}
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), EventType: apigen.AuthzVerb_AUTHZ_VERB_CREATE}
		space = apigen.Space{ID: int32(id), Name: name}
		return pq.NewUpdate(pq.SpaceMutation(meta, space)), nil
	}))
	return &space
}

func deleteSpaceForTest(s *Service, id int32) {
	ctx := context.Background()
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		if _, err := q.GetSpace(ctx, int64(id)); err != nil {
			return nil, err
		}
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli()}
		return pq.NewUpdate(pq.DeleteMutation(meta, apigen.CoreEntityType_CORE_ENTITY_SPACE, int64(id))), nil
	}))
}

const defaultSpaceID int32 = 1

func putAssetContentForTest(s *Service, blob []byte) (sha, storageKey string) {
	sum := sha256.Sum256(blob)
	sha = hex.EncodeToString(sum[:])
	ctx := context.Background()
	if row, err := s.q.GetAssetStoreRowBySha(ctx, sha); err == nil {
		return sha, row.ID
	}
	row := erru.Must(s.q.InsertAssetStoreRow(ctx, pq.InsertAssetStoreRowParams{ID: uuid.Must(uuid.NewV7()).String(), Sha256: sha, SizeBytes: int64(len(blob)), LocalStatus: 1, CreatedAt: time.Now().UnixMilli()}))
	return sha, row.ID
}
