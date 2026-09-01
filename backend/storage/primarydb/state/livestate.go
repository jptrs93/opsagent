package state

import (
	"github.com/jptrs93/opsagent/backend/apigen"
)

type LiveState struct {
	Scheduled   map[int32]*apigen.ScheduledInstanceState
	Deployments map[int32]*apigen.Deployment
	Nodes       map[int32]*Node
}

func (s *Service) LiveState() LiveState {
	nodes := make(map[int32]*Node)
	for _, n := range s.listNodesLocked() {
		nodes[n.ID] = n
	}
	return LiveState{
		Scheduled:   s.Scheduled,
		Deployments: s.deploymentCache,
		Nodes:       nodes,
	}
}

func (s *Service) GlobalLock() func() {
	s.Mu.Lock()
	return s.Mu.Unlock
}

func (s *Service) InstanceStatusesForDeploymentLocked(deploymentID int32) []apigen.ScheduledInstanceStatus {
	states := s.instanceSnapshotWithLatestFinalLocked(func(st apigen.ScheduledInstanceState) bool {
		return st.Instance.DeploymentID == deploymentID
	})
	out := make([]apigen.ScheduledInstanceStatus, 0, len(states))
	for _, st := range states {
		out = append(out, st.Status)
	}
	return out
}
