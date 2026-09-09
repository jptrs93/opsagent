package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func scanAuthzGrantEvent(row scanner) (apigen.AuthzGrantEvent, error) {
	var e apigen.AuthzGrantEvent
	var blob []byte
	if err := row.Scan(&e.EventID, &e.Seq, &e.EventTime, &e.CreatedTime, &e.Author, &e.AuthzGrantID, &e.Version, &e.Value.UserID, &e.Value.TemplateID, &blob, &e.EventType); err != nil {
		return e, err
	}
	var err error
	e.Value.Grant, err = apigen.DecodeAuthzGrant(blob)
	return e, err
}

func (q *Queries) GetLatestAuthzGrantEvent(ctx context.Context, id int64) (apigen.AuthzGrantEvent, error) {
	return scanAuthzGrantEvent(q.db.QueryRowContext(ctx, `SELECT id, global_seq, event_time, created_time, author, grant_id, version,
       user_id, template_id, data_blob, event_type
FROM authz_grant_event_log
WHERE grant_id = ?
ORDER BY version DESC LIMIT 1`, id))
}

func (q *Queries) ListLatestAuthzGrantEvents(ctx context.Context) ([]apigen.AuthzGrantEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT e.id, e.global_seq, e.event_time, e.created_time, e.author, e.grant_id,
       e.version, e.user_id, e.template_id, e.data_blob, e.event_type
FROM authz_grant_event_log e
JOIN (SELECT grant_id, MAX(version) AS version
      FROM authz_grant_event_log GROUP BY grant_id) latest
  ON latest.grant_id = e.grant_id AND latest.version = e.version
ORDER BY e.grant_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []apigen.AuthzGrantEvent
	for rows.Next() {
		e, err := scanAuthzGrantEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (q *Queries) ListAuthzGrantEventsAtSeq(ctx context.Context, id int64) ([]apigen.AuthzGrantEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id,global_seq,event_time,created_time,author,grant_id,version,user_id,template_id,data_blob,event_type FROM authz_grant_event_log WHERE global_seq = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []apigen.AuthzGrantEvent
	for rows.Next() {
		e, err := scanAuthzGrantEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (q *Queries) ListLatestLiveAuthzGrantEvents(ctx context.Context) ([]apigen.AuthzGrantEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT e.id, e.global_seq, e.event_time, e.created_time, e.author, e.grant_id,
       e.version, e.user_id, e.template_id, e.data_blob, e.event_type
FROM authz_grant_event_log e
JOIN (SELECT grant_id, MAX(version) AS version
      FROM authz_grant_event_log GROUP BY grant_id) latest
  ON latest.grant_id = e.grant_id AND latest.version = e.version
WHERE e.event_type != 3
ORDER BY e.grant_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []apigen.AuthzGrantEvent
	for rows.Next() {
		e, err := scanAuthzGrantEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
