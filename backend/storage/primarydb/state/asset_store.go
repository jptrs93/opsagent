package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// AssetStoreMeta is a content-store row without its inline blob loaded.
type AssetStoreMeta struct {
	ID           string
	Sha256       string
	SizeBytes    int64
	InlineSize   int64
	LocalStatus  int64
	RemoteStatus int64
	CreatedAt    int64
}

// Staging reports whether the row's content has not finished uploading.
func (m AssetStoreMeta) Staging() bool { return m.Sha256 == "" }

// LegacySha reports whether the row still carries a migration placeholder sha
// and needs its content hashed.
func (m AssetStoreMeta) LegacySha() bool { return strings.HasPrefix(m.Sha256, "legacy:") }

// FileBacked reports whether the row's content lives outside the database.
func (m AssetStoreMeta) FileBacked() bool {
	return !m.Staging() && m.InlineSize == 0 && m.SizeBytes > 0
}

// InsertAssetStoreRow inserts a content-store row. A staging row carries an
// empty sha and zero statuses until the content is durable.
func (s *Service) InsertAssetStoreRow(id, sha256 string, sizeBytes int64, inlineBlob []byte, localStatus, remoteStatus int64) AssetStore {
	if inlineBlob == nil {
		inlineBlob = []byte{}
	}
	s.Mu.Lock()
	defer s.Mu.Unlock()
	row, err := s.q.InsertAssetStoreRow(context.Background(), pq.InsertAssetStoreRowParams{
		ID:           id,
		Sha256:       sha256,
		SizeBytes:    sizeBytes,
		InlineBlob:   inlineBlob,
		LocalStatus:  localStatus,
		RemoteStatus: remoteStatus,
		CreatedAt:    time.Now().UnixMilli(),
	})
	if err != nil {
		panic(fmt.Sprintf("InsertAssetStoreRow: %v", err))
	}
	return row
}

func (s *Service) GetAssetStoreRowByID(id string) (AssetStore, bool) {
	row, err := s.q.GetAssetStoreRowByID(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return AssetStore{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetStoreRowByID: %v", err))
	}
	return row, true
}

func (s *Service) GetAssetStoreRowBySha(sha256 string) (AssetStore, bool) {
	row, err := s.q.GetAssetStoreRowBySha(context.Background(), sha256)
	if errors.Is(err, sql.ErrNoRows) {
		return AssetStore{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetStoreRowBySha: %v", err))
	}
	return row, true
}

// CompleteAssetStoreRow marks a staging row's content durable: the computed
// sha plus which storage side holds it.
func (s *Service) CompleteAssetStoreRow(id, sha256 string, localStatus, remoteStatus int64) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if err := s.q.CompleteAssetStoreRow(context.Background(), pq.CompleteAssetStoreRowParams{
		Sha256:       sha256,
		LocalStatus:  localStatus,
		RemoteStatus: remoteStatus,
		ID:           id,
	}); err != nil {
		panic(fmt.Sprintf("CompleteAssetStoreRow: %v", err))
	}
}

func (s *Service) SetAssetStoreLocalStatus(id string, status int64) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if err := s.q.SetAssetStoreLocalStatus(context.Background(), pq.SetAssetStoreLocalStatusParams{LocalStatus: status, ID: id}); err != nil {
		panic(fmt.Sprintf("SetAssetStoreLocalStatus: %v", err))
	}
}

func (s *Service) SetAssetStoreRemoteStatus(id string, status int64) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if err := s.q.SetAssetStoreRemoteStatus(context.Background(), pq.SetAssetStoreRemoteStatusParams{RemoteStatus: status, ID: id}); err != nil {
		panic(fmt.Sprintf("SetAssetStoreRemoteStatus: %v", err))
	}
}

func (s *Service) DeleteAssetStoreRow(id string) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if err := s.q.DeleteAssetStoreRow(context.Background(), id); err != nil {
		panic(fmt.Sprintf("DeleteAssetStoreRow: %v", err))
	}
}

func (s *Service) ListAssetStoreRowMetas() []AssetStoreMeta {
	rows, err := s.q.ListAssetStoreRowMetas(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListAssetStoreRowMetas: %v", err))
	}
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

// ListUnreferencedAssetStoreRows returns rows no version links to that were
// created before cutoff.
func (s *Service) ListUnreferencedAssetStoreRows(cutoff time.Time) []AssetStoreMeta {
	rows, err := s.q.ListUnreferencedAssetStoreRows(context.Background(), cutoff.UnixMilli())
	if err != nil {
		panic(fmt.Sprintf("ListUnreferencedAssetStoreRows: %v", err))
	}
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

func (s *Service) CountAssetVersionsBySha(sha256 string) int64 {
	count, err := s.q.CountAssetVersionsBySha(context.Background(), sha256)
	if err != nil {
		panic(fmt.Sprintf("CountAssetVersionsBySha: %v", err))
	}
	return count
}

func (s *Service) ListAssetIDsBySha(sha256 string) []int32 {
	rows, err := s.q.ListAssetIDsBySha(context.Background(), sha256)
	if err != nil {
		panic(fmt.Sprintf("ListAssetIDsBySha: %v", err))
	}
	out := make([]int32, 0, len(rows))
	for _, id := range rows {
		out = append(out, int32(id))
	}
	return out
}

// RelinkLegacyAssetSha replaces a migration placeholder sha with the real
// content hash. When another store row already holds that sha the row merges
// into it: every version linking the placeholder is repointed and the
// duplicate row is deleted. Returns whether a merge happened, in which case
// the caller reclaims the duplicate's local file.
func (s *Service) RelinkLegacyAssetSha(storeID, legacySha, realSha string) bool {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	ctx := context.Background()
	existing, err := s.q.GetAssetStoreRowBySha(ctx, realSha)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		panic(fmt.Sprintf("GetAssetStoreRowBySha: %v", err))
	}
	merge := err == nil && existing.ID != storeID
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		if merge {
			if err := q.DeleteAssetStoreRow(ctx, storeID); err != nil {
				panic(fmt.Sprintf("DeleteAssetStoreRow: %v", err))
			}
		} else if err := q.SetAssetStoreSha(ctx, pq.SetAssetStoreShaParams{Sha256: realSha, ID: storeID}); err != nil {
			panic(fmt.Sprintf("SetAssetStoreSha: %v", err))
		}
		if err := q.RelinkAssetVersionsSha(ctx, pq.RelinkAssetVersionsShaParams{Sha256: realSha, Sha256_2: legacySha}); err != nil {
			panic(fmt.Sprintf("RelinkAssetVersionsSha: %v", err))
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("relink asset sha tx: %v", err))
	}
	return merge
}
