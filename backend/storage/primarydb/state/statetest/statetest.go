package statetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func createDeployment(s *state.Service, ctx apigen.Context, def *apigen.Deployment, inlockValidate func(*pq.Queries) error) (*apigen.DeploymentEvent, error) {
	var event *apigen.DeploymentEvent
	err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		if inlockValidate != nil {
			if err := inlockValidate(q); err != nil {
				return nil, err
			}
		}
		id, err := q.NextDeploymentID(ctx)
		if err != nil {
			return nil, err
		}
		event, err = q.WriteDeploymentCreate(ctx, id, seq, time.Now(), def)
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.DeploymentMutation(event)), nil
	})
	return event, err
}

func updateDeployment(s *state.Service, ctx apigen.Context, deploymentID int32, mutate func(def *apigen.Deployment, existing *apigen.DeploymentEvent) error) *apigen.DeploymentEvent {
	var event *apigen.DeploymentEvent
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
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
		event, err = q.WriteDeploymentUpdate(ctx, int64(deploymentID), seq, time.Now(), &def)
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

func MustCreateDeploymentForNode(s *state.Service, ctx apigen.Context, spaceID int32, name string, nodeID int32, spec *apigen.DeploymentSpec) *apigen.DeploymentEvent {
	return mustCreateDeploymentForNode(s, ctx, spaceID, name, nodeID, true, spec)
}

func MustCreateStoppedDeploymentForNode(s *state.Service, ctx apigen.Context, spaceID int32, name string, nodeID int32, spec *apigen.DeploymentSpec) *apigen.DeploymentEvent {
	return mustCreateDeploymentForNode(s, ctx, spaceID, name, nodeID, false, spec)
}

func mustCreateDeploymentForNode(s *state.Service, ctx apigen.Context, spaceID int32, name string, nodeID int32, running bool, spec *apigen.DeploymentSpec) *apigen.DeploymentEvent {
	stored := erru.Must(apigen.DecodeDeploymentSpec(spec.Encode()))
	return erru.Must(createDeployment(s, ctx, &apigen.Deployment{Scheduling: apigen.DedicatedScheduling(running, nodeID), SpaceID: spaceID, Name: name, Spec: *stored}, func(q *pq.Queries) error {
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

func UpdateDeploymentSpec(s *state.Service, ctx apigen.Context, deploymentID int32, spec *apigen.DeploymentSpec) *apigen.DeploymentEvent {
	return updateDeployment(s, ctx, deploymentID, func(def *apigen.Deployment, _ *apigen.DeploymentEvent) error {
		def.Spec = *erru.Must(apigen.DecodeDeploymentSpec(spec.Encode()))
		return nil
	})
}

func UpdateDeploymentSpecKeepingWorkload(s *state.Service, ctx apigen.Context, deploymentID int32, spec *apigen.DeploymentSpec) *apigen.DeploymentEvent {
	return updateDeployment(s, ctx, deploymentID, func(def *apigen.Deployment, existing *apigen.DeploymentEvent) error {
		stored := erru.Must(apigen.DecodeDeploymentSpec(spec.Encode()))
		if err := stored.SetWorkloadVersion(existing.WorkloadVersion()); err != nil {
			return err
		}
		def.Spec = *stored
		return nil
	})
}

func SetDeploymentWorkloadState(s *state.Service, ctx apigen.Context, deploymentID int32, version string, running bool) *apigen.DeploymentEvent {
	return updateDeployment(s, ctx, deploymentID, func(def *apigen.Deployment, existing *apigen.DeploymentEvent) error {
		spec := erru.Must(apigen.DecodeDeploymentSpec(existing.Value.Spec.Encode()))
		if err := spec.SetWorkloadVersion(version); err != nil {
			return err
		}
		def.Spec = *spec
		def.Scheduling.Running = running
		return nil
	})
}

func RestartDeployment(s *state.Service, ctx apigen.Context, deploymentID int32) *apigen.DeploymentEvent {
	return updateDeployment(s, ctx, deploymentID, func(def *apigen.Deployment, _ *apigen.DeploymentEvent) error {
		def.Scheduling.Generation++
		return nil
	})
}

func RenameDeployment(s *state.Service, ctx apigen.Context, deploymentID int32, name string) *apigen.DeploymentEvent {
	return updateDeployment(s, ctx, deploymentID, func(def *apigen.Deployment, _ *apigen.DeploymentEvent) error {
		def.Name = name
		return nil
	})
}

func MoveDeploymentSpace(s *state.Service, ctx apigen.Context, deploymentID, spaceID int32) *apigen.DeploymentEvent {
	return updateDeployment(s, ctx, deploymentID, func(def *apigen.Deployment, _ *apigen.DeploymentEvent) error {
		def.SpaceID = spaceID
		return nil
	})
}

func DeleteDeployment(s *state.Service, ctx apigen.Context, deploymentID int32) *apigen.DeploymentEvent {
	var event *apigen.DeploymentEvent
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		var err error
		event, err = q.WriteDeploymentDelete(ctx, int64(deploymentID), seq, time.Now())
		if err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.DeploymentMutation(event)), nil
	}))
	return event
}

func CreateScheduledInstance(s *state.Service, deploymentID, deploymentVersion, nodeID, instanceOrdinal int32, target apigen.ScheduledInstanceTarget) *apigen.ScheduledInstance {
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
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
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
		return pq.NewUpdate(pq.ScheduledInstanceMutation(event)), nil
	}))
	return inst
}

func SetScheduledInstanceState(s *state.Service, instanceID int32, target apigen.ScheduledInstanceTarget) {
	ctx := context.Background()
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
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
		return pq.NewUpdate(pq.ScheduledInstanceMutation(event)), nil
	}))
}

func NonFinalInstances(s *state.Service, deploymentID int32) []*apigen.ScheduledInstance {
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
		Container1Spec: &apigen.ContainerSpec{
			Source:  apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: "example/app"}},
			Runtime: apigen.ContainerRuntime{User: "1000"},
		},
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

func DeploymentEnvRef(t testing.TB, cfg *apigen.DeploymentEvent, key string, secret bool) apigen.ValueRef {
	t.Helper()
	value := cfg.Value.Spec.Container1Spec.Runtime.EnvVars[key]
	if value == nil {
		t.Fatalf("deployment %d env %s is missing", cfg.DeploymentID, key)
	}
	if secret {
		if value.Secret == nil {
			t.Fatalf("deployment %d env %s has no secret ref", cfg.DeploymentID, key)
		}
		return *value.Secret
	}
	if value.Config == nil {
		t.Fatalf("deployment %d env %s has no config ref", cfg.DeploymentID, key)
	}
	return *value.Config
}

// Canonical encodes an update with its mutations in a fixed order so two
// updates that carry the same facts compare equal.
func Canonical(update state.Update) []byte {
	cp := update
	cp.Mutations = slices.Clone(update.Mutations)
	sort.Slice(cp.Mutations, func(i, j int) bool { return bytes.Compare(cp.Mutations[i].Encode(), cp.Mutations[j].Encode()) < 0 })
	return cp.Encode()
}

// AssertUpdateMatchesRows checks a published update against the rows the
// commit wrote: the same seq as the database, and the same mutations as the
// event stream replays for that seq.
func AssertUpdateMatchesRows(t testing.TB, s *state.Service, update state.Update) {
	t.Helper()
	ctx := context.Background()
	seq := erru.Must(s.Queries().GetGlobalSeq(ctx))
	if update.Seq != seq {
		t.Fatalf("published sequence = %d, database sequence = %d", update.Seq, seq)
	}
	events := pq.Events(erru.Must(s.Queries().MutationsInRange(ctx, seq-1, seq)))
	if len(events) != 1 {
		t.Fatalf("seq %d replays as %d events", seq, len(events))
	}
	if !bytes.Equal(Canonical(update), Canonical(*events[0])) {
		t.Fatalf("published update differs from persisted rows at seq %d\ngot: %+v\nwant: %+v", update.Seq, update, *events[0])
	}
}

// Fold applies mutations in order and returns the live entity payloads keyed
// by type and id, the state a stream consumer holds after the events.
func Fold(events []*apigen.CoreWriteUpdate) map[apigen.CoreEntityType]map[int64]*apigen.CoreEntity {
	out := map[apigen.CoreEntityType]map[int64]*apigen.CoreEntity{}
	for _, e := range events {
		for _, m := range e.Mutations {
			if out[m.Type()] == nil {
				out[m.Type()] = map[int64]*apigen.CoreEntity{}
			}
			if m.Delete != nil {
				delete(out[m.Type()], m.EntityID())
				continue
			}
			out[m.Type()][m.EntityID()] = m.Entity()
		}
	}
	return out
}

// Bootstrap returns the compacted history as events, the same shape the
// stream sends a fresh subscriber.
func Bootstrap(t testing.TB, q *pq.Queries) []*apigen.CoreWriteUpdate {
	t.Helper()
	return pq.Events(erru.Must(q.BootstrapMutations(context.Background())))
}

// Live returns the entity payloads a fresh subscriber holds after folding the
// bootstrap.
func Live(t testing.TB, q *pq.Queries, typ apigen.CoreEntityType) map[int64]*apigen.CoreEntity {
	t.Helper()
	return Fold(Bootstrap(t, q))[typ]
}
