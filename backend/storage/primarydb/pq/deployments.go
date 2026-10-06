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

type deploymentVersionRow struct {
	id    uint64
	meta  apigen.EntityMeta
	value []byte
}

func (r *deploymentVersionRow) fields() []any {
	return []any{&r.id, &r.meta.Version, &r.meta.SpecVersion, &r.meta.CreatedTime, &r.value, &r.meta.UpdatedSeq, &r.meta.UpdatedTime, &r.meta.UpdatedActor}
}

func (r *deploymentVersionRow) toRecord() (*apigen.DeploymentRecord, error) {
	def, err := apigen.DecodeDeployment(r.value)
	if err != nil {
		return nil, err
	}
	def.ID = r.id
	return &apigen.DeploymentRecord{Deployment: *def, Meta: r.meta}, nil
}

func scanDeploymentRecord(row scanner) (*apigen.DeploymentRecord, error) {
	var r deploymentVersionRow
	if err := row.Scan(r.fields()...); err != nil {
		return nil, err
	}
	return r.toRecord()
}

func (q *Queries) NextDeploymentID(ctx context.Context) (uint64, error) {
	return q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT)
}

// GetDeploymentVersion returns a retained version: the current one of a live
// deployment or one a retained scheduled instance pins.
func (q *Queries) GetDeploymentVersion(ctx context.Context, deploymentID uint64, version uint32) (*apigen.DeploymentRecord, error) {
	return scanDeploymentRecord(q.db.QueryRowContext(ctx, `SELECT `+deploymentVersionColumns+` FROM deployment_versions v WHERE v.deployment_id = ? AND v.version = ?`, deploymentID, version))
}

// GetLatestDeployment returns the current version of a live deployment, or
// sql.ErrNoRows when the deployment is deleted or unknown.
func (q *Queries) GetLatestDeployment(ctx context.Context, deploymentID uint64) (*apigen.DeploymentRecord, error) {
	return scanDeploymentRecord(q.db.QueryRowContext(ctx, `SELECT `+deploymentVersionColumns+` `+liveDeploymentVersionsFrom+` WHERE d.id = ?`, deploymentID))
}

// ListActiveDeployments returns the current version of every live deployment.
func (q *Queries) ListActiveDeployments(ctx context.Context) ([]*apigen.DeploymentRecord, error) {
	return q.queryDeploymentRecords(ctx, `SELECT `+deploymentVersionColumns+` `+liveDeploymentVersionsFrom+` ORDER BY d.id`)
}

// ListRetainedDeploymentVersions returns every retained version row in
// (deployment, version) order.
func (q *Queries) ListRetainedDeploymentVersions(ctx context.Context) ([]*apigen.DeploymentRecord, error) {
	return q.queryDeploymentRecords(ctx, `SELECT `+deploymentVersionColumns+` FROM deployment_versions v ORDER BY v.deployment_id, v.version`)
}

func (q *Queries) queryDeploymentRecords(ctx context.Context, query string, args ...any) ([]*apigen.DeploymentRecord, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []*apigen.DeploymentRecord
	for rows.Next() {
		record, err := scanDeploymentRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// deploymentVersionFacts numbers a deployment's logged documents the way the
// reducer does: every create or update is the next version, the spec
// version advances when the spec changed, and the creation time is the first
// row's. The log holds documents only; a history is derived on read.
type deploymentVersionFacts struct {
	version, specVersion uint32
	createdTime          int64
	previous             *apigen.Deployment
}

func (f *deploymentVersionFacts) next(d *apigen.Deployment, eventTime int64) {
	if f.previous == nil {
		f.version, f.specVersion, f.createdTime = 1, 1, eventTime
	} else {
		f.version++
		if !DeploymentSpecsEqual(&d.Spec, &f.previous.Spec) {
			f.specVersion++
		}
	}
	f.previous = d
}

func (f *deploymentVersionFacts) record(id uint64, seq, eventTime, actor int64, deleted bool, d *apigen.Deployment) *apigen.DeploymentRecord {
	value := *d
	value.ID = id
	return &apigen.DeploymentRecord{Deployment: value, Meta: apigen.EntityMeta{
		CreatedTime: f.createdTime, UpdatedTime: eventTime, UpdatedSeq: seq, UpdatedActor: actor,
		Version: f.version, SpecVersion: f.specVersion, Deleted: deleted,
	}}
}

// deploymentLogRecords is a deployment's history, oldest first, read from
// the write log; a delete carries the document it removed under the facts
// of the last version.
func (q *Queries) deploymentLogRecords(ctx context.Context, deploymentID uint64) ([]*apigen.DeploymentRecord, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT m.seq, e.time, e.actor, m.op, m.payload FROM write_event_mutations m JOIN write_events e ON e.seq = m.seq
WHERE m.entity_type = ? AND m.entity_id = ? ORDER BY m.seq, m.idx`, int64(apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT), deploymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []*apigen.DeploymentRecord
	var facts deploymentVersionFacts
	for rows.Next() {
		var seq, eventTime, actor, op int64
		var payload []byte
		if err := rows.Scan(&seq, &eventTime, &actor, &op, &payload); err != nil {
			return nil, err
		}
		if apigen.AuthzVerb(op) == apigen.AuthzVerb_AUTHZ_VERB_DELETE {
			if facts.previous != nil {
				records = append(records, facts.record(deploymentID, seq, eventTime, actor, true, facts.previous))
			}
			continue
		}
		entity, err := apigen.DecodeCoreEntity(payload)
		if err != nil || entity.Value.Deployment == nil {
			return nil, fmt.Errorf("deployment %d at seq %d: %v", deploymentID, seq, err)
		}
		facts.next(entity.Value.Deployment, eventTime)
		records = append(records, facts.record(deploymentID, seq, eventTime, actor, false, entity.Value.Deployment))
	}
	return records, rows.Err()
}

// deploymentVersionFromLog is one version of a deployment read from the write
// log, for a version the tables no longer retain.
func (q *Queries) deploymentVersionFromLog(ctx context.Context, deploymentID uint64, version uint32) (*apigen.DeploymentRecord, error) {
	records, err := q.deploymentLogRecords(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	for _, r := range records {
		if r.Meta.Version == version && !r.Meta.Deleted {
			return r, nil
		}
	}
	return nil, sql.ErrNoRows
}

// ListDeploymentHistory is a deployment's history, oldest first.
func (q *Queries) ListDeploymentHistory(ctx context.Context, deploymentID uint64) ([]*apigen.DeploymentRecord, error) {
	return q.deploymentLogRecords(ctx, deploymentID)
}

// ListDeletedDeployments returns one tombstone per deleted deployment,
// newest deletion first, read from the write log.
func (q *Queries) ListDeletedDeployments(ctx context.Context) ([]*apigen.DeploymentRecord, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT DISTINCT entity_id FROM write_event_mutations WHERE entity_type = ? AND op = ? ORDER BY entity_id`,
		int64(apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT), int64(apigen.AuthzVerb_AUTHZ_VERB_DELETE))
	if err != nil {
		return nil, err
	}
	var ids []uint64
	for rows.Next() {
		var id uint64
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
	var records []*apigen.DeploymentRecord
	for _, id := range ids {
		history, err := q.deploymentLogRecords(ctx, id)
		if err != nil {
			return nil, err
		}
		if n := len(history); n > 0 && history[n-1].Meta.Deleted {
			records = append(records, history[n-1])
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Meta.UpdatedTime != records[j].Meta.UpdatedTime {
			return records[i].Meta.UpdatedTime > records[j].Meta.UpdatedTime
		}
		return records[i].Deployment.ID > records[j].Deployment.ID
	})
	return records, nil
}

// DeploymentCreateRecord is the first version of a new deployment. Nothing
// is written: the caller returns its mutation from Commit.
func DeploymentCreateRecord(ctx apigen.Context, deploymentID uint64, seq int64, now time.Time, d *apigen.Deployment) *apigen.DeploymentRecord {
	at := now.UnixMilli()
	value := *d
	value.ID = deploymentID
	return &apigen.DeploymentRecord{Deployment: value, Meta: apigen.EntityMeta{
		UpdatedSeq: seq, UpdatedTime: at, CreatedTime: at, UpdatedActor: ctx.AttributionUserID(), Version: 1, SpecVersion: 1,
	}}
}

// DeploymentUpdateRecord is the next version of a live deployment, or
// ErrDeploymentUnchanged when no facet differs from the current one.
func (q *Queries) DeploymentUpdateRecord(ctx apigen.Context, deploymentID uint64, seq int64, now time.Time, d *apigen.Deployment) (*apigen.DeploymentRecord, error) {
	prev, err := q.GetLatestDeployment(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	record, changed := BuildDeploymentUpdateRecord(prev, d, ctx.AttributionUserID(), now)
	if !changed {
		return nil, ErrDeploymentUnchanged
	}
	record.Meta.UpdatedSeq = seq
	return record, nil
}

// DeploymentDeleteRecord is the tombstone of a live deployment, carrying the
// document it removes; its mutation is a delete.
func (q *Queries) DeploymentDeleteRecord(ctx apigen.Context, deploymentID uint64, seq int64, now time.Time) (*apigen.DeploymentRecord, error) {
	prev, err := q.GetLatestDeployment(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	record := *prev
	record.Meta.UpdatedSeq, record.Meta.UpdatedTime, record.Meta.UpdatedActor, record.Meta.Deleted = seq, now.UnixMilli(), ctx.AttributionUserID(), true
	return &record, nil
}

// BuildDeploymentUpdateRecord advances the version, and the spec version
// when the spec changed, and reports whether any facet changed. The reducer
// derives the same numbers when the write lands; the record is the writer's
// view of the result.
func BuildDeploymentUpdateRecord(prev *apigen.DeploymentRecord, updated *apigen.Deployment, author int64, now time.Time) (*apigen.DeploymentRecord, bool) {
	prevDef := &prev.Deployment
	value := *updated
	value.ID = prev.Deployment.ID
	record := &apigen.DeploymentRecord{Deployment: value, Meta: apigen.EntityMeta{
		UpdatedTime: now.UnixMilli(), CreatedTime: prev.Meta.CreatedTime, UpdatedActor: author,
		Version: prev.Meta.Version + 1, SpecVersion: prev.Meta.SpecVersion,
	}}
	specChanged := !DeploymentSpecsEqual(&updated.Spec, &prevDef.Spec)
	if specChanged {
		record.Meta.SpecVersion++
	}
	changed := specChanged || !DeploymentSchedulingEqual(&updated.Scheduling, &prevDef.Scheduling) || updated.SpaceID != prevDef.SpaceID || updated.Name != prevDef.Name
	return record, changed
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
func (q *Queries) reduceDeployment(ctx context.Context, env rowEnvelope, meta *apigen.EntityMeta, id uint64, d *apigen.Deployment) error {
	if d == nil {
		return fmt.Errorf("payload has no deployment")
	}
	value := *d
	value.ID = 0
	version, specVersion, created := uint32(1), uint32(1), env.EventTime
	prev, err := q.GetLatestDeployment(ctx, id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		version, specVersion, created = prev.Meta.Version+1, prev.Meta.SpecVersion, prev.Meta.CreatedTime
		if !DeploymentSpecsEqual(&d.Spec, &prev.Deployment.Spec) {
			specVersion++
		}
	}
	if env.Logged != nil {
		version, specVersion = env.Logged.version, env.Logged.specVersion
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
		id, d.SpaceID, d.Name, version, specVersion, created, env.Seq, env.EventTime, env.Author); err != nil {
		return err
	}
	meta.Version, meta.SpecVersion = version, specVersion
	return nil
}

func (q *Queries) deleteDeploymentRow(ctx context.Context, id uint64) error {
	_, err := q.db.ExecContext(ctx, `DELETE FROM deployments WHERE id = ?`, id)
	return err
}

// retainDeployment applies the retention rules to one deployment after its
// rows changed: finals of an ordinal go when the deployment is deleted, when
// the ordinal has a non-final instance, or when a newer final exists; a
// pruned instance takes its status with it; a version stays while it is the
// current one or a retained instance pins it.
func (q *Queries) retainDeployment(ctx context.Context, id uint64) error {
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
	var pruned []uint64
	for rows.Next() {
		var instance uint64
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
func (q *Queries) deletionEnvelope(ctx context.Context, t apigen.CoreEntityType, id uint64) (rowEnvelope, error) {
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
