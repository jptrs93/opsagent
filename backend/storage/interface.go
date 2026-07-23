package storage

import "github.com/jptrs93/opsagent/backend/apigen"

type DeploymentPredicate func(apigen.DeploymentConfig) bool

func DeploymentKeyMatches(cfg apigen.DeploymentConfig, nodeID int32, identity apigen.DeploymentIdentity) bool {
	return cfg.NodeID == nodeID &&
		cfg.Identity.SpaceID == identity.SpaceID &&
		cfg.Identity.Name == identity.Name
}

type OperatorStore interface {
	// MustWriteDeploymentStatus applies the mutator callback to the current DeploymentStatus version. The callback should
	// return true to persist the change or false to discard - if for example it determines it update is already superseded.
	MustWriteDeploymentStatus(int32, func(s *apigen.DeploymentStatus) bool)
	MustFetchSnapshotAndSubscribe(predicate DeploymentPredicate) ([]apigen.DeploymentWithStatus, chan apigen.DeploymentWithStatus, func())
}
