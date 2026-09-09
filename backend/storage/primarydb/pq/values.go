package pq

import (
	"context"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// Hand-written secret/config reads and writes. Each entity's state lives
// entirely in its event log: every facet (name, directory, space, value
// payload) is denormalised onto every row, the highest-version row is the
// current state (event_type is the deletion truth), and a value_changed row
// is a pinnable value version. Pinned value reads deliberately do not filter
// deleted identities; current-state reads do.

// SecretEvent is the row written by the handwritten secret queries.
type SecretEvent struct {
	ID               int64
	GlobalSeq        int64
	EventTime        int64
	CreatedTime      int64
	Author           int64
	SecretID         int64
	Version          int64
	ValueVersion     int64
	SpaceVersion     int64
	ValueChanged     int64
	SpaceChanged     int64
	Name             string
	ValueDirectoryID int64
	SpaceID          int64
	SmkVersion       int64
	Ciphertext       []byte
	Nonce            []byte
	EventType        int64
}

// SecretRow is a live secret identity with its current facets.
type SecretRow struct {
	ID               int64
	Name             string
	SpaceID          int64
	ValueDirectoryID int64
	CreatedAt        int64
}

// ConfigRow carries a config's current identity facets.
type ConfigRow struct {
	ID               int64
	Name             string
	SpaceID          int64
	ValueDirectoryID int64
	CreatedAt        int64
}

const secretEventColumns = `e.id,e.global_seq,e.event_time,e.created_time,e.author,e.secret_id,e.version,e.value_version,e.space_version,e.name,e.value_directory_id,e.space_id,e.event_type`

func scanSecretEvent(scan func(...any) error) (*apigen.SecretEvent, error) {
	e := &apigen.SecretEvent{Value: apigen.Secret{Fs: &apigen.SecretFs{}}}
	if err := scan(&e.EventID, &e.Seq, &e.EventTime, &e.CreatedTime, &e.Author, &e.SecretID, &e.Version, &e.ValueVersion, &e.SpaceVersion, &e.Value.Fs.Name, &e.Value.Fs.DirectoryID, &e.Value.SpaceID, &e.EventType); err != nil {
		return nil, err
	}
	return e, nil
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

func (q *Queries) GetLatestSecretEvent(ctx context.Context, secretID int64) (*apigen.SecretEvent, error) {
	return scanSecretEvent(q.db.QueryRowContext(ctx, `
		SELECT `+secretEventColumns+`
		FROM secret_event_log e
		WHERE e.secret_id = ?
		ORDER BY e.version DESC LIMIT 1`, secretID).Scan)
}

func (q *Queries) InsertConfigEvent(ctx context.Context, e *apigen.ConfigEvent) error {
	row := q.db.QueryRowContext(ctx, `WITH previous AS (
  SELECT value_version, space_version FROM config_event_log
  WHERE config_id = ? ORDER BY version DESC LIMIT 1
)
 INSERT INTO config_event_log (
  id, global_seq, event_time, created_time, author,
  config_id,version,value_version, space_version,
  name,value_directory_id,space_id,value,event_type, value_changed, space_changed
) VALUES (NULLIF(?, 0),?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
 ? > COALESCE((SELECT value_version FROM previous),0), ? > COALESCE((SELECT space_version FROM previous),0))
RETURNING id, global_seq, event_time, created_time, author,
  config_id,version,value_version, space_version,
  name,value_directory_id,space_id,value,event_type`,
		e.ConfigID, e.EventID, e.Seq, e.EventTime, e.CreatedTime, e.Author, e.ConfigID, e.Version, e.ValueVersion, e.SpaceVersion, e.Value.Fs.Name, e.Value.Fs.DirectoryID, e.Value.SpaceID, e.Value.Value, e.EventType, e.ValueVersion, e.SpaceVersion)
	written, err := scanConfigEvent(row)
	if err != nil {
		return err
	}
	*e = *written
	return nil
}

func (q *Queries) InsertSecretEvent(ctx context.Context, e SecretEvent) (*apigen.SecretEvent, error) {
	row := q.db.QueryRowContext(ctx, `
		INSERT INTO secret_event_log (
			global_seq, event_time, created_time, author, secret_id, version,
			value_version, space_version, value_changed, space_changed,
			name, value_directory_id, space_id,
			smk_version, ciphertext, nonce, event_type
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id,global_seq,event_time,created_time,author,secret_id,version,value_version,space_version,name,value_directory_id,space_id,event_type`,
		e.GlobalSeq, e.EventTime, e.CreatedTime, e.Author, e.SecretID, e.Version,
		e.ValueVersion, e.SpaceVersion, e.ValueChanged, e.SpaceChanged,
		e.Name, e.ValueDirectoryID, e.SpaceID,
		e.SmkVersion, e.Ciphertext, e.Nonce, e.EventType)
	return scanSecretEvent(row.Scan)
}

// InsertSecretCarryEvent appends a secret event that does not write a value:
// the sealed payload (smk_version, ciphertext, nonce) is copied forward from
// the previous row in SQL so the ciphertext never passes through Go. The
// payload fields of e are ignored and value_changed is always 0.
func (q *Queries) InsertSecretCarryEvent(ctx context.Context, e SecretEvent) (*apigen.SecretEvent, error) {
	row := q.db.QueryRowContext(ctx, `
		INSERT INTO secret_event_log (
			global_seq, event_time, created_time, author, secret_id, version,
			value_version, space_version, value_changed, space_changed,
			name, value_directory_id, space_id,
			smk_version, ciphertext, nonce, event_type
		)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?,
		       p.smk_version, p.ciphertext, p.nonce, ?
		FROM secret_event_log p
		WHERE p.secret_id = ?
		ORDER BY p.version DESC LIMIT 1
		RETURNING id,global_seq,event_time,created_time,author,secret_id,version,value_version,space_version,name,value_directory_id,space_id,event_type`,
		e.GlobalSeq, e.EventTime, e.CreatedTime, e.Author, e.SecretID, e.Version,
		e.ValueVersion, e.SpaceVersion, e.SpaceChanged,
		e.Name, e.ValueDirectoryID, e.SpaceID, e.EventType,
		e.SecretID)
	return scanSecretEvent(row.Scan)
}

func (q *Queries) listSecretEvents(ctx context.Context, query string, args ...any) ([]*apigen.SecretEvent, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*apigen.SecretEvent{}
	for rows.Next() {
		e, err := scanSecretEvent(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (q *Queries) GetSecretEventByID(ctx context.Context, eventID int64) (*apigen.SecretEvent, error) {
	return scanSecretEvent(q.db.QueryRowContext(ctx, `SELECT `+secretEventColumns+` FROM secret_event_log e WHERE e.id = ?`, eventID).Scan)
}

func (q *Queries) ListLatestLiveSecretEvents(ctx context.Context) ([]*apigen.SecretEvent, error) {
	return q.listSecretEvents(ctx, `SELECT `+secretEventColumns+` FROM secret_event_log e
 WHERE e.version=(SELECT MAX(version) FROM secret_event_log WHERE secret_id=e.secret_id) AND e.event_type != 3 ORDER BY e.name,e.secret_id`)
}

func (q *Queries) ListAllSecretEvents(ctx context.Context) ([]*apigen.SecretEvent, error) {
	return q.listSecretEvents(ctx, `
		SELECT `+secretEventColumns+`
		FROM secret_event_log e
 WHERE e.secret_id IN (SELECT secret_id FROM secret_event_log current WHERE current.version=(SELECT MAX(version) FROM secret_event_log WHERE secret_id=current.secret_id) AND current.event_type!=3)
		ORDER BY e.secret_id, e.version`)
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
WHERE v.id = ? AND v.value_changed != 0`, id).
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
WHERE v.value_changed != 0
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

func (q *Queries) ListSecretEventsAtSeq(ctx context.Context, seq int64) ([]*apigen.SecretEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+secretEventColumns+` FROM secret_event_log e WHERE global_seq = ? ORDER BY id`, seq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*apigen.SecretEvent
	for rows.Next() {
		e, err := scanSecretEvent(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetSecretValueEventByID resolves a pinnable content event without loading sealed bytes.
func (q *Queries) GetSecretValueEventByID(ctx context.Context, eventID int64) (*apigen.SecretEvent, error) {
	return scanSecretEvent(q.db.QueryRowContext(ctx, `SELECT `+secretEventColumns+` FROM secret_event_log e WHERE e.id=? AND e.value_changed != 0`, eventID).Scan)
}
