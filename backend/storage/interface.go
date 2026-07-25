package storage

import "github.com/jptrs93/opsagent/backend/apigen"

type DeploymentPredicate func(apigen.DeploymentConfig) bool

type ScheduledInstancePredicate func(apigen.ScheduledInstanceState) bool

// DeploymentConfigVersion is an optimistic assertion about the current
// version of a deployment config.
type DeploymentConfigVersion struct {
	ID      int32
	Version int32
}

func DeploymentKeyMatches(cfg apigen.DeploymentConfig, nodeID int32, identity apigen.DeploymentIdentity) bool {
	return cfg.NodeID == nodeID &&
		cfg.Identity.SpaceID == identity.SpaceID &&
		cfg.Identity.Name == identity.Name
}

// OperatorStore is the runtime store used by preparers and runners. Status is
// keyed by scheduled instance id.
type OperatorStore interface {
	MustWriteScheduledInstanceStatus(instanceID int32, f func(s *apigen.ScheduledInstanceStatus) bool)
	MustFetchScheduledSnapshotAndSubscribe(predicate ScheduledInstancePredicate) ([]apigen.ScheduledInstanceState, chan apigen.ScheduledInstanceState, func())
}
