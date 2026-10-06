package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const valueDirectoryColumns = `id, space_id, parent_id, key`

func scanValueDirectory(row scanner) (*apigen.ValueDirectory, error) {
	d := &apigen.ValueDirectory{}
	var parent uint64
	if err := row.Scan(&d.ID, &d.SpaceID, &parent, &d.Key); err != nil {
		return nil, err
	}
	d.ParentID = directoryRef(parent)
	return d, nil
}

// GetValueDirectoryByID returns the live directory, or sql.ErrNoRows when it
// never existed or was deleted.
func (q *Queries) GetValueDirectoryByID(ctx context.Context, directoryID uint64) (*apigen.ValueDirectory, error) {
	return scanValueDirectory(q.db.QueryRowContext(ctx, `SELECT `+valueDirectoryColumns+` FROM value_directories WHERE id = ?`, directoryID))
}

func (q *Queries) ListValueDirectories(ctx context.Context) ([]*apigen.ValueDirectory, error) {
	return listRows(ctx, q, scanValueDirectory, `SELECT `+valueDirectoryColumns+` FROM value_directories ORDER BY space_id, parent_id, key`)
}

type valueDirectoryRow struct {
	rowEnvelope
	Directory *apigen.ValueDirectory
}

func (q *Queries) listValueDirectoryRows(ctx context.Context) ([]valueDirectoryRow, error) {
	return listRows(ctx, q, func(row scanner) (valueDirectoryRow, error) {
		var r valueDirectoryRow
		d := &apigen.ValueDirectory{}
		var parent uint64
		if err := row.Scan(&d.ID, &d.SpaceID, &parent, &d.Key, &r.Seq, &r.EventTime, &r.Author, &r.CreatedTime); err != nil {
			return r, err
		}
		d.ParentID = directoryRef(parent)
		r.Directory = d
		return r, nil
	}, `SELECT `+valueDirectoryColumns+`, seq, event_time, author, created_time FROM value_directories ORDER BY id`)
}
