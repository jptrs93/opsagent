package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func assetRowToProto(r Asset) *apigen.Asset {
	return &apigen.Asset{
		ID:        int32(r.ID),
		Key:       r.Key,
		CreatedAt: time.UnixMilli(r.CreatedAt),
		Version:   int32(r.Version),
		Format:    r.Format,
		Location:  r.Location,
		Blob:      r.Blob,
	}
}

func assetRowToMeta(r ListLatestAssetsRow) *apigen.AssetMeta {
	sizeBytes := int32(0)
	if r.SizeBytes.Valid {
		sizeBytes = int32(r.SizeBytes.Int64)
	}
	return &apigen.AssetMeta{
		ID:        int32(r.ID),
		Key:       r.Key,
		CreatedAt: time.UnixMilli(r.CreatedAt),
		Version:   int32(r.Version),
		Format:    r.Format,
		Location:  r.Location,
		SizeBytes: sizeBytes,
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

func (s *PrimaryStorage) FetchAsset(ctx context.Context, assetID, version int32) (*apigen.ClusterAssetBlob, error) {
	r, err := s.q.GetAssetByIDVersion(ctx, GetAssetByIDVersionParams{ID: int64(assetID), Version: int64(version)})
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("asset %d version %d not found", assetID, version)
	}
	if err != nil {
		return nil, err
	}
	return &apigen.ClusterAssetBlob{
		AssetID: int32(r.ID),
		Key:     r.Key,
		Version: int32(r.Version),
		Format:  r.Format,
		Blob:    r.Blob,
	}, nil
}

func (s *PrimaryStorage) SetAsset(key, format string, blob []byte) *apigen.Asset {
	ctx := context.Background()
	now := time.Now().UnixMilli()

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
		CreatedAt: now,
		Version:   version,
		Format:    format,
		Location:  "",
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

func (s *PrimaryStorage) DeleteAsset(key string) {
	if err := s.q.DeleteAsset(context.Background(), key); err != nil {
		panic(fmt.Sprintf("DeleteAsset: %v", err))
	}
}
