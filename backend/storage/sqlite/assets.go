package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/apigen"
)

var (
	ErrAssetNotFound      = errors.New("asset not found")
	ErrAssetAlreadyExists = errors.New("asset already exists")
)

func assetRowToProto(r Asset) *apigen.Asset {
	return &apigen.Asset{
		ID:        int32(r.ID),
		Key:       r.Key,
		SpaceID:   int32(r.SpaceID),
		CreatedAt: time.UnixMilli(r.CreatedAt),
		Version:   int32(r.Version),
		Format:    r.Format,
		Location:  r.Location,
		SizeBytes: int32(r.SizeBytes),
		Blob:      r.Blob,
	}
}

func assetRowToMeta(r ListLatestAssetsRow) *apigen.AssetMeta {
	return &apigen.AssetMeta{
		ID:        int32(r.ID),
		Key:       r.Key,
		SpaceID:   int32(r.SpaceID),
		CreatedAt: time.UnixMilli(r.CreatedAt),
		Version:   int32(r.Version),
		Format:    r.Format,
		Location:  r.Location,
		SizeBytes: int32(r.SizeBytes),
	}
}

func assetProtoToMeta(a *apigen.Asset) apigen.AssetMeta {
	if a == nil {
		return apigen.AssetMeta{}
	}
	return apigen.AssetMeta{
		ID:        a.ID,
		Key:       a.Key,
		SpaceID:   a.SpaceID,
		CreatedAt: a.CreatedAt,
		Version:   a.Version,
		Format:    a.Format,
		Location:  a.Location,
		SizeBytes: a.SizeBytes,
	}
}

func (s *PrimaryStorage) ListAssets() []*apigen.AssetMeta {
	rows, err := s.q.ListLatestAssets(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListLatestAssets: %v", err))
	}
	out := make([]*apigen.AssetMeta, 0, len(rows))
	for _, r := range rows {
		out = append(out, assetRowToMeta(r))
	}
	return out
}

func (s *PrimaryStorage) ListAllAssetVersions() []*apigen.AssetMeta {
	return s.listAllAssetVersions(false)
}

func (s *PrimaryStorage) ListAllAssetVersionsIncludingPending() []*apigen.AssetMeta {
	return s.listAllAssetVersions(true)
}

func (s *PrimaryStorage) listAllAssetVersions(includePending bool) []*apigen.AssetMeta {
	where := "WHERE location NOT LIKE 'pending://%'"
	if includePending {
		where = ""
	}
	rows, err := s.db.QueryContext(context.Background(), `
SELECT id, key, space_id, created_at, version, format, location, size_bytes
FROM assets
`+where+`
ORDER BY key, version`)
	if err != nil {
		panic(fmt.Sprintf("ListAllAssetVersions: %v", err))
	}
	defer rows.Close()
	out := []*apigen.AssetMeta{}
	for rows.Next() {
		var r ListLatestAssetsRow
		if err := rows.Scan(&r.ID, &r.Key, &r.SpaceID, &r.CreatedAt, &r.Version, &r.Format, &r.Location, &r.SizeBytes); err != nil {
			panic(fmt.Sprintf("ListAllAssetVersions scan: %v", err))
		}
		out = append(out, assetRowToMeta(r))
	}
	if err := rows.Err(); err != nil {
		panic(fmt.Sprintf("ListAllAssetVersions rows: %v", err))
	}
	return out
}

func (s *PrimaryStorage) NotifyAssetUpdate(asset *apigen.Asset) {
	meta := assetProtoToMeta(asset)
	if meta.ID == 0 && meta.Key == "" {
		return
	}
	s.assetSubs.Notify(meta)
}

func (s *PrimaryStorage) NotifyAssetDeleted(asset *apigen.Asset) {
	meta := assetProtoToMeta(asset)
	if meta.ID == 0 && meta.Key == "" {
		return
	}
	meta.Deleted = true
	s.assetSubs.Notify(meta)
}

func (s *PrimaryStorage) SubscribeAssetUpdates() (*pubsubu.Sub[apigen.AssetMeta], func()) {
	sub := s.assetSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

func (s *PrimaryStorage) GetAsset(key string, version int32) (*apigen.Asset, bool) {
	var (
		r   Asset
		err error
	)
	if version > 0 {
		r, err = s.q.GetAssetVersion(context.Background(), GetAssetVersionParams{Key: key, Version: int64(version)})
	} else {
		r, err = s.q.GetLatestAsset(context.Background(), key)
	}
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAsset: %v", err))
	}
	return assetRowToProto(r), true
}

func (s *PrimaryStorage) GetAssetByID(assetID int32) (*apigen.Asset, bool) {
	return s.getAssetByID(assetID, false)
}

func (s *PrimaryStorage) GetAssetByIDIncludingPending(assetID int32) (*apigen.Asset, bool) {
	return s.getAssetByID(assetID, true)
}

func (s *PrimaryStorage) getAssetByID(assetID int32, includePending bool) (*apigen.Asset, bool) {
	pendingClause := "AND location NOT LIKE 'pending://%'"
	if includePending {
		pendingClause = ""
	}
	row := s.db.QueryRowContext(context.Background(), `
SELECT id, key, space_id, created_at, version, format, location, size_bytes, blob
FROM assets
WHERE id = ? `+pendingClause, assetID)
	var r Asset
	err := row.Scan(&r.ID, &r.Key, &r.SpaceID, &r.CreatedAt, &r.Version, &r.Format, &r.Location, &r.SizeBytes, &r.Blob)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetByID: %v", err))
	}
	return assetRowToProto(r), true
}

func (s *PrimaryStorage) ListAssetVersionsByKey(key string) []*apigen.Asset {
	rows, err := s.q.ListAssetVersionsByKey(context.Background(), key)
	if err != nil {
		panic(fmt.Sprintf("ListAssetVersionsByKey: %v", err))
	}
	out := make([]*apigen.Asset, 0, len(rows))
	for _, r := range rows {
		out = append(out, assetRowToProto(r))
	}
	return out
}

func (s *PrimaryStorage) ListAssetVersionsByKeyIncludingPending(key string) []*apigen.Asset {
	rows, err := s.db.QueryContext(context.Background(), `
SELECT id, key, space_id, created_at, version, format, location, size_bytes, blob
FROM assets
WHERE key = ?
ORDER BY version ASC`, key)
	if err != nil {
		panic(fmt.Sprintf("ListAssetVersionsByKeyIncludingPending: %v", err))
	}
	defer rows.Close()
	out := []*apigen.Asset{}
	for rows.Next() {
		var row Asset
		if err := rows.Scan(&row.ID, &row.Key, &row.SpaceID, &row.CreatedAt, &row.Version, &row.Format, &row.Location, &row.SizeBytes, &row.Blob); err != nil {
			panic(fmt.Sprintf("ListAssetVersionsByKeyIncludingPending scan: %v", err))
		}
		out = append(out, assetRowToProto(row))
	}
	if err := rows.Err(); err != nil {
		panic(fmt.Sprintf("ListAssetVersionsByKeyIncludingPending rows: %v", err))
	}
	return out
}

func (s *PrimaryStorage) OpenAsset(ctx context.Context, assetID int32) (*apigen.Asset, io.ReadCloser, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, key, space_id, created_at, version, format, location, size_bytes, blob
FROM assets
WHERE id = ? AND location NOT LIKE 'pending://%'`, assetID)
	var r Asset
	err := row.Scan(&r.ID, &r.Key, &r.SpaceID, &r.CreatedAt, &r.Version, &r.Format, &r.Location, &r.SizeBytes, &r.Blob)
	if err == sql.ErrNoRows {
		return nil, nil, fmt.Errorf("asset %d not found", assetID)
	}
	if err != nil {
		return nil, nil, err
	}
	asset := assetRowToProto(r)
	return asset, io.NopCloser(bytes.NewReader(asset.Blob)), nil
}

func (s *PrimaryStorage) SetAsset(key, format string, blob []byte, spaceIDs ...int32) *apigen.Asset {
	return s.SetAssetStored(key, format, "", int64(len(blob)), blob, spaceIDs...)
}

func (s *PrimaryStorage) SetAssetStored(key, format, location string, sizeBytes int64, blob []byte, spaceIDs ...int32) *apigen.Asset {
	ctx := context.Background()
	now := time.Now().UnixMilli()
	if blob == nil {
		blob = []byte{}
	}
	spaceID := DefaultSpaceID
	if len(spaceIDs) > 0 {
		spaceID = normalizedUserSpaceID(spaceIDs[0])
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin tx: %v", err))
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)

	version, err := q.GetNextAssetVersion(ctx, key)
	if err != nil {
		panic(fmt.Sprintf("GetNextAssetVersion: %v", err))
	}
	r, err := q.InsertAsset(ctx, InsertAssetParams{
		Key:       key,
		SpaceID:   int64(spaceID),
		CreatedAt: now,
		Version:   version,
		Format:    format,
		Location:  location,
		SizeBytes: sizeBytes,
		Blob:      blob,
	})
	if err != nil {
		panic(fmt.Sprintf("InsertAsset: %v", err))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit asset: %v", err))
	}
	return assetRowToProto(r)
}

func (s *PrimaryStorage) UpdateAssetLocation(id int32, location string) *apigen.Asset {
	r, err := s.q.UpdateAssetLocation(context.Background(), UpdateAssetLocationParams{ID: int64(id), Location: location})
	if err != nil {
		panic(fmt.Sprintf("UpdateAssetLocation: %v", err))
	}
	return assetRowToProto(r)
}

func (s *PrimaryStorage) RenameAsset(oldKey, newKey string) (*apigen.Asset, error) {
	if oldKey == newKey {
		asset, ok := s.GetAsset(oldKey, 0)
		if !ok {
			return nil, ErrAssetNotFound
		}
		return asset, nil
	}

	ctx := context.Background()
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin asset rename: %w", err)
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM assets WHERE key = ? LIMIT 1`, newKey).Scan(&exists); err == nil {
		return nil, ErrAssetAlreadyExists
	} else if err != sql.ErrNoRows {
		return nil, fmt.Errorf("check asset rename destination: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM assets WHERE key = ? LIMIT 1`, oldKey).Scan(&exists); err == sql.ErrNoRows {
		return nil, ErrAssetNotFound
	} else if err != nil {
		return nil, fmt.Errorf("check asset rename source: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE assets SET key = ? WHERE key = ?`, newKey, oldKey); err != nil {
		return nil, fmt.Errorf("rename asset: %w", err)
	}
	r, err := s.q.WithTx(tx).GetLatestAsset(ctx, newKey)
	if err != nil {
		return nil, fmt.Errorf("get renamed asset: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit asset rename: %w", err)
	}
	return assetRowToProto(r), nil
}

func (s *PrimaryStorage) DeleteAssetVersionByID(id int32) {
	if err := s.q.DeleteAssetVersionByID(context.Background(), int64(id)); err != nil {
		panic(fmt.Sprintf("DeleteAssetVersionByID: %v", err))
	}
}

func (s *PrimaryStorage) DeleteAsset(key string) {
	if err := s.q.DeleteAsset(context.Background(), key); err != nil {
		panic(fmt.Sprintf("DeleteAsset: %v", err))
	}
}
