package state

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

var ErrInvalidReferencingDeployments = errors.New("invalid referencing deployments")
var ErrReferencingDeploymentsChanged = errors.New("referencing deployments changed")

type versionedValueReferenceType uint8

const (
	secretValueReference versionedValueReferenceType = iota + 1
	configValueReference
)

type deploymentReferenceUpdate struct {
	row  pq.DeploymentConfigRow
	spec *apigen.DeploymentSpec
}

// setVersionedValueWithDeploymentUpdates appends a version of the stable
// secret/config identity stableID and optionally rolls the caller-asserted
// deployment references (which pin version row ids of that identity) to the
// new row atomically.
func (s *Service) setVersionedValueWithDeploymentUpdates(
	referenceType versionedValueReferenceType,
	stableID int32,
	updateDeployments bool,
	expected []storage.DeploymentConfigVersion,
	author int32,
	insert func(*pq.Queries) (int32, error),
	afterCommit func([]int32),
) ([]int32, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	ctx := context.Background()
	now := time.Now().UnixMilli()
	var updatedConfigs []*apigen.DeploymentConfig
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		updates, referenceIDs, err := prepareDeploymentReferenceUpdates(ctx, q, referenceType, stableID, updateDeployments, expected)
		if err != nil {
			return err
		}
		newID, err := insert(q)
		if err != nil {
			return err
		}

		updatedConfigs = make([]*apigen.DeploymentConfig, 0, len(updates))
		for _, update := range updates {
			replaceDeploymentReferences(update.spec, referenceType, referenceIDs, newID)
			next := update.row
			next.Version = update.row.Version + 1
			next.UpdatedAt = now
			next.Author = int64(author)
			next.SpecBlob = update.spec.Encode()
			if err := q.InsertDeploymentVersion(ctx, pq.InsertDeploymentVersionParams{
				DeploymentID: next.DeploymentID,
				Version:      next.Version,
				CreatedAt:    next.UpdatedAt,
				Author:       next.Author,
				SpecBlob:     next.SpecBlob,
			}); err != nil {
				return fmt.Errorf("update deployment %d reference: %w", update.row.DeploymentID, err)
			}
			updatedConfigs = append(updatedConfigs, configRowToProto(next))
		}
		return nil
	}); err != nil {
		return nil, err
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
		s.notifyConfigLocked(id)
	}
	return updatedIDs, nil
}

func prepareDeploymentReferenceUpdates(
	ctx context.Context,
	q *pq.Queries,
	referenceType versionedValueReferenceType,
	stableID int32,
	updateDeployments bool,
	expected []storage.DeploymentConfigVersion,
) ([]deploymentReferenceUpdate, map[int32]struct{}, error) {
	if !updateDeployments {
		if len(expected) != 0 {
			return nil, nil, fmt.Errorf("%w: deployment list requires update flag", ErrInvalidReferencingDeployments)
		}
		return nil, nil, nil
	}

	referenceIDs, err := versionedValueIDs(ctx, q, referenceType, stableID)
	if err != nil {
		return nil, nil, err
	}
	rows, err := q.ListAllDeploymentConfigs(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list deployments for reference update: %w", err)
	}
	actual := make(map[int32]deploymentReferenceUpdate)
	for _, row := range rows {
		// Deletion is soft and preserves the spec, so a tombstone keeps whatever
		// references it held when it was deleted. It will never run again, so
		// rewriting it is pointless — and counting it here would make every
		// rotation fail against the live set the caller can see.
		if row.DeletedAt != 0 {
			continue
		}
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
			return nil, nil, fmt.Errorf("%w: deployment %d version is stale or no longer references value %d", ErrReferencingDeploymentsChanged, item.ID, stableID)
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

// versionedValueIDs returns every version row id of the stable identity —
// exactly the set a deployment env ref could pin.
func versionedValueIDs(ctx context.Context, q *pq.Queries, referenceType versionedValueReferenceType, stableID int32) (map[int32]struct{}, error) {
	ids := make(map[int32]struct{})
	if referenceType == secretValueReference {
		rows, err := q.ListSecretVersionIDsBySecretID(ctx, int64(stableID))
		if err != nil {
			return nil, fmt.Errorf("list secret versions: %w", err)
		}
		for _, id := range rows {
			ids[int32(id)] = struct{}{}
		}
		return ids, nil
	}

	rows, err := q.ListConfigVersionIDsByConfigID(ctx, int64(stableID))
	if err != nil {
		return nil, fmt.Errorf("list config versions: %w", err)
	}
	for _, id := range rows {
		ids[int32(id)] = struct{}{}
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
			value.SecretVersionID = &replacementID
		} else {
			value.ConfigVersionID = &replacementID
		}
	}
}

func referencedValueID(value *apigen.EnvVarValue, referenceType versionedValueReferenceType) int32 {
	if value == nil {
		return 0
	}
	if referenceType == secretValueReference {
		if value.SecretVersionID != nil {
			return *value.SecretVersionID
		}
		return 0
	}
	if value.ConfigVersionID != nil {
		return *value.ConfigVersionID
	}
	return 0
}
