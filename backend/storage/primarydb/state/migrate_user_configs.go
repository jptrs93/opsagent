package state

import (
	"context"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

const configsEventLogMarker = "migration.configs-event-log"

const eventIDFloor = 1_000_000

func (s *Service) migrateConfigsToEventLog() {
	if _, done := s.FetchLocalKV(configsEventLogMarker); done {
		return
	}
	ctx := context.Background()
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		if err := q.SeedEventIDFloor(ctx, eventIDFloor); err != nil {
			return fmt.Errorf("seed event id floor: %w", err)
		}
		configs, err := q.ListConfigRows(ctx)
		if err != nil {
			return fmt.Errorf("list configs: %w", err)
		}
		versions, err := q.ListConfigVersionRows(ctx)
		if err != nil {
			return fmt.Errorf("list config versions: %w", err)
		}
		deployments, err := q.ListAllDeploymentConfigs(ctx)
		if err != nil {
			return fmt.Errorf("list deployments: %w", err)
		}

		specs := make(map[int64]*apigen.DeploymentSpec, len(deployments))
		pinned := make(map[int64]bool)
		for _, row := range deployments {
			spec, err := apigen.DecodeDeploymentSpec(row.SpecBlob)
			if err != nil {
				return fmt.Errorf("decode deployment %d spec: %w", row.DeploymentID, err)
			}
			specs[row.DeploymentID] = spec
			container := spec.Container()
			if container == nil {
				continue
			}
			for _, value := range container.Runtime.EnvVars {
				if value != nil && value.ConfigRefID != nil && *value.ConfigRefID > 0 {
					pinned[int64(*value.ConfigRefID)] = true
				}
			}
		}

		versionsByConfig := make(map[int64][]pq.ConfigVersion)
		for _, v := range versions {
			versionsByConfig[v.ConfigID] = append(versionsByConfig[v.ConfigID], v)
		}

		seqByOldRowID := make(map[int64]int64)
		for _, c := range configs {
			rows := versionsByConfig[c.ID]
			if len(rows) == 0 {
				continue
			}
			latest := rows[len(rows)-1].ID
			imported := 0
			for _, v := range rows {
				if v.ID != latest && !pinned[v.ID] {
					continue
				}
				action := eventActionUpdate
				if imported == 0 {
					action = eventActionCreate
				}
				seq, err := q.InsertEvent(ctx, pq.InsertEventParams{
					Ts:         v.CreatedAt,
					AuthorID:   v.CreatedBy,
					EntityType: eventTypeConfig,
					EntityID:   c.ID,
					Action:     action,
					Blob:       []byte(v.Value),
				})
				if err != nil {
					return fmt.Errorf("import config %d version %d: %w", c.ID, v.Version, err)
				}
				seqByOldRowID[v.ID] = seq
				imported++
			}
			if err := q.InsertConfigDisplay(ctx, pq.InsertConfigDisplayParams{
				ID:          c.ID,
				SpaceID:     c.SpaceID,
				Name:        c.Name,
				DirectoryID: c.ValueDirectoryID,
				UpdatedAt:   c.CreatedAt,
				UpdatedBy:   c.CreatedBy,
			}); err != nil {
				return fmt.Errorf("import config %d display: %w", c.ID, err)
			}
		}

		for _, row := range deployments {
			spec := specs[row.DeploymentID]
			container := spec.Container()
			if container == nil {
				continue
			}
			changed := false
			for _, value := range container.Runtime.EnvVars {
				if value == nil || value.ConfigRefID == nil || *value.ConfigRefID <= 0 {
					continue
				}
				seq, ok := seqByOldRowID[int64(*value.ConfigRefID)]
				if !ok {
					continue
				}
				newID := int32(seq)
				value.ConfigRefID = &newID
				changed = true
			}
			if changed {
				if err := q.UpdateDeploymentSpecBlobInPlace(ctx, pq.UpdateDeploymentSpecBlobInPlaceParams{
					SpecBlob:     spec.Encode(),
					DeploymentID: row.DeploymentID,
				}); err != nil {
					return fmt.Errorf("rewrite deployment %d spec: %w", row.DeploymentID, err)
				}
			}
		}
		return nil
	}); err != nil {
		panic(fmt.Sprintf("migrate configs to event log: %v", err))
	}
	s.MustSetLocalKV(configsEventLogMarker, []byte("1"))
}
