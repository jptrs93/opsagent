package pq

import (
	"context"
)

// Hand-written secret/config reads and writes. Each entity's state lives
// entirely in its event log: identity facets (name, directory, space) are
// denormalised onto every row, the highest-version row is the current state
// (event_type is the deletion truth), and a row with a non-NULL value payload
// is a pinnable value version. Pinned value reads deliberately do not filter
// deleted identities; current-state reads do.

// SecretRow is a live secret identity with its current facets.
type SecretRow struct {
	ID               int64
	Name             string
	SpaceID          int64
	ValueDirectoryID int64
	CreatedAt        int64
}

// ConfigRow is a live config identity with its current facets.
type ConfigRow struct {
	ID               int64
	Name             string
	SpaceID          int64
	ValueDirectoryID int64
	CreatedAt        int64
}

type SecretEventMeta struct {
	ID               int64
	GlobalSeq        int64
	EventTime        int64
	CreatedTime      int64
	Author           int64
	SecretID         int64
	Version          int64
	ValueVersion     int64
	SpaceVersion     int64
	Name             string
	ValueDirectoryID int64
	SpaceID          int64
	HasValue         bool
	EventType        int64
}

const secretEventMetaColumns = `e.id, e.global_seq, e.event_time, e.created_time, e.author,
	e.secret_id, e.version, e.value_version, e.space_version,
	e.name, e.value_directory_id, e.space_id, e.ciphertext IS NOT NULL, e.event_type`

func scanSecretEventMeta(scan func(dest ...any) error) (SecretEventMeta, error) {
	var e SecretEventMeta
	err := scan(&e.ID, &e.GlobalSeq, &e.EventTime, &e.CreatedTime, &e.Author,
		&e.SecretID, &e.Version, &e.ValueVersion, &e.SpaceVersion,
		&e.Name, &e.ValueDirectoryID, &e.SpaceID, &e.HasValue, &e.EventType)
	return e, err
}

const secretLatestJoin = `JOIN (SELECT secret_id, MAX(version) AS version
	      FROM secret_event_log GROUP BY secret_id) latest
	  ON latest.secret_id = e.secret_id AND latest.version = e.version`

const configLatestJoin = `JOIN (SELECT config_id, MAX(version) AS version
	      FROM config_event_log GROUP BY config_id) latest
	  ON latest.config_id = e.config_id AND latest.version = e.version`

const secretRowSelect = `SELECT e.secret_id, e.name, e.space_id, e.value_directory_id, e.created_time
	FROM secret_event_log e
	` + secretLatestJoin + `
	WHERE e.event_type != 3`

const configRowSelect = `SELECT e.config_id, e.name, e.space_id, e.value_directory_id, e.created_time
	FROM config_event_log e
	` + configLatestJoin + `
	WHERE e.event_type != 3`

func (q *Queries) GetLatestSecretEventMeta(ctx context.Context, secretID int64) (SecretEventMeta, error) {
	return scanSecretEventMeta(q.db.QueryRowContext(ctx, `
		SELECT `+secretEventMetaColumns+`
		FROM secret_event_log e
		WHERE e.secret_id = ?
		ORDER BY e.version DESC LIMIT 1`, secretID).Scan)
}

func (q *Queries) InsertConfigEvent(ctx context.Context, e ConfigEvent) (int64, error) {
	var id int64
	err := q.db.QueryRowContext(ctx, `
		INSERT INTO config_event_log (
			global_seq, event_time, created_time, author, config_id, version,
			value_version, space_version, name, value_directory_id, space_id,
			value, event_type
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		e.GlobalSeq, e.EventTime, e.CreatedTime, e.Author, e.ConfigID, e.Version,
		e.ValueVersion, e.SpaceVersion, e.Name, e.ValueDirectoryID, e.SpaceID,
		e.Value, e.EventType).Scan(&id)
	return id, err
}

func (q *Queries) InsertSecretEvent(ctx context.Context, e SecretEvent) (int64, error) {
	var id int64
	err := q.db.QueryRowContext(ctx, `
		INSERT INTO secret_event_log (
			global_seq, event_time, created_time, author, secret_id, version,
			value_version, space_version, name, value_directory_id, space_id,
			smk_version, ciphertext, nonce, event_type
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		e.GlobalSeq, e.EventTime, e.CreatedTime, e.Author, e.SecretID, e.Version,
		e.ValueVersion, e.SpaceVersion, e.Name, e.ValueDirectoryID, e.SpaceID,
		e.SmkVersion, e.Ciphertext, e.Nonce, e.EventType).Scan(&id)
	return id, err
}

func (q *Queries) listSecretEventMetas(ctx context.Context, query string, args ...any) ([]SecretEventMeta, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SecretEventMeta{}
	for rows.Next() {
		e, err := scanSecretEventMeta(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (q *Queries) ListSecretEventMetas(ctx context.Context, secretID int64) ([]SecretEventMeta, error) {
	return q.listSecretEventMetas(ctx, `
		SELECT `+secretEventMetaColumns+`
		FROM secret_event_log e
		WHERE e.secret_id = ?
		ORDER BY e.version`, secretID)
}

func (q *Queries) ListAllSecretEventMetas(ctx context.Context) ([]SecretEventMeta, error) {
	return q.listSecretEventMetas(ctx, `
		SELECT `+secretEventMetaColumns+`
		FROM secret_event_log e
		ORDER BY e.secret_id, e.version`)
}

func (q *Queries) ListSecretRows(ctx context.Context) ([]SecretRow, error) {
	rows, err := q.db.QueryContext(ctx, secretRowSelect+` ORDER BY e.name`)
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
	err := q.db.QueryRowContext(ctx, secretRowSelect+` AND e.secret_id = ?`, id).
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
		` AND e.value_directory_id = ? AND e.name = ? AND e.space_id = ?`,
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
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+secretRowSelect+
		` AND e.value_directory_id = ? AND e.name = ? AND e.secret_id != ? AND e.space_id = ?)`,
		arg.ValueDirectoryID, arg.Name, arg.ID, arg.SpaceID).Scan(&n)
	return n, err
}

func (q *Queries) CountSecretsInDirectory(ctx context.Context, directoryID int64) (int64, error) {
	var n int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+secretRowSelect+
		` AND e.value_directory_id = ?)`, directoryID).Scan(&n)
	return n, err
}

func (q *Queries) ListConfigRows(ctx context.Context) ([]ConfigRow, error) {
	rows, err := q.db.QueryContext(ctx, configRowSelect+` ORDER BY e.name`)
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
	err := q.db.QueryRowContext(ctx, configRowSelect+` AND e.config_id = ?`, id).
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
		` AND e.value_directory_id = ? AND e.name = ? AND e.space_id = ?`,
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
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+configRowSelect+
		` AND e.value_directory_id = ? AND e.name = ? AND e.config_id != ? AND e.space_id = ?)`,
		arg.ValueDirectoryID, arg.Name, arg.ID, arg.SpaceID).Scan(&n)
	return n, err
}

func (q *Queries) CountConfigsInDirectory(ctx context.Context, directoryID int64) (int64, error) {
	var n int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+configRowSelect+
		` AND e.value_directory_id = ?)`, directoryID).Scan(&n)
	return n, err
}

// ConfigVersionJoinedRow is one pinned value version row overlaid with the
// identity's current name and space. Pinned version reads stay resolvable for
// soft-deleted configs.
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
	err := q.db.QueryRowContext(ctx, `
SELECT v.id, v.config_id, v.value_version, v.value, v.event_time, v.author, c.name, c.space_id
FROM config_event_log v
JOIN config_event_log c
  ON c.config_id = v.config_id
 AND c.version = (SELECT MAX(version) FROM config_event_log WHERE config_id = v.config_id)
WHERE v.id = ? AND v.value IS NOT NULL`, id).
		Scan(&r.ID, &r.ConfigID, &r.Version, &r.Value, &r.CreatedAt, &r.Author, &r.Name, &r.SpaceID)
	return r, err
}

// SecretVersionRecordRow is one sealed value version overlaid with the
// identity's current name and space. Soft-deleted secrets are excluded —
// deletion drops them from the Manager cache, and the startup load must not
// resurrect them.
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
	rows, err := q.db.QueryContext(ctx, `
SELECT v.id, v.secret_id, v.value_version, v.smk_version, v.ciphertext, v.nonce, v.event_time, v.author,
       l.name, l.space_id
FROM secret_event_log v
JOIN (`+secretRowSelect+`) l ON l.secret_id = v.secret_id
WHERE v.ciphertext IS NOT NULL
ORDER BY v.secret_id, v.value_version`)
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
