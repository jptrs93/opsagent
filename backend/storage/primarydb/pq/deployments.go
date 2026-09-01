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

func (q *Queries) InsertDeploymentEvent(ctx context.Context, e DeploymentEvent) error {
	_, err := q.db.ExecContext(ctx, `
	INSERT INTO deployment_event_log (
		global_seq, event_time, created_time, author, deployment_id, version,
		spec_version, space_assignment_version, name_version, value, event_type
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.GlobalSeq, e.EventTime, e.CreatedTime, e.Author, e.DeploymentID, e.Version,
		e.SpecVersion, e.SpaceAssignmentVersion, e.NameVersion, e.Value, e.EventType)
	return err
}
