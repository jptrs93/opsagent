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
	return &apigen.Deployment{
		ID:           int32(e.DeploymentID),
		Version:      int32(e.Version),
		SpecVersion:  int32(e.SpecVersion),
		SpaceVersion: int32(e.SpaceAssignmentVersion),
		NameVersion:  int32(e.NameVersion),
		Author:       int32(e.Author),
		EventType:    apigen.DeploymentEventType(e.EventType),
		CreatedTime:  time.UnixMilli(e.CreatedTime),
		EventTime:    time.UnixMilli(e.EventTime),
		Def:          *erru.Must(apigen.DecodeDeploymentDef(e.Value)),
	}
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
