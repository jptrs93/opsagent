package pq

import (
	"context"
)

// Hand-written secret/config container reads. A container's current space is
// the newest row of its append-only *_spaces log — creation writes the first
// row in the same tx, so the join always finds one. Reads exclude soft-deleted
// rows (deleted_at != 0).

// SecretRow is a live secret identity with its current space.
type SecretRow struct {
	ID               int64
	Name             string
	SpaceID          int64
	ValueDirectoryID int64
	CreatedAt        int64
}

// ConfigRow is a live config identity with its current space.
type ConfigRow struct {
	ID               int64
	Name             string
	SpaceID          int64
	ValueDirectoryID int64
	CreatedAt        int64
}

const secretSpaceExpr = `(SELECT sp.space_id FROM secret_spaces sp WHERE sp.secret_id = s.id ORDER BY sp.id DESC LIMIT 1)`

const secretRowSelect = `SELECT s.id, s.name, ` + secretSpaceExpr + `, s.value_directory_id, s.created_at
FROM secrets s`

const configSpaceExpr = `(SELECT sp.space_id FROM config_spaces sp WHERE sp.config_id = c.id ORDER BY sp.id DESC LIMIT 1)`

const configRowSelect = `SELECT c.id, c.name, ` + configSpaceExpr + `, c.value_directory_id, c.created_at
FROM configs c`

func (q *Queries) ListSecretRows(ctx context.Context) ([]SecretRow, error) {
	rows, err := q.db.QueryContext(ctx, secretRowSelect+` WHERE s.deleted_at = 0 ORDER BY s.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SecretRow{}
	for rows.Next() {
		var r SecretRow
		if err := rows.Scan(&r.ID, &r.Name, &r.SpaceID, &r.ValueDirectoryID, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (q *Queries) GetSecretRowByID(ctx context.Context, id int64) (SecretRow, error) {
	var r SecretRow
	err := q.db.QueryRowContext(ctx, secretRowSelect+` WHERE s.id = ? AND s.deleted_at = 0`, id).
		Scan(&r.ID, &r.Name, &r.SpaceID, &r.ValueDirectoryID, &r.CreatedAt)
	return r, err
}

type GetSecretInDirectoryByNameParams struct {
	SpaceID          int64
	ValueDirectoryID int64
	Name             string
}

func (q *Queries) GetSecretInDirectoryByName(ctx context.Context, arg GetSecretInDirectoryByNameParams) (SecretRow, error) {
	var r SecretRow
	err := q.db.QueryRowContext(ctx, secretRowSelect+
		` WHERE s.deleted_at = 0 AND s.value_directory_id = ? AND s.name = ? AND `+secretSpaceExpr+` = ?`,
		arg.ValueDirectoryID, arg.Name, arg.SpaceID).
		Scan(&r.ID, &r.Name, &r.SpaceID, &r.ValueDirectoryID, &r.CreatedAt)
	return r, err
}

type CountSecretSiblingsWithNameParams struct {
	SpaceID          int64
	ValueDirectoryID int64
	Name             string
	ID               int64
}

func (q *Queries) CountSecretSiblingsWithName(ctx context.Context, arg CountSecretSiblingsWithNameParams) (int64, error) {
	var n int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM secrets s
WHERE s.deleted_at = 0 AND s.value_directory_id = ? AND s.name = ? AND s.id != ? AND `+secretSpaceExpr+` = ?`,
		arg.ValueDirectoryID, arg.Name, arg.ID, arg.SpaceID).Scan(&n)
	return n, err
}

func (q *Queries) ListConfigRows(ctx context.Context) ([]ConfigRow, error) {
	rows, err := q.db.QueryContext(ctx, configRowSelect+` WHERE c.deleted_at = 0 ORDER BY c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConfigRow{}
	for rows.Next() {
		var r ConfigRow
		if err := rows.Scan(&r.ID, &r.Name, &r.SpaceID, &r.ValueDirectoryID, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (q *Queries) GetConfigRowByID(ctx context.Context, id int64) (ConfigRow, error) {
	var r ConfigRow
	err := q.db.QueryRowContext(ctx, configRowSelect+` WHERE c.id = ? AND c.deleted_at = 0`, id).
		Scan(&r.ID, &r.Name, &r.SpaceID, &r.ValueDirectoryID, &r.CreatedAt)
	return r, err
}

type GetConfigInDirectoryByNameParams struct {
	SpaceID          int64
	ValueDirectoryID int64
	Name             string
}

func (q *Queries) GetConfigInDirectoryByName(ctx context.Context, arg GetConfigInDirectoryByNameParams) (ConfigRow, error) {
	var r ConfigRow
	err := q.db.QueryRowContext(ctx, configRowSelect+
		` WHERE c.deleted_at = 0 AND c.value_directory_id = ? AND c.name = ? AND `+configSpaceExpr+` = ?`,
		arg.ValueDirectoryID, arg.Name, arg.SpaceID).
		Scan(&r.ID, &r.Name, &r.SpaceID, &r.ValueDirectoryID, &r.CreatedAt)
	return r, err
}

type CountConfigSiblingsWithNameParams struct {
	SpaceID          int64
	ValueDirectoryID int64
	Name             string
	ID               int64
}

func (q *Queries) CountConfigSiblingsWithName(ctx context.Context, arg CountConfigSiblingsWithNameParams) (int64, error) {
	var n int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM configs c
WHERE c.deleted_at = 0 AND c.value_directory_id = ? AND c.name = ? AND c.id != ? AND `+configSpaceExpr+` = ?`,
		arg.ValueDirectoryID, arg.Name, arg.ID, arg.SpaceID).Scan(&n)
	return n, err
}

// ConfigVersionJoinedRow is one config version row joined with its identity.
// Pinned version reads stay resolvable for soft-deleted configs.
type ConfigVersionJoinedRow struct {
	ID        int64
	ConfigID  int64
	Version   int64
	Value     string
	CreatedAt int64
	Author    int64
	Name      string
	SpaceID   int64
}

func (q *Queries) GetConfigVersionByID(ctx context.Context, id int64) (ConfigVersionJoinedRow, error) {
	var r ConfigVersionJoinedRow
	err := q.db.QueryRowContext(ctx, `SELECT v.id, v.config_id, v.version, v.value, v.created_at, v.author, c.name, `+configSpaceExpr+`
FROM config_versions v
JOIN configs c ON c.id = v.config_id
WHERE v.id = ?`, id).
		Scan(&r.ID, &r.ConfigID, &r.Version, &r.Value, &r.CreatedAt, &r.Author, &r.Name, &r.SpaceID)
	return r, err
}

// SecretVersionRecordRow is one sealed secret version joined with its
// identity. Soft-deleted secrets are excluded — deletion drops them from the
// Manager cache, and the startup load must not resurrect them.
type SecretVersionRecordRow struct {
	ID         int64
	SecretID   int64
	Version    int64
	SmkVersion int64
	Ciphertext []byte
	Nonce      []byte
	CreatedAt  int64
	Author     int64
	Name       string
	SpaceID    int64
}

func (q *Queries) ListSecretVersionRecords(ctx context.Context) ([]SecretVersionRecordRow, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT v.id, v.secret_id, v.version, v.smk_version, v.ciphertext, v.nonce, v.created_at, v.author,
       s.name, `+secretSpaceExpr+`
FROM secret_versions v
JOIN secrets s ON s.id = v.secret_id
WHERE s.deleted_at = 0
ORDER BY v.secret_id, v.version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SecretVersionRecordRow{}
	for rows.Next() {
		var r SecretVersionRecordRow
		if err := rows.Scan(&r.ID, &r.SecretID, &r.Version, &r.SmkVersion, &r.Ciphertext, &r.Nonce,
			&r.CreatedAt, &r.Author, &r.Name, &r.SpaceID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
