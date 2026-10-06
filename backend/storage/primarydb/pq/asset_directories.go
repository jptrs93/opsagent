package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const assetDirectoryColumns = `id, space_id, parent_id, key`

func scanAssetDirectory(row scanner) (apigen.AssetDirectory, error) {
	var d apigen.AssetDirectory
	var parent uint64
	err := row.Scan(&d.ID, &d.SpaceID, &parent, &d.Key)
	d.ParentID = directoryRef(parent)
	return d, err
}

// GetAssetDirectoryByID returns the live directory, or sql.ErrNoRows when it
// never existed or was deleted.
func (q *Queries) GetAssetDirectoryByID(ctx context.Context, directoryID uint64) (apigen.AssetDirectory, error) {
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
		var parent uint64
		if err := row.Scan(&r.Directory.ID, &r.Directory.SpaceID, &parent, &r.Directory.Key, &r.Seq, &r.EventTime, &r.Author, &r.CreatedTime); err != nil {
			return r, err
		}
		r.Directory.ParentID = directoryRef(parent)
		return r, nil
	}, `SELECT `+assetDirectoryColumns+`, seq, event_time, author, created_time FROM asset_directories ORDER BY id`)
}
