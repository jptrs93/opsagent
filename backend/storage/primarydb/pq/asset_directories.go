package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const assetDirectoryColumns = `id, space_id, parent_id, key`

func scanAssetDirectory(row scanner) (apigen.AssetDirectory, error) {
	var d apigen.AssetDirectory
	err := row.Scan(&d.ID, &d.SpaceID, &d.ParentID, &d.Key)
	return d, err
}

// GetAssetDirectoryByID returns the live directory, or sql.ErrNoRows when it
// never existed or was deleted.
func (q *Queries) GetAssetDirectoryByID(ctx context.Context, directoryID int64) (apigen.AssetDirectory, error) {
	return scanAssetDirectory(q.db.QueryRowContext(ctx, `SELECT `+assetDirectoryColumns+` FROM asset_directories WHERE id = ?`, directoryID))
}

func (q *Queries) ListAssetDirectories(ctx context.Context) ([]apigen.AssetDirectory, error) {
	return listRows(ctx, q, scanAssetDirectory, `SELECT `+assetDirectoryColumns+` FROM asset_directories ORDER BY space_id, parent_id, key`)
}

type assetDirectoryRow struct {
	rowEnvelope
	Directory apigen.AssetDirectory
}

func (q *Queries) listAssetDirectoryRows(ctx context.Context) ([]assetDirectoryRow, error) {
	return listRows(ctx, q, func(row scanner) (assetDirectoryRow, error) {
		var r assetDirectoryRow
		if err := row.Scan(&r.Directory.ID, &r.Directory.SpaceID, &r.Directory.ParentID, &r.Directory.Key, &r.Seq, &r.EventTime, &r.Author, &r.CreatedTime); err != nil {
			return r, err
		}
		return r, nil
	}, `SELECT `+assetDirectoryColumns+`, seq, event_time, author, created_time FROM asset_directories ORDER BY id`)
}
