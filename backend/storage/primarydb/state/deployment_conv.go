package state

import (
	"fmt"
	"reflect"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func deploymentFromRow(e pq.DeploymentEvent) *apigen.Deployment {
	cfg := &apigen.Deployment{
		ID:           int32(e.DeploymentID),
		Version:      int32(e.Version),
		SpecVersion:  int32(e.SpecVersion),
		SpaceVersion: int32(e.SpaceAssignmentVersion),
		NameVersion:  int32(e.NameVersion),
		Author:       int32(e.Author),
		EventType:    apigen.DeploymentEventType(e.EventType),
		CreatedTime:  time.UnixMilli(e.CreatedTime),
		EventTime:    time.UnixMilli(e.EventTime),
		Def:          *mustDecodeDeploymentDef(e),
	}
	mirrorDeploymentDef(cfg)
	return cfg
}

func mirrorDeploymentDef(cfg *apigen.Deployment) {
	cfg.NodeID = cfg.Def.NodeID
	cfg.Spec = cfg.Def.Spec
	cfg.SpaceID = cfg.Def.SpaceID
	cfg.Name = cfg.Def.Name
	cfg.LegacyCreatedAt = cfg.CreatedTime.UnixMilli()
	cfg.LegacyUpdatedAt = cfg.EventTime.UnixMilli()
}

func mustDecodeDeploymentDef(e pq.DeploymentEvent) *apigen.DeploymentDef {
	def, err := apigen.DecodeDeploymentDef(e.Value)
	if err != nil {
		panic(fmt.Sprintf("decode deployment %d event version %d: %v", e.DeploymentID, e.Version, err))
	}
	return def
}

// pinnedSpecEventToProto assembles a pinned or historical spec version from
// the event that introduced it. Identity-level fields (node, space, name,
// creation time, tombstone state) come from base — the deployment's current
// cached config — matching the pre-event-log behaviour where version rows
// carried only the spec. base may be nil when the identity is not cached; the
// event's own historical identity then stands.
func pinnedSpecEventToProto(e pq.DeploymentEvent, base *apigen.Deployment) *apigen.Deployment {
	cfg := deploymentFromRow(e)
	if base != nil {
		cfg.Def.NodeID = base.Def.NodeID
		cfg.Def.SpaceID = base.Def.SpaceID
		cfg.Def.Name = base.Def.Name
		cfg.SpaceVersion = base.SpaceVersion
		cfg.CreatedTime = base.CreatedTime
		cfg.EventType = base.EventType
		mirrorDeploymentDef(cfg)
	}
	return cfg
}

func deploymentSpecsEqual(a, b *apigen.DeploymentSpec) bool {
	da := erru.Must(apigen.DecodeDeploymentSpec(a.Encode()))
	db := erru.Must(apigen.DecodeDeploymentSpec(b.Encode()))
	return reflect.DeepEqual(da, db)
}

func DeploymentSpecsEqual(a, b *apigen.DeploymentSpec) bool {
	return deploymentSpecsEqual(a, b)
}

func mustDecodeDeploymentSpec(blob []byte, deploymentID, version int64) *apigen.DeploymentSpec {
	spec, err := apigen.DecodeDeploymentSpec(blob)
	if err != nil {
		panic(fmt.Sprintf("decode deployment %d version %d spec: %v", deploymentID, version, err))
	}
	return spec
}
