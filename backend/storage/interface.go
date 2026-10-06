package storage

import "github.com/jptrs93/opsagent/backend/apigen"

type DeploymentPredicate func(apigen.DeploymentRecord) bool

type ScheduledInstancePredicate func(apigen.ScheduledInstanceState) bool

// DeploymentSpecVersion is an optimistic assertion about the current
// spec version of a deployment.
func DeploymentKeyMatches(def apigen.Deployment, nodeID, spaceID uint64, name string) bool {
	return def.PlacementNodeID() == nodeID &&
		def.SpaceID == spaceID &&
		def.Name == name
}

// OperatorStore is the runtime store used by preparers and runners. Status is
// keyed by scheduled instance id.
type OperatorStore interface {
	MustWriteScheduledInstanceStatus(instanceID uint64, f func(s *apigen.ScheduledInstanceStatus) bool)
	MustFetchScheduledSnapshotAndSubscribe(predicate ScheduledInstancePredicate) ([]apigen.ScheduledInstanceState, chan []apigen.ScheduledInstanceState, func())
}
