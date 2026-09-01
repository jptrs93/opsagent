package pq

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func migrateDeploymentEventLog(db *sql.DB) {
	ctx := context.Background()
	var events, olds int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployment_event_log`).Scan(&events); err != nil {
		panic(fmt.Sprintf("deployment event log migration: count events: %v", err))
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployments`).Scan(&olds); err != nil {
		panic(fmt.Sprintf("deployment event log migration: count deployments: %v", err))
	}
	if events > 0 || olds == 0 {
		return
	}

	identities, err := loadOldDeploymentIdentities(ctx, db)
	if err != nil {
		panic(fmt.Sprintf("deployment event log migration: %v", err))
	}
	specs, err := loadOldVersionRows(ctx, db, `SELECT deployment_id, version, created_at, author, global_seq, spec_blob, 0
		FROM deployment_spec_versions ORDER BY deployment_id, version`)
	if err != nil {
		panic(fmt.Sprintf("deployment event log migration: spec versions: %v", err))
	}
	spaces, err := loadOldVersionRows(ctx, db, `SELECT deployment_id, version, created_at, author, global_seq, NULL, space_id
		FROM deployment_space_versions ORDER BY deployment_id, version`)
	if err != nil {
		panic(fmt.Sprintf("deployment event log migration: space versions: %v", err))
	}

	var all []DeploymentEvent
	for _, ident := range identities {
		all = append(all, buildDeploymentEvents(ident, specs[ident.id], spaces[ident.id])...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.GlobalSeq != b.GlobalSeq {
			return a.GlobalSeq < b.GlobalSeq
		}
		if a.CreatedAt != b.CreatedAt {
			return a.CreatedAt < b.CreatedAt
		}
		if a.DeploymentID != b.DeploymentID {
			return a.DeploymentID < b.DeploymentID
		}
		return a.Version < b.Version
	})

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		panic(fmt.Sprintf("deployment event log migration: begin tx: %v", err))
	}
	defer tx.Rollback()
	q := New(tx)
	for _, e := range all {
		if err := q.InsertDeploymentEvent(ctx, e); err != nil {
			panic(fmt.Sprintf("deployment event log migration: insert event dep=%d v=%d: %v", e.DeploymentID, e.Version, err))
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE scheduled_instances SET space_id = COALESCE(
			(SELECT sp.space_id FROM deployment_space_versions sp
			 WHERE sp.id = scheduled_instances.deployment_space_version_id), 0)`); err != nil {
		panic(fmt.Sprintf("deployment event log migration: backfill instance space: %v", err))
	}
	if err := tx.Commit(); err != nil {
		panic(fmt.Sprintf("deployment event log migration: commit: %v", err))
	}
}

type oldDeploymentIdentity struct {
	id        int64
	nodeID    int64
	name      string
	deletedAt int64
}

type oldVersionRow struct {
	version   int64
	createdAt int64
	author    int64
	globalSeq int64
	specBlob  []byte
	spaceID   int64
}

func loadOldDeploymentIdentities(ctx context.Context, db *sql.DB) ([]oldDeploymentIdentity, error) {
	rows, err := db.QueryContext(ctx, `SELECT deployment_id, node_id, name, deleted_at FROM deployments ORDER BY deployment_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []oldDeploymentIdentity
	for rows.Next() {
		var ident oldDeploymentIdentity
		if err := rows.Scan(&ident.id, &ident.nodeID, &ident.name, &ident.deletedAt); err != nil {
			return nil, err
		}
		out = append(out, ident)
	}
	return out, rows.Err()
}

func loadOldVersionRows(ctx context.Context, db *sql.DB, query string) (map[int64][]oldVersionRow, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64][]oldVersionRow)
	for rows.Next() {
		var deploymentID int64
		var r oldVersionRow
		var blob []byte
		if err := rows.Scan(&deploymentID, &r.version, &r.createdAt, &r.author, &r.globalSeq, &blob, &r.spaceID); err != nil {
			return nil, err
		}
		r.specBlob = blob
		out[deploymentID] = append(out[deploymentID], r)
	}
	return out, rows.Err()
}

func buildDeploymentEvents(ident oldDeploymentIdentity, specs, spaces []oldVersionRow) []DeploymentEvent {
	if len(specs) == 0 {
		return nil
	}
	type step struct {
		row     oldVersionRow
		isSpace bool
	}
	var steps []step
	si, pi := 0, 0
	for pi < len(specs) || si < len(spaces) {
		takeSpec := pi < len(specs)
		if takeSpec && si < len(spaces) {
			sp, sc := specs[pi], spaces[si]
			if sc.globalSeq < sp.globalSeq ||
				(sc.globalSeq == sp.globalSeq && sc.createdAt < sp.createdAt) {
				takeSpec = false
			}
		}
		if takeSpec {
			steps = append(steps, step{row: specs[pi]})
			pi++
		} else {
			steps = append(steps, step{row: spaces[si], isSpace: true})
			si++
		}
	}

	createdAt := steps[0].row.createdAt
	var out []DeploymentEvent
	var specVersion, spaceVersion, spaceID int64
	var specBlob []byte
	for _, st := range steps {
		if st.isSpace {
			spaceVersion = st.row.version
			spaceID = st.row.spaceID
		} else {
			specVersion = st.row.version
			specBlob = st.row.specBlob
		}
		if len(out) == 1 && out[0].EventType == DeploymentEventCreate &&
			st.row.globalSeq == out[0].GlobalSeq && st.isSpace && st.row.version == 1 {
			out[0].SpaceAssignmentVersion = 1
			out[0].Value = encodeMigratedSnapshot(ident, out[0], createdAt, out[0].CreatedAt, out[0].Author, specBlob, spaceID, false)
			continue
		}
		eventType := DeploymentEventUpdate
		if len(out) == 0 {
			eventType = DeploymentEventCreate
		}
		e := DeploymentEvent{
			GlobalSeq:              st.row.globalSeq,
			CreatedAt:              st.row.createdAt,
			Author:                 st.row.author,
			DeploymentID:           ident.id,
			Version:                int64(len(out)) + 1,
			SpecVersion:            specVersion,
			SpaceAssignmentVersion: spaceVersion,
			NameVersion:            1,
			EventType:              eventType,
		}
		e.Value = encodeMigratedSnapshot(ident, e, createdAt, e.CreatedAt, e.Author, specBlob, spaceID, false)
		out = append(out, e)
	}
	if ident.deletedAt != 0 && len(out) > 0 {
		last := &out[len(out)-1]
		last.EventType = DeploymentEventDelete
		last.Value = encodeMigratedSnapshot(ident, *last, createdAt, last.CreatedAt, last.Author, specBlob, spaceID, true)
	}
	return out
}

func encodeMigratedSnapshot(ident oldDeploymentIdentity, e DeploymentEvent, createdAt, updatedAt, author int64, specBlob []byte, spaceID int64, deleted bool) []byte {
	spec, err := apigen.DecodeDeploymentSpec(specBlob)
	if err != nil {
		panic(fmt.Sprintf("deployment event log migration: decode spec dep=%d specVersion=%d: %v", ident.id, e.SpecVersion, err))
	}
	snapshot := apigen.Deployment{
		ID:           int32(ident.id),
		NodeID:       int32(ident.nodeID),
		SpaceID:      int32(spaceID),
		Version:      int32(e.Version),
		SpaceVersion: int32(e.SpaceAssignmentVersion),
		Name:         ident.name,
		CreatedAt:    time.UnixMilli(createdAt),
		UpdatedAt:    time.UnixMilli(updatedAt),
		Author:       int32(author),
		SpecVersion:  int32(e.SpecVersion),
		Deleted:      deleted,
	}
	if spec != nil {
		snapshot.Spec = *spec
	}
	return snapshot.Encode()
}
