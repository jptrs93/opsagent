package nodes

import (
	"context"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

type LiveState struct {
	Scheduled   map[int32]*apigen.ScheduledInstanceState
	Deployments map[int32]*apigen.DeploymentEvent
	Nodes       map[int32]*Node
}

func MustReadLiveState(q *pq.Queries) LiveState {
	return erru.Must(ReadLiveState(context.Background(), q))
}

func ReadLiveState(ctx context.Context, q *pq.Queries) (LiveState, error) {
	live := LiveState{
		Scheduled:   map[int32]*apigen.ScheduledInstanceState{},
		Deployments: map[int32]*apigen.DeploymentEvent{},
		Nodes:       map[int32]*Node{},
	}
	deployments, err := q.ListActiveDeployments(ctx)
	if err != nil {
		return LiveState{}, err
	}
	for _, event := range deployments {
		live.Deployments[event.DeploymentID] = event
	}
	nodes, err := q.ListNodeRows(ctx, pq.MemberNodeStatuses)
	if err != nil {
		return LiveState{}, err
	}
	for _, row := range nodes {
		node := nodeRowToNode(row)
		live.Nodes[node.ID] = node
	}
	states, err := q.ListLiveScheduledInstanceStates(ctx)
	if err != nil {
		return LiveState{}, err
	}
	for i := range states {
		live.Scheduled[states[i].Instance.ID] = &states[i]
	}
	return live, nil
}
