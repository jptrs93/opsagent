package state

import (
	"context"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

const secretsEventLogMarker = "migration.secrets-event-log"

func (s *Service) migrateSecretsToEventLog() {
	if _, done := s.FetchLocalKV(secretsEventLogMarker); done {
		return
	}
	ctx := context.Background()
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		if err := q.SeedEventIDFloor(ctx, eventIDFloor); err != nil {
			return fmt.Errorf("seed event id floor: %w", err)
		}
		secretRows, err := q.ListSecretRows(ctx)
		if err != nil {
			return fmt.Errorf("list secrets: %w", err)
		}
		versions, err := q.ListSecretVersionRecords(ctx)
		if err != nil {
			return fmt.Errorf("list secret versions: %w", err)
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
			if container := spec.Container(); container != nil {
				for _, value := range container.Runtime.EnvVars {
					if value != nil && value.SecretRefID != nil && *value.SecretRefID > 0 {
						pinned[int64(*value.SecretRefID)] = true
					}
				}
			}
			for _, route := range spec.Networking.Ingress {
				if route == nil || route.HttpsConfig == nil || route.HttpsConfig.CertSource == nil {
					continue
				}
				if secret := route.HttpsConfig.CertSource.Secret; secret != nil && secret.SecretRefID > 0 {
					pinned[int64(secret.SecretRefID)] = true
				}
			}
		}

		versionsBySecret := make(map[int64][]pq.ListSecretVersionRecordsRow)
		for _, v := range versions {
			versionsBySecret[v.SecretID] = append(versionsBySecret[v.SecretID], v)
		}

		eventByOldRowID := make(map[int64]int64)
		for _, sec := range secretRows {
			rows := versionsBySecret[sec.ID]
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
				env := &apigen.SecretEnvelope{
					SmkVersion:    int32(v.SmkVersion),
					Nonce:         v.Nonce,
					Ciphertext:    v.Ciphertext,
					LegacyVersion: int32(v.Version),
				}
				eventID, err := q.InsertEvent(ctx, pq.InsertEventParams{
					Ts:         v.CreatedAt,
					AuthorID:   v.CreatedBy,
					EntityType: eventTypeSecret,
					EntityID:   sec.ID,
					Action:     action,
					Blob:       env.Encode(),
				})
				if err != nil {
					return fmt.Errorf("import secret %d version %d: %w", sec.ID, v.Version, err)
				}
				eventByOldRowID[v.ID] = eventID
				imported++
			}
			if err := q.InsertSecretDisplay(ctx, pq.InsertSecretDisplayParams{
				ID:          sec.ID,
				SpaceID:     sec.SpaceID,
				Name:        sec.Name,
				DirectoryID: sec.ValueDirectoryID,
				UpdatedAt:   sec.CreatedAt,
				UpdatedBy:   sec.CreatedBy,
			}); err != nil {
				return fmt.Errorf("import secret %d display: %w", sec.ID, err)
			}
		}

		for _, row := range deployments {
			spec := specs[row.DeploymentID]
			changed := false
			if container := spec.Container(); container != nil {
				for _, value := range container.Runtime.EnvVars {
					if value == nil || value.SecretRefID == nil || *value.SecretRefID <= 0 {
						continue
					}
					eventID, ok := eventByOldRowID[int64(*value.SecretRefID)]
					if !ok {
						continue
					}
					newID := int32(eventID)
					value.SecretRefID = &newID
					changed = true
				}
			}
			for _, route := range spec.Networking.Ingress {
				if route == nil || route.HttpsConfig == nil || route.HttpsConfig.CertSource == nil {
					continue
				}
				secret := route.HttpsConfig.CertSource.Secret
				if secret == nil || secret.SecretRefID <= 0 {
					continue
				}
				eventID, ok := eventByOldRowID[int64(secret.SecretRefID)]
				if !ok {
					continue
				}
				secret.SecretRefID = int32(eventID)
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
		panic(fmt.Sprintf("migrate secrets to event log: %v", err))
	}
	s.MustSetLocalKV(secretsEventLogMarker, []byte("1"))
}
