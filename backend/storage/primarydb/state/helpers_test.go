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
		Workload: apigen.Workload{Value: apigen.WorkloadValueOneof{Container: &apigen.ContainerSpec{
			Source:          apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{RemoteImage: &apigen.RemoteImage{Image: "example/app"}}},
			Runtime:         apigen.ContainerRuntime{User: "1000"},
			UpgradeStrategy: apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE,
		}}},
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

func createDeploymentForTest(s *Service, ctx apigen.Context, def *apigen.Deployment, inlockValidate func(*pq.Queries) error) (*apigen.DeploymentRecord, error) {
	var record *apigen.DeploymentRecord
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
		record = pq.DeploymentCreateRecord(ctx, id, seq, time.Now(), def)
		return pq.NewUpdate(pq.DeploymentMutation(record)), nil
	})
	return record, err
}

func updateDeploymentForTest(s *Service, ctx apigen.Context, deploymentID uint64, mutate func(def *apigen.Deployment, existing *apigen.DeploymentRecord) error) *apigen.DeploymentRecord {
	var record *apigen.DeploymentRecord
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		existing, err := q.GetLatestDeployment(ctx, deploymentID)
		if err != nil {
			return nil, err
		}
		if existing.Deleted() {
			return nil, fmt.Errorf("deployment %d is deleted", deploymentID)
		}
		def := existing.Deployment
		if err := mutate(&def, existing); err != nil {
			return nil, err
		}
		record, err = q.DeploymentUpdateRecord(ctx, deploymentID, seq, time.Now(), &def)
		if errors.Is(err, pq.ErrDeploymentUnchanged) {
			record = existing
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.DeploymentMutation(record)), nil
	}))
	return record
}

func mustCreateDeploymentForNode(s *Service, ctx apigen.Context, spaceID uint64, name string, nodeID uint64, spec *apigen.DeploymentSpec) *apigen.DeploymentRecord {
	return mustCreateDeploymentForNodeRunning(s, ctx, spaceID, name, nodeID, true, spec)
}

func mustCreateDeploymentForNodeRunning(s *Service, ctx apigen.Context, spaceID uint64, name string, nodeID uint64, running bool, spec *apigen.DeploymentSpec) *apigen.DeploymentRecord {
	stored := erru.Must(apigen.DecodeDeploymentSpec(spec.Encode()))
	return erru.Must(createDeploymentForTest(s, ctx, &apigen.Deployment{Scheduling: apigen.DedicatedScheduling(running, nodeID), SpaceID: spaceID, Name: name, Spec: *stored}, func(q *pq.Queries) error {
		records, err := q.ListActiveDeployments(ctx)
		if err != nil {
			return err
		}
		for _, cfg := range records {
			if storage.DeploymentKeyMatches(cfg.Deployment, nodeID, spaceID, name) {
				return fmt.Errorf("deployment node=%d space=%d name=%q already exists", nodeID, spaceID, name)
			}
		}
		return nil
	}))
}

func updateDeploymentSpec(s *Service, ctx apigen.Context, deploymentID uint64, spec *apigen.DeploymentSpec) *apigen.DeploymentRecord {
	return updateDeploymentForTest(s, ctx, deploymentID, func(def *apigen.Deployment, _ *apigen.DeploymentRecord) error {
		def.Spec = *spec
		return nil
	})
}

func moveDeploymentSpace(s *Service, ctx apigen.Context, deploymentID, spaceID uint64) *apigen.DeploymentRecord {
	return updateDeploymentForTest(s, ctx, deploymentID, func(def *apigen.Deployment, _ *apigen.DeploymentRecord) error {
		def.SpaceID = spaceID
		return nil
	})
}

func deleteDeployment(s *Service, ctx apigen.Context, deploymentID uint64) *apigen.DeploymentRecord {
	var record *apigen.DeploymentRecord
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		var err error
		record, err = q.DeploymentDeleteRecord(ctx, deploymentID, seq, time.Now())
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.DeploymentMutation(record)), nil
	}))
	return record
}

func mustSetDeploymentWorkloadState(s *Service, ctx apigen.Context, deploymentID uint64, version string, running bool) {
	updateDeploymentForTest(s, ctx, deploymentID, func(def *apigen.Deployment, existing *apigen.DeploymentRecord) error {
		spec := erru.Must(apigen.DecodeDeploymentSpec(existing.Deployment.Spec.Encode()))
		if err := spec.SetWorkloadVersion(version); err != nil {
			return err
		}
		def.Spec = *spec
		def.Scheduling.Running = running
		return nil
	})
}

func mustUpdateDeploymentSpec(s *Service, ctx apigen.Context, deploymentID uint64, spec *apigen.DeploymentSpec) {
	updateDeploymentForTest(s, ctx, deploymentID, func(def *apigen.Deployment, existing *apigen.DeploymentRecord) error {
		storedSpec := erru.Must(apigen.DecodeDeploymentSpec(spec.Encode()))
		if err := storedSpec.SetWorkloadVersion(existing.WorkloadVersion()); err != nil {
			return err
		}
		def.Spec = *storedSpec
		return nil
	})
}

func fetchDeploymentForTest(s *Service, deploymentID uint64) *apigen.DeploymentRecord {
	record, err := s.q.GetLatestDeployment(context.Background(), deploymentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return erru.Must(record, err)
}

func createScheduledInstanceForTest(s *Service, deploymentID uint64, deploymentVersion uint32, nodeID uint64, instanceOrdinal uint32, target apigen.ScheduledInstanceTarget) *apigen.ScheduledInstance {
	ctx := context.Background()
	now := time.Now()
	inst := &apigen.ScheduledInstance{
		Deployment:      apigen.DeploymentRef{DeploymentID: deploymentID, Version: deploymentVersion},
		NodeID:          nodeID,
		InstanceOrdinal: instanceOrdinal,
		State:           target,
	}
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		cfg, err := q.GetDeploymentVersion(ctx, deploymentID, deploymentVersion)
		if err != nil {
			return nil, err
		}
		inst.SpaceID = cfg.Deployment.SpaceID
		inst.ID = erru.Must(q.NextScheduledInstanceID(ctx))
		event := pq.NewScheduledInstanceEvent(seq, inst, target, now)
		return pq.NewUpdate(pq.ScheduledInstanceMutation(apigen.AuthzVerb_AUTHZ_VERB_CREATE, event)), nil
	}))
	return inst
}

func setScheduledInstanceState(s *Service, instanceID uint64, target apigen.ScheduledInstanceTarget) {
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

func writeInstanceStatusForTest(s *Service, instanceID uint64, f func(*apigen.ScheduledInstanceStatus)) {
	ctx := context.Background()
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		if _, err := q.GetScheduledInstance(ctx, instanceID); err != nil {
			return nil, err
		}
		current, err := q.GetLatestScheduledInstanceStatus(ctx, instanceID)
		if errors.Is(err, sql.ErrNoRows) {
			current = &apigen.ScheduledInstanceStatus{}
		} else if err != nil {
			return nil, err
		}
		current.ScheduledInstanceID = instanceID
		f(current)
		return pq.NewUpdate(pq.ScheduledInstanceStatusMutation(seq, time.Now().UnixMilli(), pq.CanonicalScheduledInstanceStatus(current))), nil
	}))
}

func runnerStatus(status apigen.RunningStatus, pid uint32) apigen.Maybe[apigen.RunnerStatus] {
	r := apigen.RunnerStatus{Status: status}
	if pid > 0 {
		r.RunningPid = apigen.Some(pid)
	}
	return apigen.Some(r)
}

func envRefSpec(configs map[string]apigen.ValueRef, secrets map[string]apigen.ValueRef) *apigen.DeploymentSpec {
	spec := testSpecWithVersion("v1")
	env := make(map[string]apigen.EnvVar, len(configs)+len(secrets))
	for key, ref := range configs {
		env[key] = apigen.EnvVar{Value: apigen.EnvVarValueOneof{Config: &apigen.ConfigEnv{Config: apigen.ConfigRef{ConfigID: ref.ID, Version: ref.Version}}}}
	}
	for key, ref := range secrets {
		env[key] = apigen.EnvVar{Value: apigen.EnvVarValueOneof{Secret: &apigen.SecretEnv{Secret: apigen.SecretRef{SecretID: ref.ID, Version: ref.Version}}}}
	}
	spec.Container().Runtime.EnvVars = env
	return spec
}

func activeDeploymentsForTest(s *Service, predicate storage.DeploymentPredicate) []apigen.DeploymentRecord {
	records := erru.Must(s.q.ListActiveDeployments(context.Background()))
	out := make([]apigen.DeploymentRecord, 0, len(records))
	for _, cfg := range records {
		if predicate != nil && !predicate(*cfg) {
			continue
		}
		out = append(out, *cfg)
	}
	return out
}

type testNodeRef struct {
	ID         uint64
	Identifier string
}

func testNode(s *Service, identifier string) testNodeRef {
	ctx := context.Background()
	if row, err := s.q.GetNodeRowByIdentifier(ctx, identifier); err == nil {
		return testNodeRef{ID: row.Event.NodeID, Identifier: identifier}
	}
	var row pq.CurrentNode
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		now := time.Now()
		var err error
		row, err = q.NewNode(ctx, seq, now.UnixMilli(), apigen.Node{Status: apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_NORMAL,
			Operator: apigen.NodeOperator{Name: identifier, EnrolledTime: apigen.TimeOf(time.UnixMilli(now.UnixMilli())), Roles: []apigen.NodeRole{apigen.NodeRole_NODE_ROLE_PRIMARY}},
			Reported: apigen.NodeReported{Identifier: identifier, UnderlayAddress: apigen.IpAddress{Value: apigen.IpAddressValueOneof{Ipv4: &apigen.IPv4Address{Octets: []byte{10, 0, 0, 1}}}}}})
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
		space = apigen.Space{ID: id, Name: name}
		return pq.NewUpdate(pq.SpaceMutation(meta, space)), nil
	}))
	return &space
}

func deleteSpaceForTest(s *Service, id uint64) {
	ctx := context.Background()
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		if _, err := q.GetSpace(ctx, id); err != nil {
			return nil, err
		}
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli()}
		return pq.NewUpdate(pq.DeleteMutation(meta, apigen.CoreEntityType_CORE_ENTITY_SPACE, id)), nil
	}))
}

const defaultSpaceID uint64 = 1

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
