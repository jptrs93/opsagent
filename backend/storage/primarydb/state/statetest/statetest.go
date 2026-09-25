package statetest

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/ptru"

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
		event, err = q.WriteDeploymentCreate(ctx, id, seq, def)
		if err != nil {
			return nil, err
		}
		return &state.Update{DeploymentEvents: []*apigen.DeploymentEvent{event}}, nil
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
		event, err = q.WriteDeploymentUpdate(ctx, int64(deploymentID), seq, &def)
		if err != nil {
			return nil, err
		}
		return &state.Update{DeploymentEvents: []*apigen.DeploymentEvent{event}}, nil
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
	return updateDeployment(s, ctx, deploymentID, func(_ *apigen.Deployment, _ *apigen.DeploymentEvent) error {
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
		event, err = q.WriteDeploymentDelete(ctx, int64(deploymentID), seq)
		if err != nil {
			return nil, err
		}
		return &state.Update{DeploymentEvents: []*apigen.DeploymentEvent{event}}, nil
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
		return &state.Update{ScheduledInstanceEvents: []*apigen.ScheduledInstanceEvent{event}}, nil
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
		return &state.Update{ScheduledInstanceEvents: []*apigen.ScheduledInstanceEvent{event}}, nil
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

func rereadUpdateAtSeq(ctx context.Context, q *pq.Queries, seq int64) state.Update {
	core := state.Update{Seq: seq}
	core.DeploymentEvents = erru.Must(q.ListDeploymentEventsAtSeq(ctx, seq))
	core.ScheduledInstanceEvents = erru.Must(q.ListScheduledInstanceEventsAtSeq(ctx, seq))
	core.SecretEvents = erru.Must(q.ListSecretEventsAtSeq(ctx, seq))
	core.ConfigEvents = erru.Must(q.ListConfigEventsAtSeq(ctx, seq))
	for _, row := range erru.Must(q.ListAssetEventsAtSeq(ctx, seq)) {
		core.AssetEvents = append(core.AssetEvents, ptru.To(row))
	}
	core.NetworkPolicyEvents = erru.Must(q.ListNetworkPolicyEventsAtSeq(ctx, seq))
	core.NodeEvents = erru.Must(q.ListNodeEventsAtSeq(ctx, seq))
	for _, row := range erru.Must(q.ListAuthzGrantEventsAtSeq(ctx, seq)) {
		core.AuthzGrantEvents = append(core.AuthzGrantEvents, ptru.To(row))
	}
	if len(erru.Must(q.ListAuthzRuleTemplateEventsAtSeq(ctx, seq))) != 0 {
		core.AuthzRuleTemplates = &apigen.AuthzRuleTemplateList{Items: erru.Must(q.ListAuthzRuleTemplates(ctx))}
	}
	if len(erru.Must(q.ListGlobalAccessRuleEventsAtSeq(ctx, seq))) != 0 {
		core.AuthzGlobalRules = &apigen.AuthzGlobalRuleList{Items: erru.Must(q.ListAuthzGlobalRules(ctx))}
	}
	core.InstanceStatuses = erru.Must(q.ListScheduledInstanceStatusesAtSeq(ctx, seq))
	core.NodeStatuses = erru.Must(q.ListNodeStatusesAtSeq(ctx, seq))
	return core
}

func canonicalUpdate(update state.Update) []byte {
	actual := update
	actual.ValueDirectories, actual.AssetDirectories, actual.Spaces, actual.Users, actual.SystemConfig = nil, nil, nil, nil, nil
	actual.InstanceStatuses = nil
	for _, st := range update.InstanceStatuses {
		cp := *st
		cp.Runner.RunningVersion = ""
		actual.InstanceStatuses = append(actual.InstanceStatuses, &cp)
	}
	return actual.Encode()
}

func AssertUpdateMatchesRows(t testing.TB, s *state.Service, update state.Update) {
	t.Helper()
	ctx := context.Background()
	seq := erru.Must(s.Queries().GetGlobalSeq(ctx))
	if update.Seq != seq {
		t.Fatalf("published sequence = %d, database sequence = %d", update.Seq, seq)
	}
	expected := rereadUpdateAtSeq(ctx, s.Queries(), update.Seq)
	if !bytes.Equal(canonicalUpdate(update), canonicalUpdate(expected)) {
		t.Fatalf("published update differs from persisted rows at seq %d\ngot: %+v\nwant: %+v", update.Seq, update, expected)
	}
}
