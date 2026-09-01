package storage

import "github.com/jptrs93/opsagent/backend/apigen"

type DeploymentPredicate func(apigen.Deployment) bool

type ScheduledInstancePredicate func(apigen.ScheduledInstanceState) bool

// DeploymentSpecVersion is an optimistic assertion about the current
// spec version of a deployment.
type DeploymentSpecVersion struct {
	ID          int32
	SpecVersion int32
}

func DeploymentKeyMatches(cfg apigen.Deployment, nodeID, spaceID int32, name string) bool {
	return cfg.NodeID == nodeID &&
		cfg.SpaceID == spaceID &&
		cfg.Name == name
}

// OperatorStore is the runtime store used by preparers and runners. Status is
// keyed by scheduled instance id.
type OperatorStore interface {
	MustWriteScheduledInstanceStatus(instanceID int32, f func(s *apigen.ScheduledInstanceStatus) bool)
	MustFetchScheduledSnapshotAndSubscribe(predicate ScheduledInstancePredicate) ([]apigen.ScheduledInstanceState, chan apigen.ScheduledInstanceState, func())
}
