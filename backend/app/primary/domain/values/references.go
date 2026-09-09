package values

import (
	"context"
	"errors"
	"fmt"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var ErrInvalidReferencingDeployments = errors.New("invalid referencing deployments")
var ErrReferencingDeploymentsChanged = errors.New("referencing deployments changed")

type ReferenceType uint8

const (
	SecretReference ReferenceType = iota + 1
	ConfigReference
)

type deploymentReferenceUpdate struct {
	prev *apigen.DeploymentEvent
	def  *apigen.Deployment
}

func SetVersionedValueWithDeploymentUpdates(
	store *state.Service,
	referenceType ReferenceType,
	stableID int32,
	updateDeployments bool,
	expected []storage.DeploymentSpecVersion,
	author int32,
	insert func(*pq.Queries, int64) (int32, apigen.CoreUpdate, error),
	afterCommit func([]int32),
) ([]int32, error) {
	ctx := context.Background()
	var updatedEvents []*apigen.DeploymentEvent
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		updates, referenceIDs, err := prepareDeploymentReferenceUpdates(ctx, q, referenceType, stableID, updateDeployments, expected)
		if err != nil {
			return nil, err
		}
		newID, published, err := insert(q, seq)
		if err != nil {
			return nil, err
		}
		updatedEvents = make([]*apigen.DeploymentEvent, 0, len(updates))
		for _, update := range updates {
			def := update.def
			replaceDeploymentReferences(&def.Spec, referenceType, referenceIDs, newID)
			event := pq.BuildDeploymentUpdateEvent(update.prev, def, author)
			event.Seq = seq
			if err := q.InsertDeploymentEvent(ctx, event); err != nil {
				return nil, fmt.Errorf("update deployment %d reference: %w", update.prev.DeploymentID, err)
			}
			updatedEvents = append(updatedEvents, event)
			published.DeploymentEvents = append(published.DeploymentEvents, event)
		}
		return &published, nil
	}); err != nil {
		return nil, err
	}
	updatedIDs := make([]int32, 0, len(updatedEvents))
	for _, event := range updatedEvents {
		updatedIDs = append(updatedIDs, event.DeploymentID)
	}
	if afterCommit != nil {
		afterCommit(updatedIDs)
	}
	return updatedIDs, nil
}

func prepareDeploymentReferenceUpdates(ctx context.Context, q *pq.Queries, referenceType ReferenceType, stableID int32, updateDeployments bool, expected []storage.DeploymentSpecVersion) ([]deploymentReferenceUpdate, map[int32]struct{}, error) {
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
	rows, err := q.ListLatestDeploymentEvents(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, event := range rows {
		if event.Deleted() {
			continue
		}
		def, err := apigen.DecodeDeployment(event.Value.Encode())
		if err != nil {
			return nil, nil, err
		}
		if !deploymentUsesReferences(&def.Spec, referenceType, referenceIDs) {
			continue
		}
		actual[event.DeploymentID] = deploymentReferenceUpdate{prev: event, def: def}
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
		if !ok || current.prev.SpecVersion != item.SpecVersion {
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

func versionedValueIDs(ctx context.Context, q *pq.Queries, referenceType ReferenceType, stableID int32) (map[int32]struct{}, error) {
	ids := make(map[int32]struct{})
	var rows []int64
	var err error
	if referenceType == SecretReference {
		rows, err = q.ListSecretVersionIDsBySecretID(ctx, int64(stableID))
	} else {
		rows, err = q.ListConfigVersionIDsByConfigID(ctx, int64(stableID))
	}
	if err != nil {
		return nil, fmt.Errorf("list value versions: %w", err)
	}
	for _, id := range rows {
		ids[int32(id)] = struct{}{}
	}
	return ids, nil
}

func deploymentUsesReferences(spec *apigen.DeploymentSpec, referenceType ReferenceType, ids map[int32]struct{}) bool {
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

func replaceDeploymentReferences(spec *apigen.DeploymentSpec, referenceType ReferenceType, referenceIDs map[int32]struct{}, replacementID int32) {
	container := spec.Container()
	if container == nil {
		return
	}
	for _, value := range container.Runtime.EnvVars {
		if _, ok := referenceIDs[referencedValueID(value, referenceType)]; !ok {
			continue
		}
		if referenceType == SecretReference {
			value.SecretVersionID = &replacementID
		} else {
			value.ConfigVersionID = &replacementID
		}
	}
}

func referencedValueID(value *apigen.EnvVarValue, referenceType ReferenceType) int32 {
	if value == nil {
		return 0
	}
	if referenceType == SecretReference {
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
