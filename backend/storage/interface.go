package storage

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type OperatorStore interface {
	// MustWriteDeploymentStatus applies the mutator callback to the current
	// DeploymentStatus. The callback returns true to persist the change
	// (upsert + history insert) or false to skip it entirely — use false
	// when a guard like a superseded-version check fires, so no DB writes
	// are attempted with an unchanged UpdatedAt clock.
	MustWriteDeploymentStatus(context.Context, int32, func(s *apigen.DeploymentStatus) bool)
	MustFetchSnapshotAndSubscribe(ctx context.Context, machine string) ([]apigen.DeploymentWithStatus, chan apigen.DeploymentWithStatus)
}
