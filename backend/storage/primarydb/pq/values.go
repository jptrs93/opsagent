package pq

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// SecretRow is a live secret identity: its placement, name, newest value
// version, and the envelope of its last write.
type SecretRow struct {
	ID           int64
	SpaceID      int64
	DirectoryID  int64
	Name         string
	ValueVersion int64
	Seq          int64
	EventTime    int64
	Author       int64
	CreatedTime  int64
}

// SecretVersionRow is one sealed value of a live secret with the envelope of
// the write that produced it.
type SecretVersionRow struct {
	SecretID     int64
	ValueVersion int64
	Seq          int64
	EventTime    int64
	Author       int64
	SmkVersion   int64
	Ciphertext   []byte
	Nonce        []byte
}

type ConfigRow struct {
	ID           int64
	SpaceID      int64
	DirectoryID  int64
	Name         string
	ValueVersion int64
	Seq          int64
	EventTime    int64
	Author       int64
	CreatedTime  int64
}

type ConfigVersionRow struct {
	ConfigID     int64
	ValueVersion int64
	Seq          int64
	EventTime    int64
	Author       int64
	Value        string
}

// ValueName is one entry of the values namespace: the path component of a
// directory, secret, or config under its parent.
type ValueName struct {
	SpaceID  int64
	ParentID int64
	Name     string
	Kind     apigen.CoreEntityType
	ID       int64
}

const secretColumns = `id, space_id, directory_id, name, value_version, seq, event_time, author, created_time`
const secretVersionColumns = `secret_id, value_version, seq, event_time, author, smk_version, ciphertext, nonce`
const configColumns = `id, space_id, directory_id, name, value_version, seq, event_time, author, created_time`
const configVersionColumns = `config_id, value_version, seq, event_time, author, value`

func scanSecretRow(row scanner) (SecretRow, error) {
	var r SecretRow
	err := row.Scan(&r.ID, &r.SpaceID, &r.DirectoryID, &r.Name, &r.ValueVersion, &r.Seq, &r.EventTime, &r.Author, &r.CreatedTime)
	return r, err
}

func scanSecretVersionRow(row scanner) (SecretVersionRow, error) {
	var r SecretVersionRow
	err := row.Scan(&r.SecretID, &r.ValueVersion, &r.Seq, &r.EventTime, &r.Author, &r.SmkVersion, &r.Ciphertext, &r.Nonce)
	return r, err
}

func scanConfigRow(row scanner) (ConfigRow, error) {
	var r ConfigRow
	err := row.Scan(&r.ID, &r.SpaceID, &r.DirectoryID, &r.Name, &r.ValueVersion, &r.Seq, &r.EventTime, &r.Author, &r.CreatedTime)
	return r, err
}

func scanConfigVersionRow(row scanner) (ConfigVersionRow, error) {
	var r ConfigVersionRow
	err := row.Scan(&r.ConfigID, &r.ValueVersion, &r.Seq, &r.EventTime, &r.Author, &r.Value)
	return r, err
}

func listRows[T any](ctx context.Context, q *Queries, scan func(scanner) (T, error), query string, args ...any) ([]T, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (q *Queries) GetSecretRowByID(ctx context.Context, id int64) (SecretRow, error) {
	return scanSecretRow(q.db.QueryRowContext(ctx, `SELECT `+secretColumns+` FROM secrets WHERE id = ?`, id))
}

func (q *Queries) ListSecretRows(ctx context.Context) ([]SecretRow, error) {
	return listRows(ctx, q, scanSecretRow, `SELECT `+secretColumns+` FROM secrets ORDER BY name, id`)
}

func (q *Queries) GetSecretVersion(ctx context.Context, ref apigen.ValueRef) (SecretVersionRow, error) {
	return scanSecretVersionRow(q.db.QueryRowContext(ctx, `SELECT `+secretVersionColumns+` FROM secret_versions WHERE secret_id = ? AND value_version = ?`, ref.ID, ref.Version))
}

func (q *Queries) ListSecretVersions(ctx context.Context) ([]SecretVersionRow, error) {
	return listRows(ctx, q, scanSecretVersionRow, `SELECT `+secretVersionColumns+` FROM secret_versions ORDER BY secret_id, value_version`)
}

// SecretVersionJoined is one sealed value with its owning identity.
type SecretVersionJoined struct {
	Secret  SecretRow
	Version SecretVersionRow
}

func (q *Queries) GetSecretVersionJoined(ctx context.Context, ref apigen.ValueRef) (SecretVersionJoined, error) {
	var j SecretVersionJoined
	var err error
	if j.Version, err = q.GetSecretVersion(ctx, ref); err != nil {
		return j, err
	}
	j.Secret, err = q.GetSecretRowByID(ctx, int64(ref.ID))
	return j, err
}

// ListSecretVersionJoined returns every sealed value of every live secret
// with its identity, ordered by secret then version.
func (q *Queries) ListSecretVersionJoined(ctx context.Context) ([]SecretVersionJoined, error) {
	secrets, err := q.ListSecretRows(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]SecretRow, len(secrets))
	for _, s := range secrets {
		byID[s.ID] = s
	}
	versions, err := q.ListSecretVersions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SecretVersionJoined, 0, len(versions))
	for _, v := range versions {
		out = append(out, SecretVersionJoined{Secret: byID[v.SecretID], Version: v})
	}
	return out, nil
}

func (q *Queries) GetConfigRowByID(ctx context.Context, id int64) (ConfigRow, error) {
	return scanConfigRow(q.db.QueryRowContext(ctx, `SELECT `+configColumns+` FROM configs WHERE id = ?`, id))
}

func (q *Queries) ListConfigRows(ctx context.Context) ([]ConfigRow, error) {
	return listRows(ctx, q, scanConfigRow, `SELECT `+configColumns+` FROM configs ORDER BY name, id`)
}

func (q *Queries) GetConfigVersion(ctx context.Context, ref apigen.ValueRef) (ConfigVersionRow, error) {
	return scanConfigVersionRow(q.db.QueryRowContext(ctx, `SELECT `+configVersionColumns+` FROM config_versions WHERE config_id = ? AND value_version = ?`, ref.ID, ref.Version))
}

func (q *Queries) ListConfigVersions(ctx context.Context) ([]ConfigVersionRow, error) {
	return listRows(ctx, q, scanConfigVersionRow, `SELECT `+configVersionColumns+` FROM config_versions ORDER BY config_id, value_version`)
}

type ConfigVersionJoined struct {
	Config  ConfigRow
	Version ConfigVersionRow
}

func (q *Queries) GetConfigVersionJoined(ctx context.Context, ref apigen.ValueRef) (ConfigVersionJoined, error) {
	var j ConfigVersionJoined
	var err error
	if j.Version, err = q.GetConfigVersion(ctx, ref); err != nil {
		return j, err
	}
	j.Config, err = q.GetConfigRowByID(ctx, int64(ref.ID))
	return j, err
}

// LookupValueName resolves one path component under a directory to the
// entity that holds it, or sql.ErrNoRows when the name is free.
func (q *Queries) LookupValueName(ctx context.Context, spaceID, parentID int64, name string) (ValueName, error) {
	n := ValueName{SpaceID: spaceID, ParentID: parentID, Name: name}
	var kind int64
	err := q.db.QueryRowContext(ctx, `SELECT kind, id FROM value_names WHERE space_id = ? AND parent_id = ? AND name = ?`, spaceID, parentID, name).Scan(&kind, &n.ID)
	n.Kind = apigen.CoreEntityType(kind)
	return n, err
}

// ValueNameTaken reports whether a name under a directory belongs to an
// entity other than the one given.
func (q *Queries) ValueNameTaken(ctx context.Context, spaceID, parentID int64, name string, self apigen.CoreEntityType, selfID int64) (bool, error) {
	n, err := q.LookupValueName(ctx, spaceID, parentID, name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return n.Kind != self || n.ID != selfID, nil
}

func (q *Queries) CountValueNamesUnder(ctx context.Context, directoryID int64) (int64, error) {
	var n int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM value_names WHERE parent_id = ?`, directoryID).Scan(&n)
	return n, err
}

// SecretEntity is the stream payload of one secret version under its
// current identity, sealed bytes included.
func SecretEntity(s SecretRow, v SecretVersionRow) apigen.Secret {
	return apigen.Secret{
		ID: int32(s.ID), Fs: &apigen.SecretFs{Name: s.Name, DirectoryID: int32(s.DirectoryID)}, SpaceID: int32(s.SpaceID),
		SmkVersion: v.SmkVersion, Ciphertext: v.Ciphertext, Nonce: v.Nonce,
	}
}

// SecretEventOf is the API view of a live secret: the identity's envelope and
// newest value version, never the sealed bytes.
func SecretEventOf(s SecretRow) *SecretEvent {
	return &SecretEvent{
		SecretID: int32(s.ID), Seq: s.Seq, Author: int32(s.Author), CreatedTime: s.CreatedTime, EventTime: s.EventTime, ValueVersion: int32(s.ValueVersion),
		Value: apigen.Secret{Fs: &apigen.SecretFs{Name: s.Name, DirectoryID: int32(s.DirectoryID)}, SpaceID: int32(s.SpaceID)},
	}
}

// SecretVersionEventOf is the API view of one value version: the version's
// envelope under the identity's current name, directory, and space.
func SecretVersionEventOf(j SecretVersionJoined) *SecretEvent {
	e := SecretEventOf(j.Secret)
	e.Seq, e.Author, e.EventTime, e.ValueVersion = j.Version.Seq, int32(j.Version.Author), j.Version.EventTime, int32(j.Version.ValueVersion)
	return e
}

func ConfigEntity(c ConfigRow, v ConfigVersionRow) apigen.Config {
	return apigen.Config{
		ID: int32(c.ID), Fs: &apigen.ConfigFs{Name: c.Name, DirectoryID: int32(c.DirectoryID)}, SpaceID: int32(c.SpaceID),
		Value: v.Value,
	}
}

func ConfigEventOf(j ConfigVersionJoined) *ConfigEvent {
	c, v := j.Config, j.Version
	seq, author, eventTime := c.Seq, c.Author, c.EventTime
	if v.ValueVersion != c.ValueVersion {
		seq, author, eventTime = v.Seq, v.Author, v.EventTime
	}
	return &ConfigEvent{
		ConfigID: int32(c.ID), Seq: seq, Author: int32(author), CreatedTime: c.CreatedTime, EventTime: eventTime, ValueVersion: int32(v.ValueVersion),
		Value: ConfigEntity(c, v),
	}
}

// GetConfigEvent is the API view of a live config at its newest version.
func (q *Queries) GetConfigEvent(ctx context.Context, id int64) (*ConfigEvent, error) {
	c, err := q.GetConfigRowByID(ctx, id)
	if err != nil {
		return nil, err
	}
	v, err := q.GetConfigVersion(ctx, apigen.ValueRef{ID: int32(c.ID), Version: int32(c.ValueVersion)})
	if err != nil {
		return nil, err
	}
	return ConfigEventOf(ConfigVersionJoined{Config: c, Version: v}), nil
}

func (q *Queries) ListConfigEvents(ctx context.Context) ([]*ConfigEvent, error) {
	configs, err := q.ListConfigRows(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*ConfigEvent, 0, len(configs))
	for _, c := range configs {
		v, err := q.GetConfigVersion(ctx, apigen.ValueRef{ID: int32(c.ID), Version: int32(c.ValueVersion)})
		if err != nil {
			return nil, err
		}
		out = append(out, ConfigEventOf(ConfigVersionJoined{Config: c, Version: v}))
	}
	return out, nil
}
