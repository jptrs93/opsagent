package sqlite

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

var ErrInvalidReferencingDeployments = errors.New("invalid referencing deployments")
var ErrReferencingDeploymentsChanged = errors.New("referencing deployments changed")

type versionedValueReferenceType uint8

const (
	secretValueReference versionedValueReferenceType = iota + 1
	configValueReference
)

type deploymentReferenceUpdate struct {
	row  DeploymentConfig
	spec *apigen.DeploymentSpec
}

func (s *PrimaryStorage) setVersionedValueWithDeploymentUpdates(
	referenceType versionedValueReferenceType,
	name string,
	updateDeployments bool,
	expected []storage.DeploymentConfigVersion,
	updatedBy int32,
	insert func(*Queries) (int32, error),
	afterCommit func([]int32),
) ([]int32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin versioned value transaction: %w", err)
	}
	defer tx.Rollback()
	q := s.q.WithTx(tx)

	updates, referenceIDs, err := prepareDeploymentReferenceUpdates(ctx, q, referenceType, name, updateDeployments, expected)
	if err != nil {
		return nil, err
	}
	newID, err := insert(q)
	if err != nil {
		return nil, err
	}

	now := time.Now().UnixMilli()
	updatedConfigs := make([]*apigen.DeploymentConfig, 0, len(updates))
	for _, update := range updates {
		replaceDeploymentReferences(update.spec, referenceType, referenceIDs, newID)
		params := UpsertDeploymentConfigParams{
			DeploymentID: update.row.DeploymentID,
			NodeID:       update.row.NodeID,
			SpaceID:      update.row.SpaceID,
			Name:         update.row.Name,
			CreatedAt:    update.row.CreatedAt,
			Version:      update.row.Version + 1,
			UpdatedAt:    now,
			UpdatedBy:    int64(updatedBy),
			SpecBlob:     update.spec.Encode(),
			Deleted:      update.row.Deleted,
		}
		if err := q.UpsertDeploymentConfig(ctx, params); err != nil {
			return nil, fmt.Errorf("update deployment %d reference: %w", update.row.DeploymentID, err)
		}
		if err := q.InsertDeploymentConfigHistory(ctx, InsertDeploymentConfigHistoryParams{
			DeploymentID: params.DeploymentID,
			Version:      params.Version,
			UpdatedAt:    params.UpdatedAt,
			UpdatedBy:    params.UpdatedBy,
			SpecBlob:     params.SpecBlob,
			Deleted:      params.Deleted,
		}); err != nil {
			return nil, fmt.Errorf("record deployment %d reference update: %w", update.row.DeploymentID, err)
		}
		updatedConfigs = append(updatedConfigs, upsertParamsToProto(params))
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit versioned value transaction: %w", err)
	}

	updatedIDs := make([]int32, 0, len(updatedConfigs))
	for _, cfg := range updatedConfigs {
		s.configCache[cfg.ID] = cfg
		updatedIDs = append(updatedIDs, cfg.ID)
	}
	if afterCommit != nil {
		afterCommit(updatedIDs)
	}
	for _, id := range updatedIDs {
		s.notifyFromCache(id)
	}
	return updatedIDs, nil
}

func prepareDeploymentReferenceUpdates(
	ctx context.Context,
	q *Queries,
	referenceType versionedValueReferenceType,
	name string,
	updateDeployments bool,
	expected []storage.DeploymentConfigVersion,
) ([]deploymentReferenceUpdate, map[int32]struct{}, error) {
	if !updateDeployments {
		if len(expected) != 0 {
			return nil, nil, fmt.Errorf("%w: deployment list requires update flag", ErrInvalidReferencingDeployments)
		}
		return nil, nil, nil
	}

	referenceIDs, err := versionedValueIDs(ctx, q, referenceType, name)
	if err != nil {
		return nil, nil, err
	}
	rows, err := q.ListAllDeploymentConfigs(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list deployments for reference update: %w", err)
	}
	actual := make(map[int32]deploymentReferenceUpdate)
	for _, row := range rows {
		spec, err := apigen.DecodeDeploymentSpec(row.SpecBlob)
		if err != nil {
			return nil, nil, fmt.Errorf("decode deployment %d version %d spec: %w", row.DeploymentID, row.Version, err)
		}
		if deploymentUsesReferences(spec, referenceType, referenceIDs) {
			actual[int32(row.DeploymentID)] = deploymentReferenceUpdate{row: row, spec: spec}
		}
	}

	seen := make(map[int32]struct{}, len(expected))
	for _, item := range expected {
		if item.ID <= 0 || item.Version <= 0 {
			return nil, nil, fmt.Errorf("%w: deployment id and version must be positive", ErrInvalidReferencingDeployments)
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return nil, nil, fmt.Errorf("%w: duplicate deployment id %d", ErrInvalidReferencingDeployments, item.ID)
		}
		seen[item.ID] = struct{}{}
		current, ok := actual[item.ID]
		if !ok || int32(current.row.Version) != item.Version {
			return nil, nil, fmt.Errorf("%w: deployment %d version is stale or no longer references %s", ErrReferencingDeploymentsChanged, item.ID, name)
		}
	}
	if len(seen) != len(actual) {
		return nil, nil, fmt.Errorf("%w: expected %d deployments, got %d", ErrReferencingDeploymentsChanged, len(actual), len(seen))
	}

	updates := make([]deploymentReferenceUpdate, 0, len(actual))
	for _, item := range expected {
		updates = append(updates, actual[item.ID])
	}
	return updates, referenceIDs, nil
}

func versionedValueIDs(ctx context.Context, q *Queries, referenceType versionedValueReferenceType, name string) (map[int32]struct{}, error) {
	ids := make(map[int32]struct{})
	if referenceType == secretValueReference {
		rows, err := q.ListSecrets(ctx)
		if err != nil {
			return nil, fmt.Errorf("list secret versions: %w", err)
		}
		for _, row := range rows {
			if row.Name == name {
				ids[int32(row.ID)] = struct{}{}
			}
		}
		return ids, nil
	}

	rows, err := q.ListAllUserConfigs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list config versions: %w", err)
	}
	for _, row := range rows {
		if row.Name == name {
			ids[int32(row.ID)] = struct{}{}
		}
	}
	return ids, nil
}

func deploymentUsesReferences(spec *apigen.DeploymentSpec, referenceType versionedValueReferenceType, ids map[int32]struct{}) bool {
	container := spec.Container()
	if container == nil {
		return false
	}
	for _, value := range container.Runtime.EnvVars {
		if _, ok := ids[referencedValueID(value, referenceType)]; ok {
			return true
		}
	}
	return false
}

func replaceDeploymentReferences(spec *apigen.DeploymentSpec, referenceType versionedValueReferenceType, referenceIDs map[int32]struct{}, replacementID int32) {
	container := spec.Container()
	if container == nil {
		return
	}
	for _, value := range container.Runtime.EnvVars {
		if _, ok := referenceIDs[referencedValueID(value, referenceType)]; !ok {
			continue
		}
		if referenceType == secretValueReference {
			value.SecretID = &replacementID
		} else {
			value.ConfigID = &replacementID
		}
	}
}

func referencedValueID(value *apigen.EnvVarValue, referenceType versionedValueReferenceType) int32 {
	if value == nil {
		return 0
	}
	if referenceType == secretValueReference {
		if value.SecretID != nil {
			return *value.SecretID
		}
		return 0
	}
	if value.ConfigID != nil {
		return *value.ConfigID
	}
	return 0
}
