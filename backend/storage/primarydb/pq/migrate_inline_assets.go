package pq

import (
	"context"
	"database/sql"
)

// One-time v0.0.614 shape migration support: asset content of every size
// lives in the large-asset root and S3, so asset_store.inline_blob goes.
// assets.MigrateInlineContent writes each blob out, marks the row locally
// durable, and then drops the column. Remove after every active cluster has
// rolled forward, per the migrations.sql history-note convention.

func (q *Queries) AssetStoreHasInlineBlobColumn(ctx context.Context) (bool, error) {
	rows, err := q.db.QueryContext(ctx, `PRAGMA table_info(asset_store)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int64
		var name, typ string
		var notNull, pk int64
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == "inline_blob" {
			return true, nil
		}
	}
	return false, rows.Err()
}

type InlineAssetBlob struct {
	ID        string
	Sha256    string
	SizeBytes int64
	Blob      []byte
}

// ListInlineAssetBlobs returns the completed rows whose only copy is the
// inline blob, including empty content.
func (q *Queries) ListInlineAssetBlobs(ctx context.Context) ([]InlineAssetBlob, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, sha256, size_bytes, inline_blob FROM asset_store
WHERE sha256 != '' AND local_status = 0 AND remote_status = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InlineAssetBlob
	for rows.Next() {
		var b InlineAssetBlob
		if err := rows.Scan(&b.ID, &b.Sha256, &b.SizeBytes, &b.Blob); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (q *Queries) MarkInlineAssetBlobExternalized(ctx context.Context, id string) error {
	_, err := q.db.ExecContext(ctx, `UPDATE asset_store SET local_status = 1, inline_blob = x'' WHERE id = ?`, id)
	return err
}

func (q *Queries) DropAssetStoreInlineBlobColumn(ctx context.Context) error {
	_, err := q.db.ExecContext(ctx, `ALTER TABLE asset_store DROP COLUMN inline_blob`)
	return err
}
