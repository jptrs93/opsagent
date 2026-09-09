package deployments

import (
	"context"
	"log/slog"
	"strings"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func Active(q *pq.Queries, predicate storage.DeploymentPredicate) []apigen.DeploymentEvent {
	events := erru.Must(q.ListActiveDeployments(context.Background()))
	out := make([]apigen.DeploymentEvent, 0, len(events))
	for _, cfg := range events {
		if predicate != nil && !predicate(*cfg) {
			continue
		}
		out = append(out, *cfg)
	}
	return out
}

func Deleted(q *pq.Queries, predicate storage.DeploymentPredicate, limit int) []apigen.DeploymentEvent {
	events := erru.Must(q.ListDeletedDeploymentEvents(context.Background()))
	out := make([]apigen.DeploymentEvent, 0, limit)
	for _, cfg := range events {
		if predicate != nil && !predicate(*cfg) {
			continue
		}
		out = append(out, *cfg)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

func EnsureSystem(store *state.Service, nodeID int32, opendeployVersion string) {
	if nodeID <= 0 {
		panic("deployment node ID must be positive")
	}
	opendeployVersion = strings.TrimSpace(opendeployVersion)
	if opendeployVersion == "" {
		panic("EnsureSystem requires an explicit OpenDeploy version")
	}
	ctx := apigen.Context{Ctx: logu.AddTag(context.Background(), "Store")}
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		events, err := q.ListLatestDeploymentEvents(ctx)
		if err != nil {
			return nil, err
		}
		for _, cfg := range events {
			if cfg.Deleted() || !storage.DeploymentKeyMatches(cfg.Value, nodeID, internaldeploy.SpaceID, internaldeploy.SelfName) {
				continue
			}
			if internaldeploy.IsSelfSpec(&cfg.Value.Spec) {
				return nil, nil
			}
			slog.WarnContext(ctx, "repairing system deployment spec", "dep", cfg.DeploymentID, "node", nodeID)
			spec := internaldeploy.SelfSpec()
			if err := spec.SetWorkloadState(cfg.WorkloadVersion(), cfg.WorkloadRunning()); err != nil {
				return nil, err
			}
			def := cfg.Value
			def.Spec = *spec
			event, err := q.WriteDeploymentUpdate(ctx, int64(cfg.DeploymentID), seq, &def)
			if err != nil {
				return nil, err
			}
			return &state.Update{DeploymentEvents: []*apigen.DeploymentEvent{event}}, nil
		}
		spec := internaldeploy.SelfSpec()
		if err := spec.SetWorkloadState(opendeployVersion, true); err != nil {
			return nil, err
		}
		id, err := q.NextDeploymentID(ctx)
		if err != nil {
			return nil, err
		}
		event, err := q.WriteDeploymentCreate(ctx, id, seq, &apigen.Deployment{NodeID: nodeID, SpaceID: internaldeploy.SpaceID, Name: internaldeploy.SelfName, Spec: *spec})
		if err != nil {
			return nil, err
		}
		return &state.Update{DeploymentEvents: []*apigen.DeploymentEvent{event}}, nil
	}); err != nil {
		panic(err)
	}
}

func EnsureNetproxy(store *state.Service, nodeID int32, initialVersion string) *apigen.DeploymentEvent {
	if nodeID <= 0 {
		panic("deployment node ID must be positive")
	}
	desiredVersion := strings.TrimSpace(initialVersion)
	if desiredVersion == "" {
		panic("EnsureNetproxy requires an explicit OpenDeploy version")
	}
	ctx := apigen.Context{Ctx: logu.AddTag(context.Background(), "Store")}
	var event *apigen.DeploymentEvent
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		events, err := q.ListLatestDeploymentEvents(ctx)
		if err != nil {
			return nil, err
		}
		spec := internaldeploy.NetproxySpec()
		for _, cfg := range events {
			if cfg.Deleted() || !storage.DeploymentKeyMatches(cfg.Value, nodeID, internaldeploy.SpaceID, internaldeploy.NetproxyName) {
				continue
			}
			event = cfg
			if err := spec.SetWorkloadState(cfg.WorkloadVersion(), cfg.WorkloadRunning()); err != nil {
				return nil, err
			}
			if pq.DeploymentSpecsEqual(&cfg.Value.Spec, spec) {
				return nil, nil
			}
			slog.WarnContext(ctx, "repairing netproxy deployment spec", "dep", cfg.DeploymentID, "node", nodeID)
			def := cfg.Value
			def.Spec = *spec
			event, err = q.WriteDeploymentUpdate(ctx, int64(cfg.DeploymentID), seq, &def)
			if err != nil {
				return nil, err
			}
			return &state.Update{DeploymentEvents: []*apigen.DeploymentEvent{event}}, nil
		}
		if err := spec.SetWorkloadState(desiredVersion, true); err != nil {
			return nil, err
		}
		id, err := q.NextDeploymentID(ctx)
		if err != nil {
			return nil, err
		}
		event, err = q.WriteDeploymentCreate(ctx, id, seq, &apigen.Deployment{NodeID: nodeID, SpaceID: internaldeploy.SpaceID, Name: internaldeploy.NetproxyName, Spec: *spec})
		if err != nil {
			return nil, err
		}
		return &state.Update{DeploymentEvents: []*apigen.DeploymentEvent{event}}, nil
	})
	return erru.Must(event, err)
}
