package pq

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// AssetRow is a live asset identity: its placement, key, newest content
// version, and the envelope of its last write. DirectoryID 0 is the space
// root.
type AssetRow struct {
	ID           uint64
	SpaceID      uint64
	DirectoryID  uint64
	Key          string
	ValueVersion uint32
	Seq          int64
	EventTime    int64
	Author       int64
	CreatedTime  int64
}

// AssetVersionRow is one content version of a live asset with the envelope
// of the write that produced it. Sha256 is the hex digest, the form the
// store rows and S3 names use.
type AssetVersionRow struct {
	AssetID      uint64
	ValueVersion uint32
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
	SpaceID  uint64
	ParentID uint64
	Key      string
	Kind     apigen.CoreEntityType
	ID       uint64
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

func (q *Queries) GetAssetRowByID(ctx context.Context, id uint64) (AssetRow, error) {
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
	j.Asset, err = q.GetAssetRowByID(ctx, ref.ID)
	return j, err
}

func (q *Queries) CountAssetVersionsBySha(ctx context.Context, sha256 string) (int64, error) {
	var n int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM asset_versions WHERE sha256 = ?`, sha256).Scan(&n)
	return n, err
}

func (q *Queries) LookupAssetKey(ctx context.Context, spaceID, parentID uint64, key string) (AssetKey, error) {
	k := AssetKey{SpaceID: spaceID, ParentID: parentID, Key: key}
	var kind int64
	err := q.db.QueryRowContext(ctx, `SELECT kind, id FROM asset_keys WHERE space_id = ? AND parent_id = ? AND key = ?`, spaceID, parentID, key).Scan(&kind, &k.ID)
	k.Kind = apigen.CoreEntityType(kind)
	return k, err
}

func (q *Queries) AssetKeyTaken(ctx context.Context, spaceID, parentID uint64, key string, self apigen.CoreEntityType, selfID uint64) (bool, error) {
	k, err := q.LookupAssetKey(ctx, spaceID, parentID, key)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return k.Kind != self || k.ID != selfID, nil
}

func (q *Queries) CountAssetKeysUnder(ctx context.Context, directoryID uint64) (int64, error) {
	var n int64
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM asset_keys WHERE parent_id = ?`, directoryID).Scan(&n)
	return n, err
}

// Sha256Bytes is the raw digest of a hex sha256 column; a malformed column
// decodes to nil.
func Sha256Bytes(hexDigest string) []byte {
	b, err := hex.DecodeString(hexDigest)
	if err != nil {
		return nil
	}
	return b
}

func AssetEntity(a AssetRow, v AssetVersionRow) apigen.Asset {
	return apigen.Asset{
		ID: a.ID, Fs: apigen.AssetFs{Key: a.Key, DirectoryID: directoryRef(a.DirectoryID)}, SpaceID: a.SpaceID,
		Sha256: Sha256Bytes(v.Sha256), SizeBytes: uint64(v.SizeBytes), StorageKey: v.StorageKey,
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
		AssetID: a.ID, Seq: seq, Author: author, CreatedTime: a.CreatedTime, EventTime: eventTime, ValueVersion: v.ValueVersion,
		Value: AssetEntity(a, v),
	}
}

func (q *Queries) GetAssetEvent(ctx context.Context, id uint64) (*AssetEvent, error) {
	a, err := q.GetAssetRowByID(ctx, id)
	if err != nil {
		return nil, err
	}
	v, err := q.GetAssetVersion(ctx, apigen.ValueRef{ID: a.ID, Version: a.ValueVersion})
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
		v, err := q.GetAssetVersion(ctx, apigen.ValueRef{ID: a.ID, Version: a.ValueVersion})
		if err != nil {
			return nil, err
		}
		out = append(out, AssetEventOf(a, v))
	}
	return out, nil
}
