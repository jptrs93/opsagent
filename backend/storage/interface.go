package storage

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type OperatorStore interface {
	// MustWriteDeploymentStatus applies the mutator callback to the current DeploymentStatus version. The callback should
	// return true to persist the change or false to discard - if for example it determines it update is already superseded.
	MustWriteDeploymentStatus(context.Context, int32, func(s *apigen.DeploymentStatus) bool)
	MustFetchSnapshotAndSubscribe(ctx context.Context, machine string) ([]apigen.DeploymentWithStatus, chan apigen.DeploymentWithStatus)
}
