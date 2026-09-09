package pq

import (
	"context"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const deploymentEventColumns = `id, global_seq, event_time, created_time, author, deployment_id,
 version, spec_version, space_assignment_version, name_version, value, event_type`

// scanDeploymentEvent is shared by reads and INSERT RETURNING.
func scanDeploymentEvent(row interface{ Scan(...any) error }) (*apigen.DeploymentEvent, error) {
	var event apigen.DeploymentEvent
	var eventTime, createdTime int64
	var value []byte
	if err := row.Scan(&event.EventID, &event.Seq, &eventTime, &createdTime, &event.Author,
		&event.DeploymentID, &event.Version, &event.SpecVersion, &event.SpaceVersion,
		&event.NameVersion, &value, &event.EventType); err != nil {
		return nil, err
	}
	def, err := apigen.DecodeDeployment(value)
	if err != nil {
		return nil, err
	}
	event.EventTime, event.CreatedTime = time.UnixMilli(eventTime), time.UnixMilli(createdTime)
	event.Value = *def
	return &event, nil
}

func (q *Queries) NextDeploymentID(ctx context.Context) (int64, error) {
	var id int64
	err := q.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(deployment_id), 0) + 1 FROM deployment_event_log`).Scan(&id)
	return id, err
}

type GetDeploymentEventByVersionParams struct {
	DeploymentID int64
	Version      int64
}

func (q *Queries) GetDeploymentEventByVersion(ctx context.Context, arg GetDeploymentEventByVersionParams) (*apigen.DeploymentEvent, error) {
	return scanDeploymentEvent(q.db.QueryRowContext(ctx, `SELECT `+deploymentEventColumns+` FROM deployment_event_log WHERE deployment_id = ? AND version = ?`, arg.DeploymentID, arg.Version))
}

func (q *Queries) GetLatestDeploymentEvent(ctx context.Context, deploymentID int64) (*apigen.DeploymentEvent, error) {
	return scanDeploymentEvent(q.db.QueryRowContext(ctx, `SELECT `+deploymentEventColumns+` FROM deployment_event_log WHERE deployment_id = ? ORDER BY version DESC LIMIT 1`, deploymentID))
}

func (q *Queries) ListLatestDeploymentEvents(ctx context.Context) ([]*apigen.DeploymentEvent, error) {
	return q.queryDeploymentEvents(ctx, `SELECT `+deploymentEventColumns+` FROM deployment_event_log
 WHERE (deployment_id, version) IN (SELECT deployment_id, MAX(version) FROM deployment_event_log GROUP BY deployment_id)
 ORDER BY deployment_id`)
}

// ListActiveDeployments returns the latest event of each live deployment.
// Select the latest version before excluding tombstones, so deletes cannot
// expose an earlier live event.
func (q *Queries) ListActiveDeployments(ctx context.Context) ([]*apigen.DeploymentEvent, error) {
	return q.queryDeploymentEvents(ctx, `SELECT `+deploymentEventColumns+` FROM deployment_event_log
 WHERE (deployment_id, version) IN (SELECT deployment_id, MAX(version) FROM deployment_event_log GROUP BY deployment_id)
 AND event_type != 3
 ORDER BY deployment_id`)
}

func (q *Queries) ListDeletedDeploymentEvents(ctx context.Context) ([]*apigen.DeploymentEvent, error) {
	// The literal event type uses the partial index for deleted deployments.
	return q.queryDeploymentEvents(ctx, `SELECT `+deploymentEventColumns+` FROM deployment_event_log WHERE event_type = 3 ORDER BY event_time DESC, deployment_id DESC`)
}

func (q *Queries) ListDeploymentEvents(ctx context.Context, deploymentID int64) ([]*apigen.DeploymentEvent, error) {
	return q.queryDeploymentEvents(ctx, `SELECT `+deploymentEventColumns+` FROM deployment_event_log WHERE deployment_id = ? ORDER BY version ASC`, deploymentID)
}

// ListDeploymentEventsAtSeq is the publication test oracle.
func (q *Queries) ListDeploymentEventsAtSeq(ctx context.Context, seq int64) ([]*apigen.DeploymentEvent, error) {
	return q.queryDeploymentEvents(ctx, `SELECT `+deploymentEventColumns+` FROM deployment_event_log WHERE global_seq = ? ORDER BY id`, seq)
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
