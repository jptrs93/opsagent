package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
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

func (s *PrimaryStorage) GetAssetByIDVersion(assetID, version int32) (*apigen.Asset, bool) {
	r, err := s.q.GetAssetByIDVersion(context.Background(), GetAssetByIDVersionParams{ID: int64(assetID), Version: int64(version)})
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetByIDVersion: %v", err))
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

func (s *PrimaryStorage) OpenAsset(ctx context.Context, assetID, version int32) (*apigen.Asset, io.ReadCloser, error) {
	r, err := s.q.GetAssetByIDVersion(ctx, GetAssetByIDVersionParams{ID: int64(assetID), Version: int64(version)})
	if err == sql.ErrNoRows {
		return nil, nil, fmt.Errorf("asset %d version %d not found", assetID, version)
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

func (s *PrimaryStorage) RenameAsset(oldKey, newKey string) (*apigen.Asset, bool) {
	if oldKey == newKey {
		return s.GetAsset(oldKey, 0)
	}

	ctx := context.Background()
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.ExecContext(ctx, `UPDATE assets SET key = ? WHERE key = ?`, newKey, oldKey)
	if err != nil {
		panic(fmt.Sprintf("RenameAsset: %v", err))
	}
	rows, err := res.RowsAffected()
	if err != nil {
		panic(fmt.Sprintf("RenameAsset rows affected: %v", err))
	}
	if rows == 0 {
		return nil, false
	}

	r, err := s.q.GetLatestAsset(ctx, newKey)
	if err != nil {
		panic(fmt.Sprintf("RenameAsset latest: %v", err))
	}
	return assetRowToProto(r), true
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
