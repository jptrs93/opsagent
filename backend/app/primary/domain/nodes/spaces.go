package nodes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/ptru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

const DefaultSpaceID int32 = 1

func NormalizedUserSpaceID(spaceID int32) int32 {
	if spaceID <= 0 {
		return DefaultSpaceID
	}
	return spaceID
}

func InvalidateNodeRuntimeState(store *state.Service, nodeID int32) (int64, error) {
	if nodeID <= 0 {
		return 0, fmt.Errorf("deployment node ID must be positive")
	}
	ctx := context.Background()
	update := state.Update{}
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		if _, err := q.GetNodeRowByID(ctx, int64(nodeID)); err != nil {
			return nil, err
		}
		instances, err := q.ListLatestScheduledInstanceEvents(ctx)
		if err != nil {
			return nil, err
		}
		for _, event := range instances {
			inst := event.Value
			if inst.NodeID != nodeID {
				continue
			}
			cfg, err := q.GetDeploymentEventByVersion(ctx, pq.GetDeploymentEventByVersionParams{DeploymentID: int64(inst.DeploymentID), Version: int64(inst.DeploymentVersion)})
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
			tombstone := &apigen.ScheduledInstanceStatus{ScheduledInstanceID: inst.ID, DeploymentID: inst.DeploymentID, UpdatedAt: previous.UpdatedAt}
			tombstone.BumpUpdatedAt()
			if err := q.InsertScheduledInstanceStatus(ctx, seq, tombstone); err != nil {
				return nil, err
			}
			update.InstanceStatuses = append(update.InstanceStatuses, tombstone)
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
		if err := q.InsertNodeStatus(ctx, seq, tombstone); err != nil {
			return nil, err
		}
		update.NodeStatuses = append(update.NodeStatuses, tombstone)
		return &update, nil
	})
	if err != nil {
		return 0, fmt.Errorf("invalidate runtime state for node %d: %w", nodeID, err)
	}
	return int64(len(update.InstanceStatuses)), nil
}

func ListSpaces(q *pq.Queries) []*apigen.Space {
	rows := erru.Must(q.ListSpaces(context.Background()))
	out := make([]*apigen.Space, 0, len(rows))
	for _, row := range rows {
		out = append(out, ptru.To(row))
	}
	return out
}

func CreateSpace(store *state.Service, name string) (*apigen.Space, error) {
	ctx := context.Background()
	var space *apigen.Space
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		row, err := q.CreateSpace(ctx, name)
		if err != nil {
			return nil, err
		}
		space = ptru.To(row)
		nodes, err := updateAllNodeAllowedSpaces(ctx, q, seq, func(spaces []int32) []int32 { return append(spaces, space.ID) })
		return &state.Update{Spaces: []*apigen.Space{space}, NodeEvents: nodes}, err
	})
	return space, err
}

func UpdateSpace(store *state.Service, id int32, name string) (*apigen.Space, error) {
	ctx := context.Background()
	var space *apigen.Space
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		row, err := q.UpdateSpace(ctx, pq.UpdateSpaceParams{Name: name, ID: int64(id)})
		if err != nil {
			return nil, err
		}
		space = ptru.To(row)
		return &state.Update{Spaces: []*apigen.Space{space}}, nil
	})
	return space, err
}

func DeleteSpace(store *state.Service, id int32) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		if err := q.DeleteSpace(ctx, int64(id)); err != nil {
			return nil, err
		}
		nodes, err := updateAllNodeAllowedSpaces(ctx, q, seq, func(spaces []int32) []int32 {
			out := spaces[:0:0]
			for _, space := range spaces {
				if space != id {
					out = append(out, space)
				}
			}
			return out
		})
		return &state.Update{Spaces: []*apigen.Space{{ID: id, Deleted: true}}, NodeEvents: nodes}, err
	})
}

func CountDeploymentsForSpace(q *pq.Queries, id int32) (int64, error) {
	deployments, err := q.ListActiveDeployments(context.Background())
	if err != nil {
		return 0, err
	}
	var count int64
	for _, cfg := range deployments {
		if cfg.Value.SpaceID == id {
			count++
		}
	}
	return count, nil
}
