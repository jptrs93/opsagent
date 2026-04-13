package storage

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type OperatorStore interface {
	MustWriteDeploymentStatus(context.Context, int32, func(s *apigen.DeploymentStatus))
	MustFetchSnapshotAndSubscribe(ctx context.Context, machine string) ([]apigen.DeploymentWithStatus, chan apigen.DeploymentWithStatus)
}

type SecondaryStore interface {
	OperatorStore
	MustWriteDeploymentConfig(ctx context.Context, cfg *apigen.DeploymentConfig)
	FetchDeploymentStatus(id int32) *apigen.DeploymentStatus
	SubscribeDeploymentUpdates(machine string) (chan apigen.DeploymentWithStatus, func())
}

type PrimaryLocalStore interface {
	OperatorStore

	FetchDeploymentStatus(id int32) *apigen.DeploymentStatus

	MustFetchDeploymentHistory(id int32) []*apigen.DeploymentConfig
	MustFetchDeploymentStatusHistory(id int32) []*apigen.DeploymentStatus
	MustSetDeploymentDesiredState(ctx apigen.Context, deploymentID int32, desired apigen.DesiredState)

	MustFetchUserConfigVersion() *apigen.UserConfigVersion
	PutDeploymentUserConfig(ctx apigen.Context, yamlContent string, parseFunc func(string) ([]*apigen.DeploymentConfig, error)) (*apigen.UserConfigVersion, error)
	FetchDeploymentUserConfigHistory() []*apigen.UserConfigVersion
}
