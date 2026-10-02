package pq

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
)

// ErrDeploymentUnchanged reports an update that changes no facet of the
// deployment. Nothing is written.
var ErrDeploymentUnchanged = errors.New("deployment unchanged")

const deploymentVersionColumns = `v.deployment_id, v.version, v.spec_version, v.created_time, v.value, v.seq, v.event_time, v.author`

const liveDeploymentVersionsFrom = `FROM deployments d JOIN deployment_versions v ON v.deployment_id = d.id AND v.version = d.version`

func scanDeploymentEvent(row scanner) (*apigen.DeploymentEvent, error) {
	var event apigen.DeploymentEvent
	var eventTime, createdTime, author int64
	var value []byte
	if err := row.Scan(&event.DeploymentID, &event.Version, &event.SpecVersion, &createdTime, &value, &event.Seq, &eventTime, &author); err != nil {
		return nil, err
	}
	def, err := apigen.DecodeDeployment(value)
	if err != nil {
		return nil, err
	}
	event.Author = int32(author)
	event.EventTime, event.CreatedTime = time.UnixMilli(eventTime), time.UnixMilli(createdTime)
	event.EventType = apigen.EventType_EVENT_TYPE_UPDATE
	if event.Version == 1 {
		event.EventType = apigen.EventType_EVENT_TYPE_CREATE
	}
	event.Value = *def
	event.Value.ID = event.DeploymentID
	return &event, nil
}

func (q *Queries) NextDeploymentID(ctx context.Context) (int64, error) {
	return q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT)
}

type GetDeploymentEventByVersionParams struct {
	DeploymentID int64
	Version      int64
}

// GetDeploymentEventByVersion returns a retained version: the current one of
// a live deployment or one a retained scheduled instance pins.
func (q *Queries) GetDeploymentEventByVersion(ctx context.Context, arg GetDeploymentEventByVersionParams) (*apigen.DeploymentEvent, error) {
	return scanDeploymentEvent(q.db.QueryRowContext(ctx, `SELECT `+deploymentVersionColumns+` FROM deployment_versions v WHERE v.deployment_id = ? AND v.version = ?`, arg.DeploymentID, arg.Version))
}

// GetLatestDeploymentEvent returns the current version of a live deployment,
// or sql.ErrNoRows when the deployment is deleted or unknown.
func (q *Queries) GetLatestDeploymentEvent(ctx context.Context, deploymentID int64) (*apigen.DeploymentEvent, error) {
	return scanDeploymentEvent(q.db.QueryRowContext(ctx, `SELECT `+deploymentVersionColumns+` `+liveDeploymentVersionsFrom+` WHERE d.id = ?`, deploymentID))
}

// ListActiveDeployments returns the current version of every live deployment.
func (q *Queries) ListActiveDeployments(ctx context.Context) ([]*apigen.DeploymentEvent, error) {
	return q.queryDeploymentEvents(ctx, `SELECT `+deploymentVersionColumns+` `+liveDeploymentVersionsFrom+` ORDER BY d.id`)
}

// ListRetainedDeploymentVersions returns every retained version row in
// (deployment, version) order.
func (q *Queries) ListRetainedDeploymentVersions(ctx context.Context) ([]*apigen.DeploymentEvent, error) {
	return q.queryDeploymentEvents(ctx, `SELECT `+deploymentVersionColumns+` FROM deployment_versions v ORDER BY v.deployment_id, v.version`)
}

func (q *Queries) queryDeploymentEvents(ctx context.Context, query string, args ...any) ([]*apigen.DeploymentEvent, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []*apigen.DeploymentEvent
	for rows.Next() {
		event, err := scanDeploymentEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// deploymentVersionFacts numbers a deployment's logged documents the way the
// reducer does: every create or update is the next version, the spec
// version advances when the spec changed, and the creation time is the first
// row's. The log holds documents only; a history is derived on read.
type deploymentVersionFacts struct {
	version, specVersion int32
	createdTime          time.Time
	previous             *apigen.Deployment
}

func (f *deploymentVersionFacts) next(d *apigen.Deployment, eventTime int64) {
	if f.previous == nil {
		f.version, f.specVersion, f.createdTime = 1, 1, time.UnixMilli(eventTime)
	} else {
		f.version++
		if !DeploymentSpecsEqual(&d.Spec, &f.previous.Spec) {
			f.specVersion++
		}
	}
	f.previous = d
}

func (f *deploymentVersionFacts) event(id, seq, eventTime int64, actor int32, eventType apigen.EventType, d *apigen.Deployment) *apigen.DeploymentEvent {
	value := *d
	value.ID = int32(id)
	return &apigen.DeploymentEvent{
		DeploymentID: int32(id), Version: f.version, SpecVersion: f.specVersion, Seq: seq, Author: actor, EventType: eventType,
		CreatedTime: f.createdTime, EventTime: time.UnixMilli(eventTime), Value: value,
	}
}

// deploymentLogEvents is a deployment's history, oldest first, read from the
// write log; a delete carries the document it removed under the facts of
// the last version.
func (q *Queries) deploymentLogEvents(ctx context.Context, deploymentID int64) ([]*apigen.DeploymentEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT m.seq, e.time, e.actor, m.op, m.payload FROM write_event_mutations m JOIN write_events e ON e.seq = m.seq
WHERE m.entity_type = ? AND m.entity_id = ? ORDER BY m.seq, m.idx`, int64(apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT), deploymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []*apigen.DeploymentEvent
	var facts deploymentVersionFacts
	for rows.Next() {
		var seq, eventTime, op int64
		var actor int32
		var payload []byte
		if err := rows.Scan(&seq, &eventTime, &actor, &op, &payload); err != nil {
			return nil, err
		}
		if apigen.AuthzVerb(op) == apigen.AuthzVerb_AUTHZ_VERB_DELETE {
			if facts.previous != nil {
				events = append(events, facts.event(deploymentID, seq, eventTime, actor, apigen.EventType_EVENT_TYPE_DELETE, facts.previous))
			}
			continue
		}
		entity, err := apigen.DecodeCoreEntity(payload)
		if err != nil || entity.Deployment == nil {
			return nil, fmt.Errorf("deployment %d at seq %d: %v", deploymentID, seq, err)
		}
		facts.next(entity.Deployment, eventTime)
		events = append(events, facts.event(deploymentID, seq, eventTime, actor, apigen.EventType(op), entity.Deployment))
	}
	return events, rows.Err()
}

// deploymentVersionFromLog is one version of a deployment read from the write
// log, for a version the tables no longer retain.
func (q *Queries) deploymentVersionFromLog(ctx context.Context, deploymentID, version int64) (*apigen.DeploymentEvent, error) {
	events, err := q.deploymentLogEvents(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	for _, event := range events {
		if int64(event.Version) == version && event.EventType != apigen.EventType_EVENT_TYPE_DELETE {
			return event, nil
		}
	}
	return nil, sql.ErrNoRows
}

// ListDeploymentEvents is a deployment's history, oldest first.
func (q *Queries) ListDeploymentEvents(ctx context.Context, deploymentID int64) ([]*apigen.DeploymentEvent, error) {
	return q.deploymentLogEvents(ctx, deploymentID)
}

// ListDeletedDeploymentEvents returns one tombstone per deleted deployment,
// newest deletion first, read from the write log.
func (q *Queries) ListDeletedDeploymentEvents(ctx context.Context) ([]*apigen.DeploymentEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT DISTINCT entity_id FROM write_event_mutations WHERE entity_type = ? AND op = ? ORDER BY entity_id`,
		int64(apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT), int64(apigen.AuthzVerb_AUTHZ_VERB_DELETE))
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var events []*apigen.DeploymentEvent
	for _, id := range ids {
		history, err := q.deploymentLogEvents(ctx, id)
		if err != nil {
			return nil, err
		}
		if last := history[len(history)-1]; len(history) > 0 && last.EventType == apigen.EventType_EVENT_TYPE_DELETE {
			events = append(events, last)
		}
	}
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].EventTime.Equal(events[j].EventTime) {
			return events[i].EventTime.After(events[j].EventTime)
		}
		return events[i].DeploymentID > events[j].DeploymentID
	})
	return events, nil
}

// DeploymentCreateEvent is the first version of a new deployment. Nothing is
// written: the caller returns its mutation from Commit.
func DeploymentCreateEvent(ctx apigen.Context, deploymentID, seq int64, now time.Time, d *apigen.Deployment) *apigen.DeploymentEvent {
	at := time.UnixMilli(now.UnixMilli())
	event := &apigen.DeploymentEvent{
		Seq: seq, EventTime: at, CreatedTime: at, Author: ctx.AttributionUserID(),
		DeploymentID: int32(deploymentID), Version: 1, SpecVersion: 1, Value: *d, EventType: apigen.EventType_EVENT_TYPE_CREATE,
	}
	event.Value.ID = event.DeploymentID
	return event
}

// DeploymentUpdateEvent is the next version of a live deployment, or
// ErrDeploymentUnchanged when no facet differs from the current one.
func (q *Queries) DeploymentUpdateEvent(ctx apigen.Context, deploymentID, seq int64, now time.Time, d *apigen.Deployment) (*apigen.DeploymentEvent, error) {
	prev, err := q.GetLatestDeploymentEvent(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	event, changed := BuildDeploymentUpdateEvent(prev, d, ctx.AttributionUserID(), now)
	if !changed {
		return nil, ErrDeploymentUnchanged
	}
	event.Seq = seq
	return event, nil
}

// DeploymentDeleteEvent is the tombstone of a live deployment, carrying the
// document it removes; its mutation is a delete.
func (q *Queries) DeploymentDeleteEvent(ctx apigen.Context, deploymentID, seq int64, now time.Time) (*apigen.DeploymentEvent, error) {
	prev, err := q.GetLatestDeploymentEvent(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	event := *prev
	event.Seq, event.EventTime, event.Author, event.EventType = seq, time.UnixMilli(now.UnixMilli()), ctx.AttributionUserID(), apigen.EventType_EVENT_TYPE_DELETE
	return &event, nil
}

// BuildDeploymentUpdateEvent advances the version, and the spec version when
// the spec changed, and reports whether any facet changed. The reducer
// derives the same numbers when the write lands; the event is the writer's
// view of the result.
func BuildDeploymentUpdateEvent(prev *apigen.DeploymentEvent, updated *apigen.Deployment, author int32, now time.Time) (*apigen.DeploymentEvent, bool) {
	prevDef := &prev.Value
	event := &apigen.DeploymentEvent{
		EventTime: time.UnixMilli(now.UnixMilli()), CreatedTime: prev.CreatedTime, Author: author,
		DeploymentID: prev.DeploymentID, Version: prev.Version + 1, SpecVersion: prev.SpecVersion, Value: *updated, EventType: apigen.EventType_EVENT_TYPE_UPDATE,
	}
	specChanged := !DeploymentSpecsEqual(&updated.Spec, &prevDef.Spec)
	if specChanged {
		event.SpecVersion++
	}
	changed := specChanged || !DeploymentSchedulingEqual(&updated.Scheduling, &prevDef.Scheduling) || updated.SpaceID != prevDef.SpaceID || updated.Name != prevDef.Name
	event.Value.ID = event.DeploymentID
	return event, changed
}

func DeploymentSpecsEqual(a, b *apigen.DeploymentSpec) bool {
	da := erru.Must(apigen.DecodeDeploymentSpec(a.Encode()))
	db := erru.Must(apigen.DecodeDeploymentSpec(b.Encode()))
	return reflect.DeepEqual(da, db)
}

func DeploymentSchedulingEqual(a, b *apigen.Scheduling) bool {
	da := erru.Must(apigen.DecodeScheduling(a.Encode()))
	db := erru.Must(apigen.DecodeScheduling(b.Encode()))
	return reflect.DeepEqual(da, db)
}

// reduceDeployment numbers the document: the next version of a live
// deployment, the next spec version when its spec differs from the current
// version's, or version 1 of a new one.
func (q *Queries) reduceDeployment(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id int64, d *apigen.Deployment) error {
	if d == nil {
		return fmt.Errorf("payload has no deployment")
	}
	value := *d
	value.ID = 0
	version, specVersion, created := int64(1), int64(1), env.EventTime
	prev, err := q.GetLatestDeploymentEvent(ctx, id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		version, specVersion, created = int64(prev.Version)+1, int64(prev.SpecVersion), prev.CreatedTime.UnixMilli()
		if !DeploymentSpecsEqual(&d.Spec, &prev.Value.Spec) {
			specVersion++
		}
	}
	if _, err := q.db.ExecContext(ctx, `INSERT INTO deployment_versions (deployment_id, version, spec_version, created_time, value, seq, event_time, author)
VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (deployment_id, version) DO NOTHING`,
		id, version, specVersion, created, value.Encode(), env.Seq, env.EventTime, env.Author); err != nil {
		return err
	}
	if err := q.upsert(ctx, meta, `INSERT INTO deployments (id, space_id, name, version, spec_version, created_time, seq, event_time, author)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET space_id = excluded.space_id, name = excluded.name, version = excluded.version, spec_version = excluded.spec_version,
  seq = excluded.seq, event_time = excluded.event_time, author = excluded.author
RETURNING created_time`,
		id, int64(d.SpaceID), d.Name, version, specVersion, created, env.Seq, env.EventTime, env.Author); err != nil {
		return err
	}
	meta.Version, meta.SpecVersion = int32(version), int32(specVersion)
	return nil
}

func (q *Queries) deleteDeploymentRow(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM deployments WHERE id = ?`, id)
	return err
}

// retainDeployment applies the retention rules to one deployment after its
// rows changed: finals of an ordinal go when the deployment is deleted, when
// the ordinal has a non-final instance, or when a newer final exists; a
// pruned instance takes its status with it; a version stays while it is the
// current one or a retained instance pins it.
func (q *Queries) retainDeployment(ctx context.Context, id int64) error {
	var current sql.NullInt64
	if err := q.db.QueryRowContext(ctx, `SELECT version FROM deployments WHERE id = ?`, id).Scan(&current); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	final := int64(apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED)
	rows, err := q.db.QueryContext(ctx, `SELECT s.id FROM scheduled_instances s WHERE s.deployment_id = ? AND s.state = ? AND (? = 0
  OR EXISTS (SELECT 1 FROM scheduled_instances o WHERE o.deployment_id = s.deployment_id AND o.instance_ordinal = s.instance_ordinal AND o.state != ?)
  OR s.id < (SELECT MAX(n.id) FROM scheduled_instances n WHERE n.deployment_id = s.deployment_id AND n.instance_ordinal = s.instance_ordinal AND n.state = ?))`,
		id, final, boolToInt(current.Valid), final, final)
	if err != nil {
		return err
	}
	var pruned []int64
	for rows.Next() {
		var instance int64
		if err := rows.Scan(&instance); err != nil {
			rows.Close()
			return err
		}
		pruned = append(pruned, instance)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, instance := range pruned {
		if err := q.deleteScheduledInstanceRows(ctx, instance); err != nil {
			return err
		}
	}
	_, err = q.db.ExecContext(ctx, `DELETE FROM deployment_versions WHERE deployment_id = ? AND version != ?
  AND version NOT IN (SELECT deployment_version FROM scheduled_instances WHERE deployment_id = ?)`, id, current.Int64, id)
	return err
}

// deletionEnvelope is the write that deleted an entity, for the opening of a
// stream that still carries rows the deleted entity left behind.
func (q *Queries) deletionEnvelope(ctx context.Context, t apigen.CoreEntityType, id int64) (rowEnvelope, error) {
	var env rowEnvelope
	err := q.db.QueryRowContext(ctx, `SELECT m.seq, e.time, e.actor FROM write_event_mutations m JOIN write_events e ON e.seq = m.seq
WHERE m.entity_type = ? AND m.entity_id = ? AND m.op = ? ORDER BY m.seq DESC LIMIT 1`, int64(t), id, int64(apigen.AuthzVerb_AUTHZ_VERB_DELETE)).Scan(&env.Seq, &env.EventTime, &env.Author)
	return env, err
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
