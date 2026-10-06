package statetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func createDeployment(s *state.Service, ctx apigen.Context, def *apigen.Deployment, inlockValidate func(*pq.Queries) error) (*apigen.DeploymentRecord, error) {
	var record *apigen.DeploymentRecord
	err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
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

func updateDeployment(s *state.Service, ctx apigen.Context, deploymentID uint64, mutate func(def *apigen.Deployment, existing *apigen.DeploymentRecord) error) *apigen.DeploymentRecord {
	var record *apigen.DeploymentRecord
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		existing, err := q.GetLatestDeployment(ctx, deploymentID)
		if err != nil {
			return nil, err
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

func MustCreateDeploymentForNode(s *state.Service, ctx apigen.Context, spaceID uint64, name string, nodeID uint64, spec *apigen.DeploymentSpec) *apigen.DeploymentRecord {
	return mustCreateDeploymentForNode(s, ctx, spaceID, name, nodeID, true, spec)
}

func MustCreateStoppedDeploymentForNode(s *state.Service, ctx apigen.Context, spaceID uint64, name string, nodeID uint64, spec *apigen.DeploymentSpec) *apigen.DeploymentRecord {
	return mustCreateDeploymentForNode(s, ctx, spaceID, name, nodeID, false, spec)
}

func mustCreateDeploymentForNode(s *state.Service, ctx apigen.Context, spaceID uint64, name string, nodeID uint64, running bool, spec *apigen.DeploymentSpec) *apigen.DeploymentRecord {
	stored := erru.Must(apigen.DecodeDeploymentSpec(spec.Encode()))
	return erru.Must(createDeployment(s, ctx, &apigen.Deployment{Scheduling: apigen.DedicatedScheduling(running, nodeID), SpaceID: spaceID, Name: name, Spec: *stored}, func(q *pq.Queries) error {
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

func UpdateDeploymentSpec(s *state.Service, ctx apigen.Context, deploymentID uint64, spec *apigen.DeploymentSpec) *apigen.DeploymentRecord {
	return updateDeployment(s, ctx, deploymentID, func(def *apigen.Deployment, _ *apigen.DeploymentRecord) error {
		def.Spec = *erru.Must(apigen.DecodeDeploymentSpec(spec.Encode()))
		return nil
	})
}

func UpdateDeploymentSpecKeepingWorkload(s *state.Service, ctx apigen.Context, deploymentID uint64, spec *apigen.DeploymentSpec) *apigen.DeploymentRecord {
	return updateDeployment(s, ctx, deploymentID, func(def *apigen.Deployment, existing *apigen.DeploymentRecord) error {
		stored := erru.Must(apigen.DecodeDeploymentSpec(spec.Encode()))
		if err := stored.SetWorkloadVersion(existing.WorkloadVersion()); err != nil {
			return err
		}
		def.Spec = *stored
		return nil
	})
}

func SetDeploymentWorkloadState(s *state.Service, ctx apigen.Context, deploymentID uint64, version string, running bool) *apigen.DeploymentRecord {
	return updateDeployment(s, ctx, deploymentID, func(def *apigen.Deployment, existing *apigen.DeploymentRecord) error {
		spec := erru.Must(apigen.DecodeDeploymentSpec(existing.Deployment.Spec.Encode()))
		if err := spec.SetWorkloadVersion(version); err != nil {
			return err
		}
		def.Spec = *spec
		def.Scheduling.Running = running
		return nil
	})
}

func RestartDeployment(s *state.Service, ctx apigen.Context, deploymentID uint64) *apigen.DeploymentRecord {
	return updateDeployment(s, ctx, deploymentID, func(def *apigen.Deployment, _ *apigen.DeploymentRecord) error {
		def.Scheduling.RestartGeneration++
		return nil
	})
}

func RenameDeployment(s *state.Service, ctx apigen.Context, deploymentID uint64, name string) *apigen.DeploymentRecord {
	return updateDeployment(s, ctx, deploymentID, func(def *apigen.Deployment, _ *apigen.DeploymentRecord) error {
		def.Name = name
		return nil
	})
}

func MoveDeploymentSpace(s *state.Service, ctx apigen.Context, deploymentID, spaceID uint64) *apigen.DeploymentRecord {
	return updateDeployment(s, ctx, deploymentID, func(def *apigen.Deployment, _ *apigen.DeploymentRecord) error {
		def.SpaceID = spaceID
		return nil
	})
}

func DeleteDeployment(s *state.Service, ctx apigen.Context, deploymentID uint64) *apigen.DeploymentRecord {
	var record *apigen.DeploymentRecord
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		var err error
		record, err = q.DeploymentDeleteRecord(ctx, deploymentID, seq, time.Now())
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.DeploymentMutation(record)), nil
	}))
	return record
}

func CreateScheduledInstance(s *state.Service, deploymentID uint64, deploymentVersion uint32, nodeID uint64, instanceOrdinal uint32, target apigen.ScheduledInstanceTarget) *apigen.ScheduledInstance {
	ctx := context.Background()
	now := time.Now()
	inst := &apigen.ScheduledInstance{
		Deployment:      apigen.DeploymentRef{DeploymentID: deploymentID, Version: deploymentVersion},
		NodeID:          nodeID,
		InstanceOrdinal: instanceOrdinal,
		State:           target,
	}
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
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

func SetScheduledInstanceState(s *state.Service, instanceID uint64, target apigen.ScheduledInstanceTarget) {
	ctx := context.Background()
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		current, err := q.GetScheduledInstance(ctx, instanceID)
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

func NonFinalInstances(s *state.Service, deploymentID uint64) []*apigen.ScheduledInstance {
	events := erru.Must(s.Queries().ListNonFinalScheduledInstancesForDeployment(context.Background(), deploymentID))
	out := make([]*apigen.ScheduledInstance, 0, len(events))
	for _, event := range events {
		inst := event.Value
		out = append(out, &inst)
	}
	return out
}

func NonEmptySpec() *apigen.DeploymentSpec {
	return &apigen.DeploymentSpec{
		Workload: apigen.Workload{Value: apigen.WorkloadValueOneof{Container: &apigen.ContainerSpec{
			Source:          apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{RemoteImage: &apigen.RemoteImage{Image: "example/app"}}},
			Runtime:         apigen.ContainerRuntime{User: "1000"},
			UpgradeStrategy: apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE,
		}}},
		Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST},
	}
}

func SpecWithVersion(version string) *apigen.DeploymentSpec {
	spec := NonEmptySpec()
	if err := spec.SetWorkloadVersion(version); err != nil {
		panic(err)
	}
	return spec
}

func EnvRefSpec(configs map[string]apigen.ValueRef, secrets map[string]apigen.ValueRef) *apigen.DeploymentSpec {
	spec := SpecWithVersion("v1")
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

func DeploymentEnvRef(t testing.TB, cfg *apigen.DeploymentRecord, key string, secret bool) apigen.ValueRef {
	t.Helper()
	value, ok := cfg.Deployment.Spec.Container().Runtime.EnvVars[key]
	if !ok {
		t.Fatalf("deployment %d env %s is missing", cfg.Deployment.ID, key)
	}
	if secret {
		if value.Value.Secret == nil {
			t.Fatalf("deployment %d env %s has no secret ref", cfg.Deployment.ID, key)
		}
		return apigen.ValueRef{ID: value.Value.Secret.Secret.SecretID, Version: value.Value.Secret.Secret.Version}
	}
	if value.Value.Config == nil {
		t.Fatalf("deployment %d env %s has no config ref", cfg.Deployment.ID, key)
	}
	return apigen.ValueRef{ID: value.Value.Config.Config.ConfigID, Version: value.Value.Config.Config.Version}
}

// Canonical encodes an update with its mutations in a fixed order and
// without meta, so a published update and its write log replay compare
// equal.
func Canonical(update state.WriteUpdate) []byte {
	cp := update
	cp.Mutations = make([]apigen.CoreMutation, 0, len(update.Mutations))
	for _, m := range update.Mutations {
		cp.Mutations = append(cp.Mutations, withoutMeta(m))
	}
	sort.Slice(cp.Mutations, func(i, j int) bool { return bytes.Compare(cp.Mutations[i].Encode(), cp.Mutations[j].Encode()) < 0 })
	return cp.Encode()
}

func withoutMeta(m apigen.CoreMutation) apigen.CoreMutation {
	switch {
	case m.Value.Create != nil:
		c := *m.Value.Create
		c.Meta = apigen.Maybe[apigen.EntityMeta]{}
		return apigen.CoreMutation{Value: apigen.CoreMutationValueOneof{Create: &c}}
	case m.Value.Update != nil:
		u := *m.Value.Update
		u.Meta = apigen.Maybe[apigen.EntityMeta]{}
		return apigen.CoreMutation{Value: apigen.CoreMutationValueOneof{Update: &u}}
	}
	return m
}

// AssertUpdateMatchesRows checks a published update against the rows the
// commit wrote: the same seq as the database, and the same mutations as the
// event stream replays for that seq.
func AssertUpdateMatchesRows(t testing.TB, s *state.Service, update state.WriteUpdate) {
	t.Helper()
	ctx := context.Background()
	seq := erru.Must(s.Queries().GetGlobalSeq(ctx))
	if update.Seq != seq {
		t.Fatalf("published sequence = %d, database sequence = %d", update.Seq, seq)
	}
	events := erru.Must(s.Queries().WriteEventsInRange(ctx, seq-1, seq))
	if len(events) != 1 {
		t.Fatalf("seq %d replays as %d events", seq, len(events))
	}
	if !bytes.Equal(Canonical(update), Canonical(*events[0])) {
		t.Fatalf("published update differs from persisted rows at seq %d\ngot: %+v\nwant: %+v", update.Seq, update, *events[0])
	}
}

// Fold applies mutations in order and returns the live entity payloads keyed
// by type and id, the state a stream consumer holds after the events.
func Fold(events []*apigen.CoreWriteUpdate) map[apigen.CoreEntityType]map[uint64]*apigen.CoreEntity {
	out := map[apigen.CoreEntityType]map[uint64]*apigen.CoreEntity{}
	for _, e := range events {
		for i := range e.Mutations {
			m := &e.Mutations[i]
			if out[m.Type()] == nil {
				out[m.Type()] = map[uint64]*apigen.CoreEntity{}
			}
			if m.Value.Delete != nil {
				delete(out[m.Type()], m.EntityID())
				continue
			}
			out[m.Type()][m.EntityID()] = m.Entity()
		}
	}
	return out
}

// Snapshot returns the opening snapshot, the same shape the stream sends a
// fresh subscriber.
func Snapshot(t testing.TB, q *pq.Queries) []*apigen.MaterialisedEntity {
	t.Helper()
	return erru.Must(q.Snapshot(context.Background()))
}

// FoldSnapshot returns, by type and id, the live entity payloads a fresh
// subscriber holds after the opening snapshot: the newest retained version of
// each entity, and nothing for deleted ones.
func FoldSnapshot(entries []*apigen.MaterialisedEntity) map[apigen.CoreEntityType]map[uint64]*apigen.CoreEntity {
	out := map[apigen.CoreEntityType]map[uint64]*apigen.CoreEntity{}
	for _, e := range entries {
		if e.Meta.Deleted {
			continue
		}
		if out[e.EntityType] == nil {
			out[e.EntityType] = map[uint64]*apigen.CoreEntity{}
		}
		out[e.EntityType][e.EntityID] = &e.Entity
	}
	return out
}

// Live returns the entity payloads a fresh subscriber holds after the opening
// snapshot.
func Live(t testing.TB, q *pq.Queries, typ apigen.CoreEntityType) map[uint64]*apigen.CoreEntity {
	t.Helper()
	return FoldSnapshot(Snapshot(t, q))[typ]
}
