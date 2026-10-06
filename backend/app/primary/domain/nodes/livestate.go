package nodes

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

type LiveState struct {
	Scheduled   map[uint64]*apigen.ScheduledInstanceState
	Deployments map[uint64]*apigen.DeploymentRecord
	Nodes       map[uint64]*Node
}

func ReadLiveState(ctx context.Context, q *pq.Queries) (LiveState, error) {
	live := LiveState{
		Scheduled:   map[uint64]*apigen.ScheduledInstanceState{},
		Deployments: map[uint64]*apigen.DeploymentRecord{},
		Nodes:       map[uint64]*Node{},
	}
	deployments, err := q.ListActiveDeployments(ctx)
	if err != nil {
		return LiveState{}, err
	}
	for _, record := range deployments {
		live.Deployments[record.Deployment.ID] = record
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
