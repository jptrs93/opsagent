package state

import (
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func configRowToProto(r pq.DeploymentRow) *apigen.DeploymentConfig {
	spec := mustDecodeDeploymentSpec(r.SpecBlob, r.DeploymentID, r.Version)
	return &apigen.DeploymentConfig{
		ID:           int32(r.DeploymentID),
		NodeID:       int32(r.NodeID),
		SpaceID:      int32(r.SpaceID),
		SpaceVersion: int32(r.SpaceVersion),
		Name:         r.Name,
		CreatedAt:    millisToTime(r.CreatedAt),
		Version:      int32(r.Version),
		UpdatedAt:    time.UnixMilli(r.UpdatedAt),
		Author:       int32(r.Author),
		Spec:         deploymentSpecValue(spec),
		Deleted:      r.DeletedAt != 0,
	}
}

// configVersionRowToProto assembles a pinned or historical version row into a
// full config proto. Identity-level fields (node, space, name, creation time,
// tombstone state) come from base — the deployment's current cached config —
// since the version rows carry only the immutable spec. base may be nil when
// the identity is not cached; identity fields are then zero-valued.
func configVersionRowToProto(v pq.DeploymentVersion, base *apigen.DeploymentConfig) *apigen.DeploymentConfig {
	spec := mustDecodeDeploymentSpec(v.SpecBlob, v.DeploymentID, v.Version)
	cfg := &apigen.DeploymentConfig{
		ID:        int32(v.DeploymentID),
		Version:   int32(v.Version),
		UpdatedAt: time.UnixMilli(v.CreatedAt),
		Author:    int32(v.Author),
		Spec:      deploymentSpecValue(spec),
	}
	if base != nil {
		cfg.NodeID = base.NodeID
		cfg.SpaceID = base.SpaceID
		cfg.SpaceVersion = base.SpaceVersion
		cfg.Name = base.Name
		cfg.CreatedAt = base.CreatedAt
		cfg.Deleted = base.Deleted
	}
	return cfg
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
