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
	// are attempted with an unchanged StatusSeqNo.
	MustWriteDeploymentStatus(context.Context, int32, func(s *apigen.DeploymentStatus) bool)
	MustFetchSnapshotAndSubscribe(ctx context.Context, machine string) ([]apigen.DeploymentWithStatus, chan apigen.DeploymentWithStatus)
}

type SecondaryStore interface {
	OperatorStore
	MustWriteDeploymentConfig(ctx context.Context, cfg *apigen.DeploymentConfig)
	FetchDeploymentStatus(id int32) *apigen.DeploymentStatus
	FetchDeploymentStatusHistorySince(deploymentID int32, sinceSeqNo int32) []*apigen.DeploymentStatus
	SubscribeDeploymentUpdates(machine string) (chan apigen.DeploymentWithStatus, func())
}

type PrimaryLocalStore interface {
	OperatorStore

	FetchDeploymentStatus(id int32) *apigen.DeploymentStatus

	MustWriteReplicatedDeploymentStatus(ctx context.Context, status *apigen.DeploymentStatus)

	MustFetchDeploymentHistory(id int32) []*apigen.DeploymentConfig
	MustFetchDeploymentStatusHistory(id int32) []*apigen.DeploymentStatus
	MustSetDeploymentDesiredState(ctx apigen.Context, deploymentID int32, desired apigen.DesiredState)

	MustUpdateDeploymentSpec(ctx apigen.Context, deploymentID int32, spec *apigen.DeploymentSpec)
	MustCreateDeployment(ctx apigen.Context, cid *apigen.DeploymentIdentifier, spec *apigen.DeploymentSpec) *apigen.DeploymentConfig
	ListActiveDeploymentConfigs() []*apigen.DeploymentConfig
}
