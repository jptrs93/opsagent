package assets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

type AssetStoreMeta struct {
	ID           string
	Sha256       string
	SizeBytes    int64
	InlineSize   int64
	LocalStatus  int64
	RemoteStatus int64
	CreatedAt    int64
}

func (m AssetStoreMeta) Staging() bool { return m.Sha256 == "" }
func (m AssetStoreMeta) FileBacked() bool {
	return !m.Staging() && m.InlineSize == 0 && m.SizeBytes > 0
}
func InsertAssetStoreRow(q *pq.Queries, id, sha256 string, sizeBytes int64, inlineBlob []byte, localStatus, remoteStatus int64) pq.AssetStore {
	if inlineBlob == nil {
		inlineBlob = []byte{}
	}
	row := erru.Must(q.InsertAssetStoreRow(context.Background(), pq.InsertAssetStoreRowParams{
		ID:           id,
		Sha256:       sha256,
		SizeBytes:    sizeBytes,
		InlineBlob:   inlineBlob,
		LocalStatus:  localStatus,
		RemoteStatus: remoteStatus,
		CreatedAt:    time.Now().UnixMilli(),
	}))
	return row
}

func GetAssetStoreRowByID(q *pq.Queries, id string) (pq.AssetStore, bool) {
	row, err := q.GetAssetStoreRowByID(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return pq.AssetStore{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetStoreRowByID: %v", err))
	}
	return row, true
}

func GetAssetStoreRowBySha(q *pq.Queries, sha256 string) (pq.AssetStore, bool) {
	row, err := q.GetAssetStoreRowBySha(context.Background(), sha256)
	if errors.Is(err, sql.ErrNoRows) {
		return pq.AssetStore{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetStoreRowBySha: %v", err))
	}
	return row, true
}
func CompleteAssetStoreRow(q *pq.Queries, id, sha256 string, localStatus, remoteStatus int64) {
	if err := q.CompleteAssetStoreRow(context.Background(), pq.CompleteAssetStoreRowParams{
		Sha256:       sha256,
		LocalStatus:  localStatus,
		RemoteStatus: remoteStatus,
		ID:           id,
	}); err != nil {
		panic(fmt.Sprintf("CompleteAssetStoreRow: %v", err))
	}
}

func SetAssetStoreLocalStatus(q *pq.Queries, id string, status int64) {
	if err := q.SetAssetStoreLocalStatus(context.Background(), pq.SetAssetStoreLocalStatusParams{LocalStatus: status, ID: id}); err != nil {
		panic(fmt.Sprintf("SetAssetStoreLocalStatus: %v", err))
	}
}

func SetAssetStoreRemoteStatus(q *pq.Queries, id string, status int64) {
	if err := q.SetAssetStoreRemoteStatus(context.Background(), pq.SetAssetStoreRemoteStatusParams{RemoteStatus: status, ID: id}); err != nil {
		panic(fmt.Sprintf("SetAssetStoreRemoteStatus: %v", err))
	}
}

func DeleteAssetStoreRow(q *pq.Queries, id string) {
	if err := q.DeleteAssetStoreRow(context.Background(), id); err != nil {
		panic(fmt.Sprintf("DeleteAssetStoreRow: %v", err))
	}
}

func ListAssetStoreRowMetas(q *pq.Queries) []AssetStoreMeta {
	rows := erru.Must(q.ListAssetStoreRowMetas(context.Background()))
	out := make([]AssetStoreMeta, 0, len(rows))
	for _, r := range rows {
		out = append(out, AssetStoreMeta{
			ID:           r.ID,
			Sha256:       r.Sha256,
			SizeBytes:    r.SizeBytes,
			InlineSize:   r.InlineSize,
			LocalStatus:  r.LocalStatus,
			RemoteStatus: r.RemoteStatus,
			CreatedAt:    r.CreatedAt,
		})
	}
	return out
}
func ListUnreferencedAssetStoreRows(q *pq.Queries, cutoff time.Time) []AssetStoreMeta {
	rows := erru.Must(q.ListUnreferencedAssetStoreRows(context.Background(), cutoff.UnixMilli()))
	out := make([]AssetStoreMeta, 0, len(rows))
	for _, r := range rows {
		out = append(out, AssetStoreMeta{
			ID:           r.ID,
			Sha256:       r.Sha256,
			SizeBytes:    r.SizeBytes,
			InlineSize:   r.InlineSize,
			LocalStatus:  r.LocalStatus,
			RemoteStatus: r.RemoteStatus,
			CreatedAt:    r.CreatedAt,
		})
	}
	return out
}

func CountAssetVersionsBySha(q *pq.Queries, sha256 string) int64 {
	count := erru.Must(q.CountAssetVersionsBySha(context.Background(), sha256))
	return count
}

func ListAssetIDsBySha(q *pq.Queries, sha256 string) []int32 {
	rows := erru.Must(q.ListAssetIDsBySha(context.Background(), sha256))
	out := make([]int32, 0, len(rows))
	for _, id := range rows {
		out = append(out, int32(id))
	}
	return out
}
