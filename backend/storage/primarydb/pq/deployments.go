package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const (
	DeploymentEventCreate = int64(apigen.AuthzVerb_AUTHZ_VERB_CREATE)
	DeploymentEventUpdate = int64(apigen.AuthzVerb_AUTHZ_VERB_UPDATE)
	DeploymentEventDelete = int64(apigen.AuthzVerb_AUTHZ_VERB_DELETE)
)

type DeploymentEvent struct {
	ID                     int64
	GlobalSeq              int64
	CreatedAt              int64 // epoch ms
	Author                 int64
	DeploymentID           int64
	Version                int64
	SpecVersion            int64
	SpaceAssignmentVersion int64
	NameVersion            int64
	Value                  []byte
	EventType              int64
}

const deploymentEventSelect = `
	SELECT e.id, e.global_seq, e.created_at, e.author, e.deployment_id, e.version,
	       e.spec_version, e.space_assignment_version, e.name_version, e.value,
	       e.event_type
	FROM deployment_event_log e`

type deploymentEventScanner interface {
	Scan(dest ...any) error
}

func scanDeploymentEvent(scanner deploymentEventScanner) (DeploymentEvent, error) {
	var e DeploymentEvent
	err := scanner.Scan(&e.ID, &e.GlobalSeq, &e.CreatedAt, &e.Author, &e.DeploymentID,
		&e.Version, &e.SpecVersion, &e.SpaceAssignmentVersion, &e.NameVersion,
		&e.Value, &e.EventType)
	return e, err
}

func (q *Queries) InsertDeploymentEvent(ctx context.Context, e DeploymentEvent) error {
	_, err := q.db.ExecContext(ctx, `
	INSERT INTO deployment_event_log (
		global_seq, created_at, author, deployment_id, version, spec_version,
		space_assignment_version, name_version, value, event_type
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.GlobalSeq, e.CreatedAt, e.Author, e.DeploymentID, e.Version, e.SpecVersion,
		e.SpaceAssignmentVersion, e.NameVersion, e.Value, e.EventType)
	return err
}

func (q *Queries) NextDeploymentID(ctx context.Context) (int64, error) {
	var id int64
	err := q.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(deployment_id), 0) + 1 FROM deployment_event_log`).Scan(&id)
	return id, err
}

func (q *Queries) GetLatestDeploymentEvent(ctx context.Context, deploymentID int64) (DeploymentEvent, error) {
	return scanDeploymentEvent(q.db.QueryRowContext(ctx, deploymentEventSelect+`
	WHERE e.deployment_id = ?
	ORDER BY e.version DESC LIMIT 1`, deploymentID))
}

func (q *Queries) ListLatestDeploymentEvents(ctx context.Context) ([]DeploymentEvent, error) {
	return q.listDeploymentEvents(ctx, deploymentEventSelect+`
	JOIN (SELECT deployment_id, MAX(version) AS version
	      FROM deployment_event_log GROUP BY deployment_id) latest
	  ON latest.deployment_id = e.deployment_id AND latest.version = e.version
	ORDER BY e.deployment_id`)
}

func (q *Queries) ListDeploymentEvents(ctx context.Context, deploymentID int64) ([]DeploymentEvent, error) {
	return q.listDeploymentEvents(ctx, deploymentEventSelect+`
	WHERE e.deployment_id = ?
	ORDER BY e.version ASC`, deploymentID)
}

func (q *Queries) GetDeploymentEventBySpecVersion(ctx context.Context, deploymentID, specVersion int64) (DeploymentEvent, error) {
	return scanDeploymentEvent(q.db.QueryRowContext(ctx, deploymentEventSelect+`
	WHERE e.deployment_id = ? AND e.spec_version = ?
	ORDER BY e.version ASC LIMIT 1`, deploymentID, specVersion))
}

func (q *Queries) listDeploymentEvents(ctx context.Context, query string, args ...any) ([]DeploymentEvent, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeploymentEvent
	for rows.Next() {
		e, err := scanDeploymentEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
