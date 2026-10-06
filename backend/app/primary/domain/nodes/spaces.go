package nodes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/ptru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

const DefaultSpaceID uint64 = 1

func NormalizedUserSpaceID(spaceID uint64) uint64 {
	if spaceID == 0 {
		return DefaultSpaceID
	}
	return spaceID
}

func InvalidateNodeRuntimeState(store *state.Service, nodeID uint64) (int64, error) {
	if nodeID == 0 {
		return 0, fmt.Errorf("deployment node ID must be positive")
	}
	ctx := context.Background()
	var invalidated int64
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if _, err := q.GetNodeRowByID(ctx, nodeID); err != nil {
			return nil, err
		}
		now := time.Now().UnixMilli()
		update := &state.WriteUpdate{}
		instances, err := q.ListRetainedScheduledInstances(ctx)
		if err != nil {
			return nil, err
		}
		for _, event := range instances {
			inst := event.Value
			if inst.NodeID != nodeID {
				continue
			}
			cfg, err := q.GetDeploymentVersion(ctx, inst.Deployment.DeploymentID, inst.Deployment.Version)
			if err != nil {
				return nil, err
			}
			if internaldeploy.IsSelfConfig(cfg) {
				continue
			}
			previous, err := q.GetLatestScheduledInstanceStatus(ctx, inst.ID)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			tombstone := &apigen.ScheduledInstanceStatus{ScheduledInstanceID: inst.ID, UpdatedAt: previous.UpdatedAt}
			tombstone.BumpUpdatedAt()
			pq.AppendMutations(update, pq.ScheduledInstanceStatusMutation(seq, now, tombstone))
			invalidated++
		}
		previous, err := q.GetLatestNodeStatus(ctx, nodeID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		tombstone := &apigen.NodeStatus{NodeID: nodeID}
		if previous != nil {
			tombstone.UpdatedAt = previous.UpdatedAt
		}
		tombstone.BumpUpdatedAt()
		pq.AppendMutations(update, pq.NodeStatusMutation(seq, now, tombstone))
		return update, nil
	})
	if err != nil {
		return 0, fmt.Errorf("invalidate runtime state for node %d: %w", nodeID, err)
	}
	return invalidated, nil
}

func ListSpaces(q *pq.Queries) []*apigen.Space {
	rows := erru.Must(q.ListSpaces(context.Background()))
	out := make([]*apigen.Space, 0, len(rows))
	for _, row := range rows {
		out = append(out, ptru.To(row))
	}
	return out
}

func spaceMeta(seq, now int64, author int64) pq.EventMeta {
	return pq.EventMeta{GlobalSeq: seq, EventTime: now, Author: author, EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE}
}

func CreateSpace(store *state.Service, name string, author int64) (*apigen.Space, error) {
	ctx := context.Background()
	var space *apigen.Space
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_SPACE)
		if err != nil {
			return nil, err
		}
		meta := spaceMeta(seq, time.Now().UnixMilli(), author)
		meta.EventType = apigen.AuthzVerb_AUTHZ_VERB_CREATE
		space = &apigen.Space{ID: id, Name: name}
		nodes, err := updateAllNodeAllowedSpaces(ctx, q, seq, meta.EventTime, func(spaces []uint64) []uint64 { return append(spaces, space.ID) })
		if err != nil {
			return nil, err
		}
		update := pq.NewUpdate(pq.SpaceMutation(meta, *space))
		pq.AppendMutations(update, nodes...)
		return update, nil
	})
	return space, err
}

func UpdateSpace(store *state.Service, id uint64, name string, author int64) (*apigen.Space, error) {
	ctx := context.Background()
	var space *apigen.Space
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if _, err := q.GetSpace(ctx, id); err != nil {
			return nil, err
		}
		space = &apigen.Space{ID: id, Name: name}
		return pq.NewUpdate(pq.SpaceMutation(spaceMeta(seq, time.Now().UnixMilli(), author), *space)), nil
	})
	return space, err
}

func DeleteSpace(store *state.Service, id uint64, author int64) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if _, err := q.GetSpace(ctx, id); err != nil {
			return nil, err
		}
		meta := spaceMeta(seq, time.Now().UnixMilli(), author)
		nodes, err := updateAllNodeAllowedSpaces(ctx, q, seq, meta.EventTime, func(spaces []uint64) []uint64 {
			out := spaces[:0:0]
			for _, space := range spaces {
				if space != id {
					out = append(out, space)
				}
			}
			return out
		})
		if err != nil {
			return nil, err
		}
		update := pq.NewUpdate(pq.DeleteMutation(meta, apigen.CoreEntityType_CORE_ENTITY_SPACE, id))
		pq.AppendMutations(update, nodes...)
		return update, nil
	})
}

func CountDeploymentsForSpace(q *pq.Queries, id uint64) (int64, error) {
	deployments, err := q.ListActiveDeployments(context.Background())
	if err != nil {
		return 0, err
	}
	var count int64
	for _, cfg := range deployments {
		if cfg.Deployment.SpaceID == id {
			count++
		}
	}
	return count, nil
}
