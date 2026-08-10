package state

import (
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func configRowToProto(r pq.ListAllDeploymentConfigsRow) *apigen.DeploymentConfig {
	spec := mustDecodeDeploymentSpec(r.SpecBlob, r.DeploymentID, r.Version)
	return &apigen.DeploymentConfig{
		ID:     int32(r.DeploymentID),
		NodeID: int32(r.NodeID),
		Identity: apigen.DeploymentIdentity{
			SpaceID: int32(r.SpaceID),
			Name:    r.Name,
		},
		CreatedAt: millisToTime(r.CreatedAt),
		Version:   int32(r.Version),
		UpdatedAt: time.UnixMilli(r.UpdatedAt),
		UpdatedBy: int32(r.UpdatedBy),
		Spec:      deploymentSpecValue(spec),
		Deleted:   r.Deleted != 0,
	}
}

func getConfigRowToProto(r pq.GetDeploymentConfigRow) *apigen.DeploymentConfig {
	return configRowToProto(pq.ListAllDeploymentConfigsRow{
		DeploymentID: r.DeploymentID,
		NodeID:       r.NodeID,
		SpaceID:      r.SpaceID,
		Name:         r.Name,
		CreatedAt:    r.CreatedAt,
		Version:      r.Version,
		UpdatedAt:    r.UpdatedAt,
		UpdatedBy:    r.UpdatedBy,
		SpecBlob:     r.SpecBlob,
		Deleted:      r.Deleted,
	})
}

func upsertParamsToProto(p pq.UpsertDeploymentConfigParams) *apigen.DeploymentConfig {
	spec := mustDecodeDeploymentSpec(p.SpecBlob, p.DeploymentID, p.Version)
	return &apigen.DeploymentConfig{
		ID:     int32(p.DeploymentID),
		NodeID: int32(p.NodeID),
		Identity: apigen.DeploymentIdentity{
			SpaceID: int32(p.SpaceID),
			Name:    p.Name,
		},
		CreatedAt: millisToTime(p.CreatedAt),
		Version:   int32(p.Version),
		UpdatedAt: time.UnixMilli(p.UpdatedAt),
		UpdatedBy: int32(p.UpdatedBy),
		Spec:      deploymentSpecValue(spec),
		Deleted:   p.Deleted != 0,
	}
}

func configHistoryRowToFullProto(r pq.DeploymentConfigHistory, identity apigen.DeploymentIdentity, createdAt time.Time) *apigen.DeploymentConfig {
	spec := mustDecodeDeploymentSpec(r.SpecBlob, r.DeploymentID, r.Version)
	identity.SpaceID = int32(r.SpaceID)
	return &apigen.DeploymentConfig{
		ID:        int32(r.DeploymentID),
		NodeID:    int32(r.NodeID),
		Identity:  identity,
		CreatedAt: createdAt,
		Version:   int32(r.Version),
		UpdatedAt: time.UnixMilli(r.UpdatedAt),
		UpdatedBy: int32(r.UpdatedBy),
		Spec:      deploymentSpecValue(spec),
		Deleted:   r.Deleted != 0,
	}
}

func deploymentSpecValue(spec *apigen.DeploymentSpec) apigen.DeploymentSpec {
	if spec == nil {
		return apigen.DeploymentSpec{}
	}
	return *spec
}

func mustDecodeDeploymentSpec(blob []byte, deploymentID, version int64) *apigen.DeploymentSpec {
	spec, err := apigen.DecodeDeploymentSpec(blob)
	if err != nil {
		panic(fmt.Sprintf("decode deployment %d version %d spec: %v", deploymentID, version, err))
	}
	return spec
}
