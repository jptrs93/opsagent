package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func scanAssetEvent(row scanner) (apigen.AssetEvent, error) {
	e := apigen.AssetEvent{Value: apigen.Asset{Fs: &apigen.AssetFs{}}}
	err := row.Scan(&e.EventID, &e.Seq, &e.EventTime, &e.CreatedTime, &e.Author, &e.AssetID, &e.Version, &e.ValueVersion, &e.SpaceVersion, &e.Value.Fs.Key, &e.Value.Fs.DirectoryID, &e.Value.SpaceID, &e.Value.SizeBytes, &e.Value.Sha256, &e.EventType)
	return e, err
}

func (q *Queries) GetLatestAssetEvent(ctx context.Context, id int64) (apigen.AssetEvent, error) {
	return scanAssetEvent(q.db.QueryRowContext(ctx, `SELECT id, global_seq, event_time, created_time, author, asset_id, version,
       value_version, space_version, key, asset_directory_id, space_id, size_bytes, sha256, event_type
FROM asset_event_log
WHERE asset_id = ?
ORDER BY version DESC LIMIT 1`, id))
}

func (q *Queries) ListAllAssetEvents(ctx context.Context) ([]apigen.AssetEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, global_seq, event_time, created_time, author, asset_id, version,
       value_version, space_version, key, asset_directory_id, space_id, size_bytes, sha256, event_type
FROM asset_event_log
WHERE asset_id IN (SELECT asset_id FROM asset_event_log current
 WHERE current.version = (SELECT MAX(version) FROM asset_event_log WHERE asset_id=current.asset_id) AND current.event_type != 3)
ORDER BY asset_id, version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []apigen.AssetEvent
	for rows.Next() {
		e, err := scanAssetEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (q *Queries) ListAssetEventsAtSeq(ctx context.Context, id int64) ([]apigen.AssetEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, global_seq, event_time, created_time, author, asset_id, version,
       value_version, space_version, key, asset_directory_id, space_id, size_bytes, sha256, event_type FROM asset_event_log WHERE global_seq = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []apigen.AssetEvent
	for rows.Next() {
		e, err := scanAssetEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (q *Queries) ListLatestLiveAssetEvents(ctx context.Context) ([]apigen.AssetEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, global_seq, event_time, created_time, author, asset_id, version,
       value_version, space_version, key, asset_directory_id, space_id, size_bytes, sha256, event_type
FROM asset_event_log e
WHERE e.version = (SELECT MAX(version) FROM asset_event_log WHERE asset_id=e.asset_id)
  AND e.event_type != 3
ORDER BY key, asset_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []apigen.AssetEvent
	for rows.Next() {
		e, err := scanAssetEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
