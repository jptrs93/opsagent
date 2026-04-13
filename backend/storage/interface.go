package storage

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type OperatorStore interface {
	MustWriteDeploymentStatus(context.Context, apigen.DeploymentIdentifier, func(s *apigen.DeploymentStatus))
	MustFetchSnapshotAndSubscribe(ctx context.Context, machine string) ([]apigen.DeploymentWithStatus, chan apigen.DeploymentWithStatus)
}

type SecondaryLocalStore interface {
	MustFetchLocalSnapshot(ctx context.Context) []apigen.DeploymentWithStatus
	MustWriteLocalDeploymentConfig(ctx context.Context, s apigen.DeploymentConfig)
	MustWriteLocalDeploymentStatus(context.Context, *apigen.DeploymentStatus)
}

type PrimaryLocalStore interface {
	OperatorStore

	// deployment specific config history
	MustFetchDeploymentHistory(id apigen.DeploymentIdentifier) []*apigen.DeploymentConfig
	MustFetchDeploymentStatusHistory(id apigen.DeploymentIdentifier) []*apigen.DeploymentStatus
	MustSetDeploymentDesiredState(ctx apigen.Context, identifier apigen.DeploymentIdentifier, v2 apigen.DesiredState)

	// user defined config
	MustFetchUserConfigVersion() *apigen.UserConfigVersion
	PutDeploymentUserConfig(ctx apigen.Context, yamlContent string, parseFunc func(string) ([]*apigen.DeploymentConfig, error)) (*apigen.UserConfigVersion, error)
	FetchDeploymentUserConfigHistory() []*apigen.UserConfigVersion
}
