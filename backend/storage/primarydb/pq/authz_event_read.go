package pq

import (
	"context"
)

type AuthzRuleTemplateEvent struct {
	ID          int64
	GlobalSeq   int64
	EventTime   int64
	CreatedTime int64
	Author      int64
	TemplateID  int64
	Version     int64
	Name        string
	Builtin     int64
	DataBlob    []byte
	EventType   int64
}

type GlobalAccessRuleEvent struct {
	ID          int64
	GlobalSeq   int64
	EventTime   int64
	CreatedTime int64
	Author      int64
	RuleID      int64
	Version     int64
	Name        string
	Disabled    int64
	DataBlob    []byte
	EventType   int64
}

const countGlobalAccessRuleEventsByName = `SELECT COUNT(*) FROM global_access_rule_event_log WHERE name = ?
`

func (q *Queries) CountGlobalAccessRuleEventsByName(ctx context.Context, name string) (int64, error) {
	row := q.db.QueryRowContext(ctx, countGlobalAccessRuleEventsByName, name)
	var count int64
	err := row.Scan(&count)
	return count, err
}

const getLatestAuthzRuleTemplateEvent = `SELECT id, global_seq, event_time, created_time, author, template_id, version,
       name, builtin, data_blob, event_type
FROM authz_rule_template_event_log
WHERE template_id = ?
ORDER BY version DESC LIMIT 1
`

func (q *Queries) GetLatestAuthzRuleTemplateEvent(ctx context.Context, templateID int64) (AuthzRuleTemplateEvent, error) {
	row := q.db.QueryRowContext(ctx, getLatestAuthzRuleTemplateEvent, templateID)
	var i AuthzRuleTemplateEvent
	err := row.Scan(
		&i.ID,
		&i.GlobalSeq,
		&i.EventTime,
		&i.CreatedTime,
		&i.Author,
		&i.TemplateID,
		&i.Version,
		&i.Name,
		&i.Builtin,
		&i.DataBlob,
		&i.EventType,
	)
	return i, err
}

const getLatestGlobalAccessRuleEvent = `SELECT id, global_seq, event_time, created_time, author, rule_id, version,
       name, disabled, data_blob, event_type
FROM global_access_rule_event_log
WHERE rule_id = ?
ORDER BY version DESC LIMIT 1
`

func (q *Queries) GetLatestGlobalAccessRuleEvent(ctx context.Context, ruleID int64) (GlobalAccessRuleEvent, error) {
	row := q.db.QueryRowContext(ctx, getLatestGlobalAccessRuleEvent, ruleID)
	var i GlobalAccessRuleEvent
	err := row.Scan(
		&i.ID,
		&i.GlobalSeq,
		&i.EventTime,
		&i.CreatedTime,
		&i.Author,
		&i.RuleID,
		&i.Version,
		&i.Name,
		&i.Disabled,
		&i.DataBlob,
		&i.EventType,
	)
	return i, err
}

const listAuthzRuleTemplateEventsAtSeq = `SELECT id, global_seq, event_time, created_time, author, template_id, version, name, builtin, data_blob, event_type FROM authz_rule_template_event_log WHERE global_seq = ? ORDER BY id
`

func (q *Queries) ListAuthzRuleTemplateEventsAtSeq(ctx context.Context, globalSeq int64) ([]AuthzRuleTemplateEvent, error) {
	rows, err := q.db.QueryContext(ctx, listAuthzRuleTemplateEventsAtSeq, globalSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []AuthzRuleTemplateEvent
	for rows.Next() {
		var i AuthzRuleTemplateEvent
		if err := rows.Scan(
			&i.ID,
			&i.GlobalSeq,
			&i.EventTime,
			&i.CreatedTime,
			&i.Author,
			&i.TemplateID,
			&i.Version,
			&i.Name,
			&i.Builtin,
			&i.DataBlob,
			&i.EventType,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const listGlobalAccessRuleEventsAtSeq = `SELECT id, global_seq, event_time, created_time, author, rule_id, version, name, disabled, data_blob, event_type FROM global_access_rule_event_log WHERE global_seq = ? ORDER BY id
`

func (q *Queries) ListGlobalAccessRuleEventsAtSeq(ctx context.Context, globalSeq int64) ([]GlobalAccessRuleEvent, error) {
	rows, err := q.db.QueryContext(ctx, listGlobalAccessRuleEventsAtSeq, globalSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []GlobalAccessRuleEvent
	for rows.Next() {
		var i GlobalAccessRuleEvent
		if err := rows.Scan(
			&i.ID,
			&i.GlobalSeq,
			&i.EventTime,
			&i.CreatedTime,
			&i.Author,
			&i.RuleID,
			&i.Version,
			&i.Name,
			&i.Disabled,
			&i.DataBlob,
			&i.EventType,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const listLatestAuthzRuleTemplateEvents = `SELECT e.id, e.global_seq, e.event_time, e.created_time, e.author, e.template_id,
       e.version, e.name, e.builtin, e.data_blob, e.event_type
FROM authz_rule_template_event_log e
JOIN (SELECT template_id, MAX(version) AS version
      FROM authz_rule_template_event_log GROUP BY template_id) latest
  ON latest.template_id = e.template_id AND latest.version = e.version
ORDER BY e.template_id
`

func (q *Queries) ListLatestAuthzRuleTemplateEvents(ctx context.Context) ([]AuthzRuleTemplateEvent, error) {
	rows, err := q.db.QueryContext(ctx, listLatestAuthzRuleTemplateEvents)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []AuthzRuleTemplateEvent
	for rows.Next() {
		var i AuthzRuleTemplateEvent
		if err := rows.Scan(
			&i.ID,
			&i.GlobalSeq,
			&i.EventTime,
			&i.CreatedTime,
			&i.Author,
			&i.TemplateID,
			&i.Version,
			&i.Name,
			&i.Builtin,
			&i.DataBlob,
			&i.EventType,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const listLatestGlobalAccessRuleEvents = `SELECT e.id, e.global_seq, e.event_time, e.created_time, e.author, e.rule_id,
       e.version, e.name, e.disabled, e.data_blob, e.event_type
FROM global_access_rule_event_log e
JOIN (SELECT rule_id, MAX(version) AS version
      FROM global_access_rule_event_log GROUP BY rule_id) latest
  ON latest.rule_id = e.rule_id AND latest.version = e.version
ORDER BY e.rule_id
`

func (q *Queries) ListLatestGlobalAccessRuleEvents(ctx context.Context) ([]GlobalAccessRuleEvent, error) {
	rows, err := q.db.QueryContext(ctx, listLatestGlobalAccessRuleEvents)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []GlobalAccessRuleEvent
	for rows.Next() {
		var i GlobalAccessRuleEvent
		if err := rows.Scan(
			&i.ID,
			&i.GlobalSeq,
			&i.EventTime,
			&i.CreatedTime,
			&i.Author,
			&i.RuleID,
			&i.Version,
			&i.Name,
			&i.Disabled,
			&i.DataBlob,
			&i.EventType,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
