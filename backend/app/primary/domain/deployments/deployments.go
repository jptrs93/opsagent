package deployments

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func Active(q *pq.Queries, predicate storage.DeploymentPredicate) []apigen.DeploymentRecord {
	records := erru.Must(q.ListActiveDeployments(context.Background()))
	out := make([]apigen.DeploymentRecord, 0, len(records))
	for _, cfg := range records {
		if predicate != nil && !predicate(*cfg) {
			continue
		}
		out = append(out, *cfg)
	}
	return out
}

func Deleted(q *pq.Queries, predicate storage.DeploymentPredicate, limit int) []apigen.DeploymentRecord {
	records := erru.Must(q.ListDeletedDeployments(context.Background()))
	out := make([]apigen.DeploymentRecord, 0, limit)
	for _, cfg := range records {
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

func EnsureSystem(store *state.Service, nodeID uint64, opendeployVersion string) {
	if nodeID == 0 {
		panic("deployment node ID must be positive")
	}
	opendeployVersion = strings.TrimSpace(opendeployVersion)
	if opendeployVersion == "" {
		panic("EnsureSystem requires an explicit OpenDeploy version")
	}
	ctx := apigen.Context{Ctx: logu.AddTag(context.Background(), "Store")}
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		records, err := q.ListActiveDeployments(ctx)
		if err != nil {
			return nil, err
		}
		for _, cfg := range records {
			if !storage.DeploymentKeyMatches(cfg.Deployment, nodeID, internaldeploy.SpaceID, internaldeploy.SelfName) {
				continue
			}
			if internaldeploy.IsSelfSpec(&cfg.Deployment.Spec) {
				return nil, nil
			}
			slog.WarnContext(ctx, "repairing system deployment spec", "dep", cfg.Deployment.ID, "node", nodeID)
			spec := internaldeploy.SelfSpec()
			if err := spec.SetWorkloadVersion(cfg.WorkloadVersion()); err != nil {
				return nil, err
			}
			def := cfg.Deployment
			def.Spec = *spec
			def.Scheduling.Running = true
			record, err := q.DeploymentUpdateRecord(ctx, cfg.Deployment.ID, seq, time.Now(), &def)
			if err != nil {
				return nil, err
			}
			return pq.NewUpdate(pq.DeploymentMutation(record)), nil
		}
		spec := internaldeploy.SelfSpec()
		if err := spec.SetWorkloadVersion(opendeployVersion); err != nil {
			return nil, err
		}
		id, err := q.NextDeploymentID(ctx)
		if err != nil {
			return nil, err
		}
		record := pq.DeploymentCreateRecord(ctx, id, seq, time.Now(), &apigen.Deployment{Scheduling: apigen.DedicatedScheduling(true, nodeID), SpaceID: internaldeploy.SpaceID, Name: internaldeploy.SelfName, Spec: *spec})
		return pq.NewUpdate(pq.DeploymentMutation(record)), nil
	}); err != nil {
		panic(err)
	}
}

func EnsureNetproxy(store *state.Service, nodeID uint64, initialVersion string) *apigen.DeploymentRecord {
	if nodeID == 0 {
		panic("deployment node ID must be positive")
	}
	desiredVersion := strings.TrimSpace(initialVersion)
	if desiredVersion == "" {
		panic("EnsureNetproxy requires an explicit OpenDeploy version")
	}
	ctx := apigen.Context{Ctx: logu.AddTag(context.Background(), "Store")}
	var record *apigen.DeploymentRecord
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		records, err := q.ListActiveDeployments(ctx)
		if err != nil {
			return nil, err
		}
		spec := internaldeploy.NetproxySpec()
		for _, cfg := range records {
			if !storage.DeploymentKeyMatches(cfg.Deployment, nodeID, internaldeploy.SpaceID, internaldeploy.NetproxyName) {
				continue
			}
			record = cfg
			if err := spec.SetWorkloadVersion(cfg.WorkloadVersion()); err != nil {
				return nil, err
			}
			if pq.DeploymentSpecsEqual(&cfg.Deployment.Spec, spec) {
				return nil, nil
			}
			slog.WarnContext(ctx, "repairing netproxy deployment spec", "dep", cfg.Deployment.ID, "node", nodeID)
			def := cfg.Deployment
			def.Spec = *spec
			record, err = q.DeploymentUpdateRecord(ctx, cfg.Deployment.ID, seq, time.Now(), &def)
			if err != nil {
				return nil, err
			}
			return pq.NewUpdate(pq.DeploymentMutation(record)), nil
		}
		if err := spec.SetWorkloadVersion(desiredVersion); err != nil {
			return nil, err
		}
		id, err := q.NextDeploymentID(ctx)
		if err != nil {
			return nil, err
		}
		record = pq.DeploymentCreateRecord(ctx, id, seq, time.Now(), &apigen.Deployment{Scheduling: apigen.DedicatedScheduling(true, nodeID), SpaceID: internaldeploy.SpaceID, Name: internaldeploy.NetproxyName, Spec: *spec})
		return pq.NewUpdate(pq.DeploymentMutation(record)), nil
	})
	return erru.Must(record, err)
}
