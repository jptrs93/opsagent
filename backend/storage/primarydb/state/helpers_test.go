package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

// nonEmptySpec returns a valid spec that encodes to non-empty bytes.
func nonEmptySpec() *apigen.DeploymentSpec {
	return &apigen.DeploymentSpec{
		Container1Spec: &apigen.ContainerSpec{
			Source:  apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: "example/app"}},
			Runtime: apigen.ContainerRuntime{User: "1000"},
		},
		Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST},
	}
}

func testSpecWithState(version string, running bool) *apigen.DeploymentSpec {
	spec := nonEmptySpec()
	if err := spec.SetWorkloadState(version, running); err != nil {
		panic(err)
	}
	return spec
}

func createDeploymentForTest(s *Service, ctx apigen.Context, def *apigen.Deployment, inlockValidate pq.Validator) (*apigen.DeploymentEvent, error) {
	var event *apigen.DeploymentEvent
	err := s.Commit(ctx, inlockValidate, func(q *pq.Queries, seq int64) (*Update, error) {
		id, err := q.NextDeploymentID(ctx)
		if err != nil {
			return nil, err
		}
		event, err = q.WriteDeploymentCreate(ctx, id, seq, def)
		if err != nil {
			return nil, err
		}
		return &Update{DeploymentEvents: []*apigen.DeploymentEvent{event}}, nil
	})
	return event, err
}

func updateDeploymentForTest(s *Service, ctx apigen.Context, deploymentID int32, mutate func(def *apigen.Deployment, existing *apigen.DeploymentEvent) error) *apigen.DeploymentEvent {
	var event *apigen.DeploymentEvent
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*Update, error) {
		existing := s.mustLatestEventLocked(deploymentID)
		def := existing.Value
		if err := mutate(&def, existing); err != nil {
			return nil, err
		}
		var err error
		event, err = q.WriteDeploymentUpdate(ctx, int64(deploymentID), seq, &def)
		if err != nil {
			return nil, err
		}
		return &Update{DeploymentEvents: []*apigen.DeploymentEvent{event}}, nil
	}))
	return event
}

func mustCreateDeploymentForNode(s *Service, ctx apigen.Context, spaceID int32, name string, nodeID int32, spec *apigen.DeploymentSpec) *apigen.DeploymentEvent {
	return erru.Must(createDeploymentForTest(s, ctx, &apigen.Deployment{NodeID: nodeID, SpaceID: spaceID, Name: name, Spec: *spec}, func(q *pq.Queries) error {
		events, err := q.ListLatestDeploymentEvents(ctx)
		if err != nil {
			return err
		}
		for _, cfg := range events {
			if !cfg.Deleted() && storage.DeploymentKeyMatches(cfg.Value, nodeID, spaceID, name) {
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
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*Update, error) {
		var err error
		event, err = q.WriteDeploymentDelete(ctx, int64(deploymentID), seq)
		if err != nil {
			return nil, err
		}
		return &Update{DeploymentEvents: []*apigen.DeploymentEvent{event}}, nil
	}))
	return event
}

func mustSetDeploymentWorkloadState(s *Service, ctx apigen.Context, deploymentID int32, version string, running bool) {
	updateDeploymentForTest(s, ctx, deploymentID, func(def *apigen.Deployment, existing *apigen.DeploymentEvent) error {
		spec := erru.Must(apigen.DecodeDeploymentSpec(existing.Value.Spec.Encode()))
		if err := spec.SetWorkloadState(version, running); err != nil {
			return err
		}
		def.Spec = *spec
		return nil
	})
}

func mustUpdateDeploymentSpec(s *Service, ctx apigen.Context, deploymentID int32, spec *apigen.DeploymentSpec) {
	updateDeploymentForTest(s, ctx, deploymentID, func(def *apigen.Deployment, existing *apigen.DeploymentEvent) error {
		storedSpec := erru.Must(apigen.DecodeDeploymentSpec(spec.Encode()))
		if err := storedSpec.SetWorkloadState(existing.WorkloadVersion(), existing.WorkloadRunning()); err != nil {
			return err
		}
		def.Spec = *storedSpec
		return nil
	})
}

func (s *Service) mustLatestEventLocked(deploymentID int32) *apigen.DeploymentEvent {
	event := erru.Must(s.q.GetLatestDeploymentEvent(context.Background(), int64(deploymentID)))
	if event.Deleted() {
		panic(fmt.Sprintf("deployment %d is deleted or has no events", deploymentID))
	}
	return event
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
		CreatedAt:         time.UnixMilli(now.UnixMilli()),
		DeploymentID:      deploymentID,
		DeploymentVersion: deploymentVersion,
		NodeID:            nodeID,
		InstanceOrdinal:   instanceOrdinal,
		State:             target,
	}
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*Update, error) {
		cfg, err := q.GetDeploymentEventByVersion(ctx, pq.GetDeploymentEventByVersionParams{DeploymentID: int64(deploymentID), Version: int64(deploymentVersion)})
		if err != nil {
			return nil, err
		}
		inst.DeploymentSpecVersion = cfg.SpecVersion
		inst.SpaceID = cfg.Value.SpaceID
		inst.ID = erru.Must(q.NextScheduledInstanceID(ctx))
		event, err := q.AppendScheduledInstanceEvent(ctx, seq, inst, target, now)
		if err != nil {
			return nil, err
		}
		return &Update{ScheduledInstanceEvents: []*apigen.ScheduledInstanceEvent{event}}, nil
	}))
	return inst
}

func setScheduledInstanceState(s *Service, instanceID int32, target apigen.ScheduledInstanceTarget) {
	ctx := context.Background()
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*Update, error) {
		current, err := q.GetScheduledInstance(ctx, instanceID)
		if err != nil {
			return nil, err
		}
		inst := current.Value
		if inst.State == target {
			return nil, nil
		}
		event, err := q.AppendScheduledInstanceEvent(ctx, seq, &inst, target, time.Now())
		if err != nil {
			return nil, err
		}
		return &Update{ScheduledInstanceEvents: []*apigen.ScheduledInstanceEvent{event}}, nil
	}))
}

func writeInstanceStatusForTest(s *Service, instanceID int32, f func(*apigen.ScheduledInstanceStatus)) {
	ctx := context.Background()
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*Update, error) {
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
		if err := q.InsertScheduledInstanceStatus(ctx, seq, current); err != nil {
			return nil, err
		}
		stored, err := q.GetLatestScheduledInstanceStatus(ctx, instanceID)
		if err != nil {
			return nil, err
		}
		return &Update{InstanceStatuses: []*apigen.ScheduledInstanceStatus{stored}}, nil
	}))
}

// getAssetInRootByKey resolves an asset by key in a space's implicit root
// directory.

func envRefSpec(configIDs map[string]int32, secretIDs map[string]int32) *apigen.DeploymentSpec {
	spec := testSpecWithState("v1", true)
	spec.Container1Spec.Runtime.EnvVars = make(map[string]*apigen.EnvVarValue, len(configIDs)+len(secretIDs))
	for key, id := range configIDs {
		id := id
		spec.Container1Spec.Runtime.EnvVars[key] = &apigen.EnvVarValue{ConfigVersionID: &id}
	}
	for key, id := range secretIDs {
		id := id
		spec.Container1Spec.Runtime.EnvVars[key] = &apigen.EnvVarValue{SecretVersionID: &id}
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
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*Update, error) {
		now := time.Now().UnixMilli()
		var err error
		row, err = q.InsertNodeRow(ctx, pq.InsertNodeParams{CreatedAt: now, EnrolledAt: now, Name: identifier, Identifier: identifier, Status: int64(apigen.NodeLifecycleStatus_NODE_MEMBER_NORMAL), RolesJSON: "[0]", AddressesJSON: "[]", GlobalSeq: seq})
		if err != nil {
			return nil, err
		}
		return &Update{NodeEvents: []*apigen.NodeEvent{&row.Event}}, nil
	}))
	return testNodeRef{ID: row.Event.NodeID, Identifier: identifier}
}

func setNodeStatusForTest(s *Service, identifier string, connected bool, at time.Time) {
	ctx := context.Background()
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*Update, error) {
		status, err := q.SetNodeConnectionStatus(ctx, seq, identifier, connected, time.UnixMilli(at.UnixMilli()))
		if err != nil {
			return nil, err
		}
		return &Update{NodeStatuses: []*apigen.NodeStatus{status}}, nil
	}))
}

func createSpaceForTest(s *Service, name string) *apigen.Space {
	ctx := context.Background()
	var space apigen.Space
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*Update, error) {
		var err error
		space, err = q.CreateSpace(ctx, name)
		if err != nil {
			return nil, err
		}
		return &Update{Spaces: []*apigen.Space{&space}}, nil
	}))
	return &space
}

func deleteSpaceForTest(s *Service, id int32) {
	ctx := context.Background()
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*Update, error) {
		if err := q.DeleteSpace(ctx, int64(id)); err != nil {
			return nil, err
		}
		return &Update{Spaces: []*apigen.Space{{ID: id, Deleted: true}}}, nil
	}))
}

const defaultSpaceID int32 = 1

func putInlineAssetContentForTest(s *Service, blob []byte) string {
	sum := sha256.Sum256(blob)
	sha := hex.EncodeToString(sum[:])
	ctx := context.Background()
	if _, err := s.q.GetAssetStoreRowBySha(ctx, sha); err == nil {
		return sha
	}
	erru.Must(s.q.InsertAssetStoreRow(ctx, pq.InsertAssetStoreRowParams{ID: uuid.Must(uuid.NewV7()).String(), Sha256: sha, SizeBytes: int64(len(blob)), InlineBlob: blob, CreatedAt: time.Now().UnixMilli()}))
	return sha
}

func setAssetByKeyForTest(s *Service, key string, blob []byte) *apigen.AssetEvent {
	ctx := context.Background()
	sha := putInlineAssetContentForTest(s, blob)
	now := time.Now().UnixMilli()
	var event apigen.AssetEvent
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*Update, error) {
		existing, err := q.GetAssetInDirectoryByKey(ctx, pq.GetAssetInDirectoryByKeyParams{SpaceID: int64(defaultSpaceID), AssetDirectoryID: 0, Key: key})
		if err == nil {
			prev := erru.Must(q.GetLatestAssetEvent(ctx, existing.ID))
			event = prev
			event.EventID, event.Seq, event.EventTime, event.EventType = 0, seq, now, apigen.EventType_EVENT_TYPE_UPDATE
			event.Version++
			event.ValueVersion++
			event.Value.Fs = &apigen.AssetFs{Key: prev.Value.Fs.Key, DirectoryID: prev.Value.Fs.DirectoryID}
			event.Value.SizeBytes, event.Value.Sha256 = int64(len(blob)), sha
		} else if errors.Is(err, sql.ErrNoRows) {
			id := erru.Must(q.NextAssetID(ctx))
			event = apigen.AssetEvent{Seq: seq, EventTime: now, CreatedTime: now, AssetID: int32(id), Version: 1, ValueVersion: 1, SpaceVersion: 1, Value: apigen.Asset{Fs: &apigen.AssetFs{Key: key}, SpaceID: defaultSpaceID, SizeBytes: int64(len(blob)), Sha256: sha}, EventType: apigen.EventType_EVENT_TYPE_CREATE}
		} else {
			return nil, err
		}
		if err := q.InsertAssetEvent(ctx, &event); err != nil {
			return nil, err
		}
		return &Update{AssetEvents: []*apigen.AssetEvent{&event}}, nil
	}))
	return &event
}

func deleteAssetForTest(s *Service, assetID int32) {
	ctx := context.Background()
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*Update, error) {
		prev, err := q.GetLatestAssetEvent(ctx, int64(assetID))
		if err != nil {
			return nil, err
		}
		event := prev
		event.EventID, event.Seq, event.EventTime, event.EventType = 0, seq, time.Now().UnixMilli(), apigen.EventType_EVENT_TYPE_DELETE
		event.Version++
		event.Value.Fs = &apigen.AssetFs{Key: prev.Value.Fs.Key, DirectoryID: prev.Value.Fs.DirectoryID}
		if err := q.InsertAssetEvent(ctx, &event); err != nil {
			return nil, err
		}
		return &Update{AssetEvents: []*apigen.AssetEvent{&event}}, nil
	}))
}

func createAssetDirectoryForTest(s *Service, spaceID, parentID int32, key string, author int32) apigen.AssetDirectory {
	ctx := context.Background()
	var d apigen.AssetDirectory
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*Update, error) {
		var err error
		d, err = q.InsertAssetDirectory(ctx, pq.InsertAssetDirectoryParams{SpaceID: int64(spaceID), Key: key, ParentID: int64(parentID), CreatedAt: time.Now().UnixMilli(), Author: int64(author)})
		if err != nil {
			return nil, err
		}
		return &Update{AssetDirectories: []*apigen.AssetDirectory{&d}}, nil
	}))
	return d
}

func deleteAssetDirectoryForTest(s *Service, id int32) {
	ctx := context.Background()
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*Update, error) {
		d, err := q.GetAssetDirectoryByID(ctx, int64(id))
		if err != nil {
			return nil, err
		}
		if err := q.DeleteAssetDirectory(ctx, int64(id)); err != nil {
			return nil, err
		}
		d.Deleted = true
		return &Update{AssetDirectories: []*apigen.AssetDirectory{&d}}, nil
	}))
}
