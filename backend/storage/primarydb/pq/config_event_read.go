package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func scanConfigEvent(row scanner) (*apigen.ConfigEvent, error) {
	e := &apigen.ConfigEvent{Value: apigen.Config{Fs: &apigen.ConfigFs{}}}
	if err := row.Scan(&e.EventID, &e.Seq, &e.EventTime, &e.CreatedTime, &e.Author, &e.ConfigID, &e.Version, &e.ValueVersion, &e.SpaceVersion, &e.Value.Fs.Name, &e.Value.Fs.DirectoryID, &e.Value.SpaceID, &e.Value.Value, &e.EventType); err != nil {
		return nil, err
	}
	return e, nil
}

func (q *Queries) GetLatestConfigEvent(ctx context.Context, id int64) (*apigen.ConfigEvent, error) {
	return scanConfigEvent(q.db.QueryRowContext(ctx, `SELECT id, global_seq, event_time, created_time, author, config_id, version,
       value_version, space_version, name, value_directory_id, space_id, value, event_type
FROM config_event_log
WHERE config_id = ?
ORDER BY version DESC LIMIT 1`, id))
}

func (q *Queries) ListAllConfigEvents(ctx context.Context) ([]*apigen.ConfigEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, global_seq, event_time, created_time, author, config_id, version,
       value_version, space_version, name, value_directory_id, space_id, value, event_type
FROM config_event_log
WHERE config_id IN (SELECT config_id FROM config_event_log current
 WHERE current.version = (SELECT MAX(version) FROM config_event_log WHERE config_id=current.config_id) AND current.event_type != 3)
ORDER BY config_id, version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*apigen.ConfigEvent
	for rows.Next() {
		e, err := scanConfigEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (q *Queries) ListConfigEventsAtSeq(ctx context.Context, id int64) ([]*apigen.ConfigEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, global_seq, event_time, created_time, author, config_id, version,
       value_version, space_version, name, value_directory_id, space_id, value, event_type FROM config_event_log WHERE global_seq = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*apigen.ConfigEvent
	for rows.Next() {
		e, err := scanConfigEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (q *Queries) ListLatestLiveConfigEvents(ctx context.Context) ([]*apigen.ConfigEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, global_seq, event_time, created_time, author, config_id, version,
       value_version, space_version, name, value_directory_id, space_id, value, event_type
FROM config_event_log e
WHERE e.version = (SELECT MAX(version) FROM config_event_log WHERE config_id=e.config_id)
  AND e.event_type != 3
ORDER BY name, config_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*apigen.ConfigEvent
	for rows.Next() {
		e, err := scanConfigEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

const listConfigVersionIDsByConfigID = `SELECT id FROM config_event_log
WHERE config_id = ? AND value_changed != 0
ORDER BY value_version
`

func (q *Queries) ListConfigVersionIDsByConfigID(ctx context.Context, configID int64) ([]int64, error) {
	rows, err := q.db.QueryContext(ctx, listConfigVersionIDsByConfigID, configID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		items = append(items, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
