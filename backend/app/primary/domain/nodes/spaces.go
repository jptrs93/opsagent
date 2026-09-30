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
	var invalidated int64
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if _, err := q.GetNodeRowByID(ctx, int64(nodeID)); err != nil {
			return nil, err
		}
		now := time.Now().UnixMilli()
		update := &state.WriteUpdate{}
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
			if err := q.InsertScheduledInstanceStatus(ctx, seq, now, tombstone); err != nil {
				return nil, err
			}
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
		if err := q.InsertNodeStatus(ctx, seq, now, tombstone); err != nil {
			return nil, err
		}
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

func spaceEvent(seq, now int64, author int32, eventType apigen.AuthzVerb, id int64, name string) pq.SpaceEventParams {
	return pq.SpaceEventParams{
		EventMeta: pq.EventMeta{GlobalSeq: seq, EventTime: now, Author: int64(author), EventType: eventType},
		SpaceID:   id, Name: name,
	}
}

func CreateSpace(store *state.Service, name string, author int32) (*apigen.Space, error) {
	ctx := context.Background()
	var space *apigen.Space
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		id, err := q.NextSpaceID(ctx)
		if err != nil {
			return nil, err
		}
		event := spaceEvent(seq, time.Now().UnixMilli(), author, apigen.AuthzVerb_AUTHZ_VERB_CREATE, id, name)
		if err := q.InsertSpaceEvent(ctx, event); err != nil {
			return nil, err
		}
		space = &apigen.Space{ID: int32(id), Name: name}
		nodes, err := updateAllNodeAllowedSpaces(ctx, q, seq, event.EventTime, func(spaces []int32) []int32 { return append(spaces, space.ID) })
		if err != nil {
			return nil, err
		}
		update := pq.NewUpdate(pq.SpaceMutation(event.EventMeta, *space))
		pq.AppendMutations(update, nodes...)
		return update, nil
	})
	return space, err
}

func UpdateSpace(store *state.Service, id int32, name string, author int32) (*apigen.Space, error) {
	ctx := context.Background()
	var space *apigen.Space
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if _, err := q.GetSpace(ctx, int64(id)); err != nil {
			return nil, err
		}
		event := spaceEvent(seq, time.Now().UnixMilli(), author, apigen.AuthzVerb_AUTHZ_VERB_UPDATE, int64(id), name)
		if err := q.InsertSpaceEvent(ctx, event); err != nil {
			return nil, err
		}
		space = &apigen.Space{ID: id, Name: name}
		return pq.NewUpdate(pq.SpaceMutation(event.EventMeta, *space)), nil
	})
	return space, err
}

func DeleteSpace(store *state.Service, id int32, author int32) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		current, err := q.GetSpace(ctx, int64(id))
		if err != nil {
			return nil, err
		}
		event := spaceEvent(seq, time.Now().UnixMilli(), author, apigen.AuthzVerb_AUTHZ_VERB_DELETE, int64(id), current.Name)
		if err := q.InsertSpaceEvent(ctx, event); err != nil {
			return nil, err
		}
		nodes, err := updateAllNodeAllowedSpaces(ctx, q, seq, event.EventTime, func(spaces []int32) []int32 {
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
		update := pq.NewUpdate(pq.SpaceMutation(event.EventMeta, apigen.Space{ID: id, Name: current.Name}))
		pq.AppendMutations(update, nodes...)
		return update, nil
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
