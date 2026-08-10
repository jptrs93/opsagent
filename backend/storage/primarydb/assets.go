package primarydb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/apigen"
)

var (
	ErrAssetNotFound      = errors.New("asset not found")
	ErrAssetAlreadyExists = errors.New("asset already exists")
	ErrAssetKeyInvalid    = errors.New("asset key is not a valid file name")
)

// ValidAssetKey reports whether key can be a file name in the asset namespace.
// Path separators are excluded because the full asset path is the join of the
// directory ancestry and the key.
func ValidAssetKey(key string) bool {
	if key == "" || key == "." || key == ".." || len(key) > 255 {
		return false
	}
	return !strings.ContainsAny(key, "/\\\x00")
}

// assetMetaFromRow builds the wire meta: identity at the root, version facts
// only in VersionRefs (newest first). refs must be non-empty — an asset with
// no published version is never surfaced as a meta.
func assetMetaFromRow(a Asset, refs []*apigen.AssetVersionMeta) *apigen.AssetMeta {
	return &apigen.AssetMeta{
		ID:               int32(a.ID),
		Key:              a.Key,
		SpaceID:          int32(a.SpaceID),
		AssetDirectoryID: int32(a.AssetDirectoryID),
		CreatedAt:        time.UnixMilli(a.CreatedAt),
		CreatedBy:        int32(a.CreatedBy),
		VersionRefs:      refs,
	}
}

func assetVersionMetaFromRow(v AssetVersion) *apigen.AssetVersionMeta {
	return &apigen.AssetVersionMeta{
		ID:        int32(v.ID),
		Version:   int32(v.Version),
		CreatedAt: time.UnixMilli(v.CreatedAt),
		CreatedBy: int32(v.CreatedBy),
		SizeBytes: int32(v.SizeBytes),
		Location:  v.Location,
	}
}

func assetVersionFromRows(a Asset, v AssetVersion) *apigen.AssetVersion {
	return &apigen.AssetVersion{
		ID:        int32(v.ID),
		AssetID:   int32(a.ID),
		Key:       a.Key,
		SpaceID:   int32(a.SpaceID),
		CreatedAt: time.UnixMilli(v.CreatedAt),
		CreatedBy: int32(v.CreatedBy),
		Version:   int32(v.Version),
		Location:  v.Location,
		SizeBytes: int32(v.SizeBytes),
		Blob:      v.Blob,
	}
}

func (s *Storage) ListAssets() []*apigen.AssetMeta {
	rows, err := s.q.ListAssetRows(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListAssetRows: %v", err))
	}
	versionRows, err := s.q.ListPublishedAssetVersionMetas(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListPublishedAssetVersionMetas: %v", err))
	}
	// The query orders by version DESC, so each asset's slice is newest first.
	versionRefs := make(map[int64][]*apigen.AssetVersionMeta, len(rows))
	for _, v := range versionRows {
		versionRefs[v.AssetID] = append(versionRefs[v.AssetID], &apigen.AssetVersionMeta{
			ID:        int32(v.ID),
			Version:   int32(v.Version),
			CreatedAt: time.UnixMilli(v.CreatedAt),
			CreatedBy: int32(v.CreatedBy),
			SizeBytes: int32(v.SizeBytes),
			Location:  v.Location,
		})
	}
	out := make([]*apigen.AssetMeta, 0, len(rows))
	for _, r := range rows {
		refs := versionRefs[r.ID]
		if len(refs) == 0 {
			// No published version yet (e.g. a pending first upload): not
			// listable.
			continue
		}
		out = append(out, assetMetaFromRow(r, refs))
	}
	return out
}

// ListAllAssetVersions returns every published version row across all assets,
// joined with its owning asset for display fields. Blobs are not loaded.
func (s *Storage) ListAllAssetVersions() []*apigen.AssetVersion {
	return s.listAllAssetVersions(false)
}

func (s *Storage) ListAllAssetVersionsIncludingPending() []*apigen.AssetVersion {
	return s.listAllAssetVersions(true)
}

func (s *Storage) listAllAssetVersions(includePending bool) []*apigen.AssetVersion {
	where := "WHERE v.location NOT LIKE 'pending://%'"
	if includePending {
		where = ""
	}
	rows, err := s.db.QueryContext(context.Background(), `
SELECT v.id, v.asset_id, v.version, v.created_at, v.created_by, v.location, v.size_bytes,
       a.key, a.space_id
FROM asset_versions v
JOIN assets a ON a.id = v.asset_id
`+where+`
ORDER BY a.key, v.version`)
	if err != nil {
		panic(fmt.Sprintf("ListAllAssetVersions: %v", err))
	}
	defer rows.Close()
	out := []*apigen.AssetVersion{}
	for rows.Next() {
		var (
			v AssetVersion
			a Asset
		)
		if err := rows.Scan(&v.ID, &v.AssetID, &v.Version, &v.CreatedAt, &v.CreatedBy, &v.Location, &v.SizeBytes, &a.Key, &a.SpaceID); err != nil {
			panic(fmt.Sprintf("ListAllAssetVersions scan: %v", err))
		}
		a.ID = v.AssetID
		out = append(out, assetVersionFromRows(a, v))
	}
	if err := rows.Err(); err != nil {
		panic(fmt.Sprintf("ListAllAssetVersions rows: %v", err))
	}
	return out
}

func (s *Storage) NotifyAssetUpdate(meta *apigen.AssetMeta) {
	if meta == nil || (meta.ID == 0 && meta.Key == "") {
		return
	}
	s.assetSubs.Notify(*meta)
}

func (s *Storage) NotifyAssetDeleted(meta *apigen.AssetMeta) {
	if meta == nil || (meta.ID == 0 && meta.Key == "") {
		return
	}
	cp := *meta
	cp.Deleted = true
	s.assetSubs.Notify(cp)
}

func (s *Storage) SubscribeAssetUpdates() (*pubsubu.Sub[apigen.AssetMeta], func()) {
	sub := s.assetSubs.Subscribe(nil)
	return sub, sub.UnsubscribeFunc
}

// GetAssetRow returns the stable asset identity row.
func (s *Storage) GetAssetRow(assetID int32) (Asset, bool) {
	r, err := s.q.GetAssetByID(context.Background(), int64(assetID))
	if errors.Is(err, sql.ErrNoRows) {
		return Asset{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetByID: %v", err))
	}
	return r, true
}

// GetAssetMeta returns the asset with its published version index, or false
// when the asset does not exist or has no published version yet.
func (s *Storage) GetAssetMeta(assetID int32) (*apigen.AssetMeta, bool) {
	a, ok := s.GetAssetRow(assetID)
	if !ok {
		return nil, false
	}
	versions, err := s.q.ListAssetVersions(context.Background(), a.ID)
	if err != nil {
		panic(fmt.Sprintf("ListAssetVersions: %v", err))
	}
	if len(versions) == 0 {
		return nil, false
	}
	refs := make([]*apigen.AssetVersionMeta, 0, len(versions))
	for i := len(versions) - 1; i >= 0; i-- { // query is oldest first; refs are newest first
		refs = append(refs, assetVersionMetaFromRow(versions[i]))
	}
	return assetMetaFromRow(a, refs), true
}

// GetAssetInRootByKey resolves an asset by key in a space's implicit root
// directory.
func (s *Storage) GetAssetInRootByKey(spaceID int32, key string) (Asset, bool) {
	r, err := s.q.GetAssetInDirectoryByKey(context.Background(), GetAssetInDirectoryByKeyParams{
		SpaceID:          int64(normalizedUserSpaceID(spaceID)),
		AssetDirectoryID: 0,
		Key:              key,
	})
	if err == sql.ErrNoRows {
		return Asset{}, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetInDirectoryByKey: %v", err))
	}
	return r, true
}

// GetAssetVersion returns one published version of an asset; version 0 means
// latest.
func (s *Storage) GetAssetVersion(assetID, version int32) (*apigen.AssetVersion, bool) {
	a, ok := s.GetAssetRow(assetID)
	if !ok {
		return nil, false
	}
	var (
		v   AssetVersion
		err error
	)
	if version > 0 {
		v, err = s.q.GetAssetVersionByNumber(context.Background(), GetAssetVersionByNumberParams{AssetID: a.ID, Version: int64(version)})
	} else {
		v, err = s.q.GetLatestAssetVersion(context.Background(), a.ID)
	}
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetVersion: %v", err))
	}
	return assetVersionFromRows(a, v), true
}

// GetAssetVersionByID resolves a version row id — the id deployment configs
// pin and workers fetch by.
func (s *Storage) GetAssetVersionByID(assetVersionID int32) (*apigen.AssetVersion, bool) {
	return s.getAssetVersionByID(assetVersionID, false)
}

func (s *Storage) GetAssetVersionByIDIncludingPending(assetVersionID int32) (*apigen.AssetVersion, bool) {
	return s.getAssetVersionByID(assetVersionID, true)
}

func (s *Storage) getAssetVersionByID(assetVersionID int32, includePending bool) (*apigen.AssetVersion, bool) {
	pendingClause := "AND v.location NOT LIKE 'pending://%'"
	if includePending {
		pendingClause = ""
	}
	row := s.db.QueryRowContext(context.Background(), `
SELECT v.id, v.asset_id, v.version, v.created_at, v.created_by, v.location, v.size_bytes, v.blob,
       a.key, a.space_id
FROM asset_versions v
JOIN assets a ON a.id = v.asset_id
WHERE v.id = ? `+pendingClause, assetVersionID)
	var (
		v AssetVersion
		a Asset
	)
	err := row.Scan(&v.ID, &v.AssetID, &v.Version, &v.CreatedAt, &v.CreatedBy, &v.Location, &v.SizeBytes, &v.Blob, &a.Key, &a.SpaceID)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetVersionByID: %v", err))
	}
	a.ID = v.AssetID
	return assetVersionFromRows(a, v), true
}

// ListAssetVersions returns every published version of one asset, oldest
// first.
func (s *Storage) ListAssetVersions(assetID int32) []*apigen.AssetVersion {
	a, ok := s.GetAssetRow(assetID)
	if !ok {
		return nil
	}
	rows, err := s.q.ListAssetVersions(context.Background(), a.ID)
	if err != nil {
		panic(fmt.Sprintf("ListAssetVersions: %v", err))
	}
	out := make([]*apigen.AssetVersion, 0, len(rows))
	for _, v := range rows {
		out = append(out, assetVersionFromRows(a, v))
	}
	return out
}

func (s *Storage) ListAssetVersionsIncludingPending(assetID int32) []*apigen.AssetVersion {
	a, ok := s.GetAssetRow(assetID)
	if !ok {
		return nil
	}
	rows, err := s.q.ListAssetVersionsIncludingPending(context.Background(), a.ID)
	if err != nil {
		panic(fmt.Sprintf("ListAssetVersionsIncludingPending: %v", err))
	}
	out := make([]*apigen.AssetVersion, 0, len(rows))
	for _, v := range rows {
		out = append(out, assetVersionFromRows(a, v))
	}
	return out
}

// assetSiblingKeyTakenLocked reports whether key is already used by another
// asset or a directory under (spaceID, directoryID). Caller must hold s.Mu:
// path uniqueness spans two tables, so only the mutex makes the check-and-write
// atomic.
func (s *Storage) assetSiblingKeyTakenLocked(ctx context.Context, q *Queries, spaceID, directoryID int64, key string, excludeAssetID int64) bool {
	assets, err := q.CountAssetSiblingsWithKey(ctx, CountAssetSiblingsWithKeyParams{
		SpaceID:          spaceID,
		AssetDirectoryID: directoryID,
		Key:              key,
		ID:               excludeAssetID,
	})
	if err != nil {
		panic(fmt.Sprintf("CountAssetSiblingsWithKey: %v", err))
	}
	if assets > 0 {
		return true
	}
	dirs, err := q.CountDirectorySiblingsWithKey(ctx, CountDirectorySiblingsWithKeyParams{
		SpaceID:  spaceID,
		ParentID: directoryID,
		Key:      key,
	})
	if err != nil {
		panic(fmt.Sprintf("CountDirectorySiblingsWithKey: %v", err))
	}
	return dirs > 0
}

// CreateAssetWithVersion creates a new asset in the root directory of spaceID
// with its first version.
func (s *Storage) CreateAssetWithVersion(key string, spaceID, createdBy int32, location string, sizeBytes int64, blob []byte) (*apigen.AssetVersion, error) {
	if !ValidAssetKey(key) {
		return nil, ErrAssetKeyInvalid
	}
	if blob == nil {
		blob = []byte{}
	}
	ctx := context.Background()
	now := time.Now().UnixMilli()
	space := int64(normalizedUserSpaceID(spaceID))

	s.Mu.Lock()
	defer s.Mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin tx: %v", err))
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)

	if s.assetSiblingKeyTakenLocked(ctx, q, space, 0, key, 0) {
		return nil, ErrAssetAlreadyExists
	}
	a, err := q.InsertAssetRow(ctx, InsertAssetRowParams{
		SpaceID:          space,
		Key:              key,
		AssetDirectoryID: 0,
		CreatedAt:        now,
		CreatedBy:        int64(createdBy),
	})
	if err != nil {
		panic(fmt.Sprintf("InsertAssetRow: %v", err))
	}
	v, err := q.InsertAssetVersion(ctx, InsertAssetVersionParams{
		AssetID:   a.ID,
		Version:   1,
		CreatedAt: now,
		CreatedBy: int64(createdBy),
		Location:  location,
		SizeBytes: sizeBytes,
		Blob:      blob,
	})
	if err != nil {
		panic(fmt.Sprintf("InsertAssetVersion: %v", err))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit asset create: %v", err))
	}
	return assetVersionFromRows(a, v), nil
}

// AppendAssetVersion appends the next version of an existing asset. The asset
// identity — key, space, directory — is untouched.
func (s *Storage) AppendAssetVersion(assetID, createdBy int32, location string, sizeBytes int64, blob []byte) (*apigen.AssetVersion, error) {
	if blob == nil {
		blob = []byte{}
	}
	ctx := context.Background()
	now := time.Now().UnixMilli()

	s.Mu.Lock()
	defer s.Mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin tx: %v", err))
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)

	a, err := q.GetAssetByID(ctx, int64(assetID))
	if err == sql.ErrNoRows {
		return nil, ErrAssetNotFound
	}
	if err != nil {
		panic(fmt.Sprintf("GetAssetByID: %v", err))
	}
	version, err := q.GetNextAssetVersionNumber(ctx, a.ID)
	if err != nil {
		panic(fmt.Sprintf("GetNextAssetVersionNumber: %v", err))
	}
	v, err := q.InsertAssetVersion(ctx, InsertAssetVersionParams{
		AssetID:   a.ID,
		Version:   version,
		CreatedAt: now,
		CreatedBy: int64(createdBy),
		Location:  location,
		SizeBytes: sizeBytes,
		Blob:      blob,
	})
	if err != nil {
		panic(fmt.Sprintf("InsertAssetVersion: %v", err))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit asset version: %v", err))
	}
	return assetVersionFromRows(a, v), nil
}

func (s *Storage) UpdateAssetVersionLocation(assetVersionID int32, location string) *apigen.AssetVersion {
	v, err := s.q.UpdateAssetVersionLocation(context.Background(), UpdateAssetVersionLocationParams{ID: int64(assetVersionID), Location: location})
	if err != nil {
		panic(fmt.Sprintf("UpdateAssetVersionLocation: %v", err))
	}
	a, ok := s.GetAssetRow(int32(v.AssetID))
	if !ok {
		a = Asset{ID: v.AssetID}
	}
	return assetVersionFromRows(a, v)
}

// RenameAssetKey renames an asset in place. Version rows, ids, and content are
// untouched; deployment configs keep working because they pin version row ids.
func (s *Storage) RenameAssetKey(assetID int32, newKey string) (*apigen.AssetMeta, error) {
	if !ValidAssetKey(newKey) {
		return nil, ErrAssetKeyInvalid
	}
	ctx := context.Background()

	s.Mu.Lock()
	defer s.Mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin asset rename: %w", err)
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)

	a, err := q.GetAssetByID(ctx, int64(assetID))
	if err == sql.ErrNoRows {
		return nil, ErrAssetNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load asset for rename: %w", err)
	}
	if a.Key != newKey {
		if s.assetSiblingKeyTakenLocked(ctx, q, a.SpaceID, a.AssetDirectoryID, newKey, a.ID) {
			return nil, ErrAssetAlreadyExists
		}
		if err := q.RenameAssetKey(ctx, RenameAssetKeyParams{Key: newKey, ID: a.ID}); err != nil {
			return nil, fmt.Errorf("rename asset: %w", err)
		}
		a.Key = newKey
	}
	versions, err := q.ListAssetVersions(ctx, a.ID)
	if err != nil {
		return nil, fmt.Errorf("load renamed asset versions: %w", err)
	}
	if len(versions) == 0 {
		return nil, ErrAssetNotFound
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit asset rename: %w", err)
	}
	refs := make([]*apigen.AssetVersionMeta, 0, len(versions))
	for i := len(versions) - 1; i >= 0; i-- { // query is oldest first; refs are newest first
		refs = append(refs, assetVersionMetaFromRow(versions[i]))
	}
	return assetMetaFromRow(a, refs), nil
}

func (s *Storage) DeleteAssetVersionByID(assetVersionID int32) {
	if err := s.q.DeleteAssetVersionByID(context.Background(), int64(assetVersionID)); err != nil {
		panic(fmt.Sprintf("DeleteAssetVersionByID: %v", err))
	}
}

// DeleteAsset removes the asset identity and every version row.
func (s *Storage) DeleteAsset(assetID int32) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		panic(fmt.Sprintf("begin asset delete: %v", err))
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)
	if err := q.DeleteAssetVersionsByAssetID(ctx, int64(assetID)); err != nil {
		panic(fmt.Sprintf("DeleteAssetVersionsByAssetID: %v", err))
	}
	if err := q.DeleteAssetRow(ctx, int64(assetID)); err != nil {
		panic(fmt.Sprintf("DeleteAssetRow: %v", err))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("commit asset delete: %v", err))
	}
}

// DeleteAssetIfNoVersions removes an asset row left with no version rows at
// all — e.g. after a failed first upload — so its key does not stay claimed by
// an invisible asset.
func (s *Storage) DeleteAssetIfNoVersions(assetID int32) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	rows, err := s.q.ListAssetVersionsIncludingPending(context.Background(), int64(assetID))
	if err != nil {
		panic(fmt.Sprintf("ListAssetVersionsIncludingPending: %v", err))
	}
	if len(rows) > 0 {
		return
	}
	if err := s.q.DeleteAssetRow(context.Background(), int64(assetID)); err != nil {
		panic(fmt.Sprintf("DeleteAssetRow: %v", err))
	}
}

// SetAssetByKey creates the asset in spaceID's root on first use and appends a
// version on each later call. Test and seed convenience.
func (s *Storage) SetAssetByKey(key string, blob []byte, spaceIDs ...int32) *apigen.AssetVersion {
	spaceID := DefaultSpaceID
	if len(spaceIDs) > 0 {
		spaceID = normalizedUserSpaceID(spaceIDs[0])
	}
	if existing, ok := s.GetAssetInRootByKey(spaceID, key); ok {
		v, err := s.AppendAssetVersion(int32(existing.ID), 0, "", int64(len(blob)), blob)
		if err != nil {
			panic(fmt.Sprintf("SetAssetByKey append: %v", err))
		}
		return v
	}
	v, err := s.CreateAssetWithVersion(key, spaceID, 0, "", int64(len(blob)), blob)
	if err != nil {
		panic(fmt.Sprintf("SetAssetByKey create: %v", err))
	}
	return v
}
