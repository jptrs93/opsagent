package statetest

import (
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

// MustCreateDeploymentForNode is a test seeding convenience: it locks, panics
// on a duplicate active identity, and creates the deployment.
func MustCreateDeploymentForNode(s *state.Service, ctx apigen.Context, spaceID int32, name string, nodeID int32, spec *apigen.DeploymentSpec) *apigen.Deployment {
	defer s.GlobalLock()()
	for _, cfg := range s.LiveState().Deployments {
		if storage.DeploymentKeyMatches(*cfg, nodeID, spaceID, name) && !cfg.Deleted {
			panic(fmt.Sprintf("deployment node=%d space=%d name=%q already exists", nodeID, spaceID, name))
		}
	}
	return s.CreateDeploymentLocked(ctx, &apigen.Deployment{
		NodeID:  nodeID,
		SpaceID: spaceID,
		Name:    name,
		Spec:    *spec,
	})
}

func UpdateDeployment(s *state.Service, ctx apigen.Context, deploymentID int32, update state.DeploymentUpdate) *apigen.Deployment {
	defer s.GlobalLock()()
	return s.UpdateDeploymentLocked(ctx, deploymentID, update)
}

func DeleteDeployment(s *state.Service, ctx apigen.Context, deploymentID int32) *apigen.Deployment {
	defer s.GlobalLock()()
	return s.DeleteDeploymentLocked(ctx, deploymentID)
}
