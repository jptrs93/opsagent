package pq

import (
	"context"
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

func (q *Queries) InsertAuthzGrantEvent(ctx context.Context, e AuthzGrantEvent) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO authz_grant_event_log (
			global_seq, event_time, created_time, author, grant_id, version,
			user_id, template_id, data_blob, event_type
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.GlobalSeq, e.EventTime, e.CreatedTime, e.Author, e.GrantID, e.Version,
		e.UserID, e.TemplateID, e.DataBlob, e.EventType)
	return err
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
