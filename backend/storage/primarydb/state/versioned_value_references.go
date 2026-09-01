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
	prev pq.DeploymentEvent
	next *apigen.Deployment
}

// setVersionedValueWithDeploymentUpdates appends a version of the stable
// secret/config identity stableID and optionally rolls the caller-asserted
// deployment references (which pin version row ids of that identity) to the
// new row atomically.
func (s *Service) setVersionedValueWithDeploymentUpdates(
	referenceType versionedValueReferenceType,
	stableID int32,
	updateDeployments bool,
	expected []storage.DeploymentSpecVersion,
	author int32,
	insert func(*pq.Queries, int64) (int32, error),
	afterCommit func([]int32),
) ([]int32, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()

	ctx := context.Background()
	now := time.Now()
	var updatedEvents []pq.DeploymentEvent
	if err := s.q.Tx(ctx, func(q *pq.Queries) error {
		updates, referenceIDs, err := s.prepareDeploymentReferenceUpdatesLocked(ctx, q, referenceType, stableID, updateDeployments, expected)
		if err != nil {
			return err
		}
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			return err
		}
		newID, err := insert(q, seq)
		if err != nil {
			return err
		}

		updatedEvents = make([]pq.DeploymentEvent, 0, len(updates))
		for _, update := range updates {
			next := update.next
			replaceDeploymentReferences(&next.Spec, referenceType, referenceIDs, newID)
			next.Version++
			next.SpecVersion++
			next.UpdatedAt = now
			next.Author = author
			event := buildDeploymentEvent(update.prev, true, next, pq.DeploymentEventUpdate, "deployment reference update")
			event.GlobalSeq = seq
			if err := q.InsertDeploymentEvent(ctx, event); err != nil {
				return fmt.Errorf("update deployment %d reference: %w", next.ID, err)
			}
			updatedEvents = append(updatedEvents, event)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	updatedIDs := make([]int32, 0, len(updatedEvents))
	for _, event := range updatedEvents {
		cfg := deploymentEventToProto(event)
		s.deploymentCache[cfg.ID] = cfg
		s.latestEvents[cfg.ID] = event
		updatedIDs = append(updatedIDs, cfg.ID)
	}
	if afterCommit != nil {
		afterCommit(updatedIDs)
	}
	for _, id := range updatedIDs {
		s.notifyDeploymentLocked(id)
	}
	return updatedIDs, nil
}

func (s *Service) prepareDeploymentReferenceUpdatesLocked(
	ctx context.Context,
	q *pq.Queries,
	referenceType versionedValueReferenceType,
	stableID int32,
	updateDeployments bool,
	expected []storage.DeploymentSpecVersion,
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
	actual := make(map[int32]deploymentReferenceUpdate)
	for _, cfg := range s.deploymentCache {
		// Deletion is soft and preserves the spec, so a tombstone keeps whatever
		// references it held when it was deleted. It will never run again, so
		// rewriting it is pointless — and counting it here would make every
		// rotation fail against the live set the caller can see.
		if cfg.Deleted {
			continue
		}
		event, ok := s.latestEvents[cfg.ID]
		if !ok {
			return nil, nil, fmt.Errorf("deployment %d has no latest event", cfg.ID)
		}
		next := deploymentEventToProto(event)
		if !deploymentUsesReferences(&next.Spec, referenceType, referenceIDs) {
			continue
		}
		actual[cfg.ID] = deploymentReferenceUpdate{prev: event, next: next}
	}

	seen := make(map[int32]struct{}, len(expected))
	for _, item := range expected {
		if item.ID <= 0 || item.SpecVersion <= 0 {
			return nil, nil, fmt.Errorf("%w: deployment id and version must be positive", ErrInvalidReferencingDeployments)
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return nil, nil, fmt.Errorf("%w: duplicate deployment id %d", ErrInvalidReferencingDeployments, item.ID)
		}
		seen[item.ID] = struct{}{}
		current, ok := actual[item.ID]
		if !ok || current.next.SpecVersion != item.SpecVersion {
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
		return nil, fmt.Errorf("list spec versions: %w", err)
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
