package pq

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// AssetRow is a live asset identity: its placement, key, newest content
// version, and the envelope of its last write.
type AssetRow struct {
	ID           int64
	SpaceID      int64
	DirectoryID  int64
	Key          string
	ValueVersion int64
	Seq          int64
	EventTime    int64
	Author       int64
	CreatedTime  int64
}

// AssetVersionRow is one content version of a live asset with the envelope
// of the write that produced it.
type AssetVersionRow struct {
	AssetID      int64
	ValueVersion int64
	Seq          int64
	EventTime    int64
	Author       int64
	Sha256       string
	SizeBytes    int64
	StorageKey   string
}

// AssetStoreRef carries the placement of the content a version row names
// through its storage key.
type AssetStoreRef struct {
	ID           string
	LocalStatus  int64
	RemoteStatus int64
}

// AssetVersionJoined is one content version with its owning identity and the
// placement of its content.
type AssetVersionJoined struct {
	Asset   AssetRow
	Version AssetVersionRow
	Store   AssetStoreRef
}

type AssetKey struct {
	SpaceID  int64
	ParentID int64
	Key      string
	Kind     apigen.CoreEntityType
	ID       int64
}

const assetColumns = `id, space_id, directory_id, key, value_version, seq, event_time, author, created_time`
const assetVersionColumns = `asset_id, value_version, seq, event_time, author, sha256, size_bytes, storage_key`

func scanAssetRow(row scanner) (AssetRow, error) {
	var r AssetRow
	err := row.Scan(&r.ID, &r.SpaceID, &r.DirectoryID, &r.Key, &r.ValueVersion, &r.Seq, &r.EventTime, &r.Author, &r.CreatedTime)
	return r, err
}

func scanAssetVersionRow(row scanner) (AssetVersionRow, error) {
	var r AssetVersionRow
	err := row.Scan(&r.AssetID, &r.ValueVersion, &r.Seq, &r.EventTime, &r.Author, &r.Sha256, &r.SizeBytes, &r.StorageKey)
	return r, err
}

func (q *Queries) GetAssetRowByID(ctx context.Context, id int64) (AssetRow, error) {
	return scanAssetRow(q.db.QueryRowContext(ctx, `SELECT `+assetColumns+` FROM assets WHERE id = ?`, id))
}

func (q *Queries) ListAssetRows(ctx context.Context) ([]AssetRow, error) {
	return listRows(ctx, q, scanAssetRow, `SELECT `+assetColumns+` FROM assets ORDER BY key, id`)
}

func (q *Queries) GetAssetVersion(ctx context.Context, ref apigen.ValueRef) (AssetVersionRow, error) {
	return scanAssetVersionRow(q.db.QueryRowContext(ctx, `SELECT `+assetVersionColumns+` FROM asset_versions WHERE asset_id = ? AND value_version = ?`, ref.ID, ref.Version))
}

func (q *Queries) ListAssetVersions(ctx context.Context) ([]AssetVersionRow, error) {
	return listRows(ctx, q, scanAssetVersionRow, `SELECT `+assetVersionColumns+` FROM asset_versions ORDER BY asset_id, value_version`)
}

// GetAssetVersionJoined resolves a pinned asset value with its identity and
// the placement of its content.
func (q *Queries) GetAssetVersionJoined(ctx context.Context, ref apigen.ValueRef) (AssetVersionJoined, error) {
	var j AssetVersionJoined
	err := q.db.QueryRowContext(ctx, `SELECT v.asset_id, v.value_version, v.seq, v.event_time, v.author, v.sha256, v.size_bytes, v.storage_key, s.id, s.local_status, s.remote_status
FROM asset_versions v JOIN asset_store s ON s.id = v.storage_key
WHERE v.asset_id = ? AND v.value_version = ?`, ref.ID, ref.Version).Scan(
		&j.Version.AssetID, &j.Version.ValueVersion, &j.Version.Seq, &j.Version.EventTime, &j.Version.Author, &j.Version.Sha256, &j.Version.SizeBytes, &j.Version.StorageKey,
		&j.Store.ID, &j.Store.LocalStatus, &j.Store.RemoteStatus)
	if err != nil {
		return j, err
	}
	j.Asset, err = q.GetAssetRowByID(ctx, int64(ref.ID))
	return j, err
}

func (q *Queries) CountAssetVersionsBySha(ctx context.Context, sha256 string) (int64, error) {
	var n int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM asset_versions WHERE sha256 = ?`, sha256).Scan(&n)
	return n, err
}

func (q *Queries) LookupAssetKey(ctx context.Context, spaceID, parentID int64, key string) (AssetKey, error) {
	k := AssetKey{SpaceID: spaceID, ParentID: parentID, Key: key}
	var kind int64
	err := q.db.QueryRowContext(ctx, `SELECT kind, id FROM asset_keys WHERE space_id = ? AND parent_id = ? AND key = ?`, spaceID, parentID, key).Scan(&kind, &k.ID)
	k.Kind = apigen.CoreEntityType(kind)
	return k, err
}

func (q *Queries) AssetKeyTaken(ctx context.Context, spaceID, parentID int64, key string, self apigen.CoreEntityType, selfID int64) (bool, error) {
	k, err := q.LookupAssetKey(ctx, spaceID, parentID, key)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return k.Kind != self || k.ID != selfID, nil
}

func (q *Queries) CountAssetKeysUnder(ctx context.Context, directoryID int64) (int64, error) {
	var n int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM asset_keys WHERE parent_id = ?`, directoryID).Scan(&n)
	return n, err
}

func AssetEntity(a AssetRow, v AssetVersionRow) apigen.Asset {
	return apigen.Asset{
		ID: int32(a.ID), Fs: &apigen.AssetFs{Key: a.Key, DirectoryID: int32(a.DirectoryID)}, SpaceID: int32(a.SpaceID),
		Sha256: v.Sha256, SizeBytes: v.SizeBytes, StorageKey: v.StorageKey,
	}
}

// AssetEventOf is the API view of an asset at one content version: the
// identity's envelope when that is its newest version, the version's
// otherwise.
func AssetEventOf(a AssetRow, v AssetVersionRow) *AssetEvent {
	seq, author, eventTime := a.Seq, a.Author, a.EventTime
	if v.ValueVersion != a.ValueVersion {
		seq, author, eventTime = v.Seq, v.Author, v.EventTime
	}
	return &AssetEvent{
		AssetID: int32(a.ID), Seq: seq, Author: int32(author), CreatedTime: a.CreatedTime, EventTime: eventTime, ValueVersion: int32(v.ValueVersion),
		Value: AssetEntity(a, v),
	}
}

func (q *Queries) GetAssetEvent(ctx context.Context, id int64) (*AssetEvent, error) {
	a, err := q.GetAssetRowByID(ctx, id)
	if err != nil {
		return nil, err
	}
	v, err := q.GetAssetVersion(ctx, apigen.ValueRef{ID: int32(a.ID), Version: int32(a.ValueVersion)})
	if err != nil {
		return nil, err
	}
	return AssetEventOf(a, v), nil
}

func (q *Queries) ListAssetEvents(ctx context.Context) ([]*AssetEvent, error) {
	assets, err := q.ListAssetRows(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*AssetEvent, 0, len(assets))
	for _, a := range assets {
		v, err := q.GetAssetVersion(ctx, apigen.ValueRef{ID: int32(a.ID), Version: int32(a.ValueVersion)})
		if err != nil {
			return nil, err
		}
		out = append(out, AssetEventOf(a, v))
	}
	return out, nil
}
