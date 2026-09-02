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

const authzRuleTemplateEventColumns = `e.id, e.global_seq, e.event_time, e.created_time, e.author,
	e.template_id, e.version, e.name, e.builtin, e.data_blob, e.event_type`

func scanAuthzRuleTemplateEvent(scan func(dest ...any) error) (AuthzRuleTemplateEvent, error) {
	var e AuthzRuleTemplateEvent
	err := scan(&e.ID, &e.GlobalSeq, &e.EventTime, &e.CreatedTime, &e.Author,
		&e.TemplateID, &e.Version, &e.Name, &e.Builtin, &e.DataBlob, &e.EventType)
	return e, err
}

func (q *Queries) NextAuthzRuleTemplateID(ctx context.Context) (int64, error) {
	var id int64
	err := q.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(template_id), 0) + 1 FROM authz_rule_template_event_log`).Scan(&id)
	return id, err
}

func (q *Queries) GetLatestAuthzRuleTemplateEvent(ctx context.Context, templateID int64) (AuthzRuleTemplateEvent, error) {
	return scanAuthzRuleTemplateEvent(q.db.QueryRowContext(ctx, `
		SELECT `+authzRuleTemplateEventColumns+`
		FROM authz_rule_template_event_log e
		WHERE e.template_id = ?
		ORDER BY e.version DESC LIMIT 1`, templateID).Scan)
}

// AuthzRuleTemplateLatest is a template's latest event joined with the first
// event's author (the creator, which the identity surfaces regardless of who
// edited last).
type AuthzRuleTemplateLatest struct {
	AuthzRuleTemplateEvent
	FirstAuthor int64
}

func (q *Queries) ListLatestAuthzRuleTemplateEvents(ctx context.Context) ([]AuthzRuleTemplateLatest, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT `+authzRuleTemplateEventColumns+`, f.author
		FROM authz_rule_template_event_log e
		JOIN (SELECT template_id, MAX(version) AS version
		      FROM authz_rule_template_event_log GROUP BY template_id) latest
		  ON latest.template_id = e.template_id AND latest.version = e.version
		JOIN authz_rule_template_event_log f ON f.id =
		    (SELECT MIN(m.id) FROM authz_rule_template_event_log m
		     WHERE m.template_id = e.template_id)
		ORDER BY e.template_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuthzRuleTemplateLatest
	for rows.Next() {
		var r AuthzRuleTemplateLatest
		if err := rows.Scan(&r.ID, &r.GlobalSeq, &r.EventTime, &r.CreatedTime, &r.Author,
			&r.TemplateID, &r.Version, &r.Name, &r.Builtin, &r.DataBlob, &r.EventType,
			&r.FirstAuthor); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

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

type AuthzGrantEvent struct {
	ID          int64
	GlobalSeq   int64
	EventTime   int64
	CreatedTime int64
	Author      int64
	GrantID     int64
	Version     int64
	UserID      int64
	TemplateID  int64
	DataBlob    []byte
	EventType   int64
}

const authzGrantEventColumns = `e.id, e.global_seq, e.event_time, e.created_time, e.author,
	e.grant_id, e.version, e.user_id, e.template_id, e.data_blob, e.event_type`

func scanAuthzGrantEvent(scan func(dest ...any) error) (AuthzGrantEvent, error) {
	var e AuthzGrantEvent
	err := scan(&e.ID, &e.GlobalSeq, &e.EventTime, &e.CreatedTime, &e.Author,
		&e.GrantID, &e.Version, &e.UserID, &e.TemplateID, &e.DataBlob, &e.EventType)
	return e, err
}

func (q *Queries) NextAuthzGrantID(ctx context.Context) (int64, error) {
	var id int64
	err := q.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(grant_id), 0) + 1 FROM authz_grant_event_log`).Scan(&id)
	return id, err
}

func (q *Queries) GetLatestAuthzGrantEvent(ctx context.Context, grantID int64) (AuthzGrantEvent, error) {
	return scanAuthzGrantEvent(q.db.QueryRowContext(ctx, `
		SELECT `+authzGrantEventColumns+`
		FROM authz_grant_event_log e
		WHERE e.grant_id = ?
		ORDER BY e.version DESC LIMIT 1`, grantID).Scan)
}

func (q *Queries) ListLatestAuthzGrantEvents(ctx context.Context) ([]AuthzGrantEvent, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT `+authzGrantEventColumns+`
		FROM authz_grant_event_log e
		JOIN (SELECT grant_id, MAX(version) AS version
		      FROM authz_grant_event_log GROUP BY grant_id) latest
		  ON latest.grant_id = e.grant_id AND latest.version = e.version
		ORDER BY e.grant_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuthzGrantEvent
	for rows.Next() {
		e, err := scanAuthzGrantEvent(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
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

type GlobalAccessRuleEvent struct {
	ID          int64
	GlobalSeq   int64
	EventTime   int64
	CreatedTime int64
	Author      int64
	RuleID      int64
	Version     int64
	Name        string
	DataBlob    []byte
	EventType   int64
}

const globalAccessRuleEventColumns = `e.id, e.global_seq, e.event_time, e.created_time, e.author,
	e.rule_id, e.version, e.name, e.data_blob, e.event_type`

func scanGlobalAccessRuleEvent(scan func(dest ...any) error) (GlobalAccessRuleEvent, error) {
	var e GlobalAccessRuleEvent
	err := scan(&e.ID, &e.GlobalSeq, &e.EventTime, &e.CreatedTime, &e.Author,
		&e.RuleID, &e.Version, &e.Name, &e.DataBlob, &e.EventType)
	return e, err
}

func (q *Queries) NextGlobalAccessRuleID(ctx context.Context) (int64, error) {
	var id int64
	err := q.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(rule_id), 0) + 1 FROM global_access_rule_event_log`).Scan(&id)
	return id, err
}

func (q *Queries) GetLatestGlobalAccessRuleEvent(ctx context.Context, ruleID int64) (GlobalAccessRuleEvent, error) {
	return scanGlobalAccessRuleEvent(q.db.QueryRowContext(ctx, `
		SELECT `+globalAccessRuleEventColumns+`
		FROM global_access_rule_event_log e
		WHERE e.rule_id = ?
		ORDER BY e.version DESC LIMIT 1`, ruleID).Scan)
}

func (q *Queries) ListLatestGlobalAccessRuleEvents(ctx context.Context) ([]GlobalAccessRuleEvent, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT `+globalAccessRuleEventColumns+`
		FROM global_access_rule_event_log e
		JOIN (SELECT rule_id, MAX(version) AS version
		      FROM global_access_rule_event_log GROUP BY rule_id) latest
		  ON latest.rule_id = e.rule_id AND latest.version = e.version
		ORDER BY e.rule_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GlobalAccessRuleEvent
	for rows.Next() {
		e, err := scanGlobalAccessRuleEvent(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (q *Queries) CountGlobalAccessRuleEventsByName(ctx context.Context, name string) (int64, error) {
	var count int64
	err := q.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM global_access_rule_event_log WHERE name = ?`, name).Scan(&count)
	return count, err
}

func (q *Queries) InsertGlobalAccessRuleEvent(ctx context.Context, e GlobalAccessRuleEvent) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO global_access_rule_event_log (
			global_seq, event_time, created_time, author, rule_id, version,
			name, data_blob, event_type
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.GlobalSeq, e.EventTime, e.CreatedTime, e.Author, e.RuleID, e.Version,
		e.Name, e.DataBlob, e.EventType)
	return err
}
