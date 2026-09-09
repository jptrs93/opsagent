package pq

import (
	"context"
	"database/sql"
	"reflect"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
)

const (
	DeploymentEventCreate = int64(apigen.AuthzVerb_AUTHZ_VERB_CREATE)
	DeploymentEventUpdate = int64(apigen.AuthzVerb_AUTHZ_VERB_UPDATE)
	DeploymentEventDelete = int64(apigen.AuthzVerb_AUTHZ_VERB_DELETE)
)

func (q *Queries) InsertDeploymentEvent(ctx context.Context, event *apigen.DeploymentEvent) error {
	row := q.db.QueryRowContext(ctx, `WITH previous AS (
 SELECT spec_version, space_assignment_version, name_version
 FROM deployment_event_log WHERE deployment_id = ? ORDER BY version DESC LIMIT 1
 ) INSERT INTO deployment_event_log (
 global_seq, event_time, created_time, author, deployment_id, version,
 spec_version, space_assignment_version, name_version,
 spec_changed, space_assignment_changed, name_changed, value, event_type
 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?,
 ? > COALESCE((SELECT spec_version FROM previous), 0),
 ? > COALESCE((SELECT space_assignment_version FROM previous), 0),
 ? > COALESCE((SELECT name_version FROM previous), 0), ?, ?)
 RETURNING `+deploymentEventColumns,
		event.DeploymentID, event.Seq, event.EventTime.UnixMilli(), event.CreatedTime.UnixMilli(), event.Author,
		event.DeploymentID, event.Version, event.SpecVersion, event.SpaceVersion, event.NameVersion,
		event.SpecVersion, event.SpaceVersion, event.NameVersion, event.Value.Encode(), event.EventType)
	written, err := scanDeploymentEvent(row)
	if err != nil {
		return err
	}
	*event = *written
	return nil
}

func (q *Queries) WriteDeploymentCreate(ctx apigen.Context, deploymentID, seq int64, d *apigen.Deployment) (*apigen.DeploymentEvent, error) {
	now := time.Now()
	event := &apigen.DeploymentEvent{
		Seq:          seq,
		EventTime:    now,
		CreatedTime:  now,
		Author:       ctx.AttributionUserID(),
		DeploymentID: int32(deploymentID),
		Version:      1,
		SpecVersion:  1,
		SpaceVersion: 1,
		NameVersion:  1,
		Value:        *d,
		EventType:    apigen.EventType_EVENT_TYPE_CREATE,
	}
	if err := q.InsertDeploymentEvent(ctx, event); err != nil {
		return nil, err
	}
	return event, nil
}

func (q *Queries) WriteDeploymentUpdate(ctx apigen.Context, deploymentID, seq int64, d *apigen.Deployment) (*apigen.DeploymentEvent, error) {
	prev, err := q.GetLatestDeploymentEvent(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	if prev.Deleted() {
		return nil, sql.ErrNoRows
	}
	event := BuildDeploymentUpdateEvent(prev, d, ctx.AttributionUserID())
	event.Seq = seq
	if err := q.InsertDeploymentEvent(ctx, event); err != nil {
		return nil, err
	}
	return event, nil
}

func (q *Queries) WriteDeploymentDelete(ctx apigen.Context, deploymentID, seq int64) (*apigen.DeploymentEvent, error) {
	// caller is responsible for checking deployment exists and isn't deleted
	prev, err := q.GetLatestDeploymentEvent(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	event := &apigen.DeploymentEvent{
		Seq:          seq,
		EventTime:    time.UnixMilli(time.Now().UnixMilli()),
		CreatedTime:  prev.CreatedTime,
		Author:       ctx.AttributionUserID(),
		DeploymentID: prev.DeploymentID,
		Version:      prev.Version + 1,
		SpecVersion:  prev.SpecVersion,
		SpaceVersion: prev.SpaceVersion,
		NameVersion:  prev.NameVersion,
		Value:        prev.Value,
		EventType:    apigen.EventType_EVENT_TYPE_DELETE,
	}
	if err := q.InsertDeploymentEvent(ctx, event); err != nil {
		return nil, err
	}
	return event, nil
}

// BuildDeploymentUpdateEvent advances only the facets changed by an update.
// Composed rotation writers can use their already-checked predecessor row.
func BuildDeploymentUpdateEvent(prev *apigen.DeploymentEvent, updated *apigen.Deployment, author int32) *apigen.DeploymentEvent {
	prevDef := &prev.Value
	event := &apigen.DeploymentEvent{
		EventTime:    time.UnixMilli(time.Now().UnixMilli()),
		CreatedTime:  prev.CreatedTime,
		Author:       author,
		DeploymentID: prev.DeploymentID,
		Version:      prev.Version + 1,
		SpecVersion:  prev.SpecVersion,
		SpaceVersion: prev.SpaceVersion,
		NameVersion:  prev.NameVersion,
		Value:        *updated,
		EventType:    apigen.EventType_EVENT_TYPE_UPDATE,
	}
	if !DeploymentSpecsEqual(&updated.Spec, &prevDef.Spec) {
		event.SpecVersion++
	}
	if updated.SpaceID != prevDef.SpaceID {
		event.SpaceVersion++
	}
	if updated.Name != prevDef.Name {
		event.NameVersion++
	}
	return event
}

func DeploymentSpecsEqual(a, b *apigen.DeploymentSpec) bool {
	da := erru.Must(apigen.DecodeDeploymentSpec(a.Encode()))
	db := erru.Must(apigen.DecodeDeploymentSpec(b.Encode()))
	return reflect.DeepEqual(da, db)
}
