package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func (q *Queries) InsertAuthzRuleTemplateEvent(ctx context.Context, e AuthzRuleTemplateEvent) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO authz_rule_template_event_log (
			global_seq, event_time, created_time, author, template_id, version,
			name, builtin, data_blob, event_type
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.GlobalSeq, e.EventTime, e.CreatedTime, e.Author, e.TemplateID, e.Version,
		e.Name, e.Builtin, e.DataBlob, e.EventType)
	return err
}

func (q *Queries) InsertAuthzGrantEvent(ctx context.Context, e *apigen.AuthzGrantEvent) error {
	blob := e.Value.Grant.Encode()
	if blob == nil {
		blob = []byte{}
	}
	row := q.db.QueryRowContext(ctx, `INSERT INTO authz_grant_event_log (global_seq,event_time,created_time,author,grant_id,version,user_id,template_id,data_blob,event_type) VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id,global_seq,event_time,created_time,author,grant_id,version,user_id,template_id,data_blob,event_type`, e.Seq, e.EventTime, e.CreatedTime, e.Author, e.AuthzGrantID, e.Version, e.Value.UserID, e.Value.TemplateID, blob, e.EventType)
	written, err := scanAuthzGrantEvent(row)
	if err != nil {
		return err
	}
	*e = written
	return nil
}

func (q *Queries) InsertGlobalAccessRuleEvent(ctx context.Context, e GlobalAccessRuleEvent) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO global_access_rule_event_log (
			global_seq, event_time, created_time, author, rule_id, version,
			name, disabled, data_blob, event_type
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.GlobalSeq, e.EventTime, e.CreatedTime, e.Author, e.RuleID, e.Version,
		e.Name, e.Disabled, e.DataBlob, e.EventType)
	return err
}
