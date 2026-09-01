package state

import (
	"fmt"
	"reflect"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// deploymentEventToProto decodes an event's full Deployment snapshot. The
// version columns on the event are a materialisation of the same snapshot, so
// nothing is joined in.
func deploymentEventToProto(e pq.DeploymentEvent) *apigen.Deployment {
	cfg, err := apigen.DecodeDeployment(e.Value)
	if err != nil {
		panic(fmt.Sprintf("decode deployment %d event version %d: %v", e.DeploymentID, e.Version, err))
	}
	return cfg
}

// pinnedSpecEventToProto assembles a pinned or historical spec version from
// the event that introduced it. Identity-level fields (node, space, name,
// creation time, tombstone state) come from base — the deployment's current
// cached config — matching the pre-event-log behaviour where version rows
// carried only the spec. base may be nil when the identity is not cached; the
// event's own historical identity then stands.
func pinnedSpecEventToProto(e pq.DeploymentEvent, base *apigen.Deployment) *apigen.Deployment {
	cfg := deploymentEventToProto(e)
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

// deploymentSpecsEqual reports semantic spec equality. Encoded bytes cannot
// be compared directly: map fields (env vars) encode in Go map iteration
// order, so two encodes of equal specs can produce different bytes. Both
// sides are canonicalised through a decode and compared structurally.
func deploymentSpecsEqual(a, b *apigen.DeploymentSpec) bool {
	da, err := apigen.DecodeDeploymentSpec(a.Encode())
	if err != nil {
		panic(fmt.Sprintf("canonicalise deployment spec: %v", err))
	}
	db, err := apigen.DecodeDeploymentSpec(b.Encode())
	if err != nil {
		panic(fmt.Sprintf("canonicalise deployment spec: %v", err))
	}
	return reflect.DeepEqual(da, db)
}

func mustDecodeDeploymentSpec(blob []byte, deploymentID, version int64) *apigen.DeploymentSpec {
	spec, err := apigen.DecodeDeploymentSpec(blob)
	if err != nil {
		panic(fmt.Sprintf("decode deployment %d version %d spec: %v", deploymentID, version, err))
	}
	return spec
}
