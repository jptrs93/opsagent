package values

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
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

// SetVersionedValueWithDeploymentUpdates writes a value version through
// insert, which receives the commit seq and its wall-clock time in unix
// milliseconds and returns the new value version and the update it published
// (nil when it wrote nothing), then repoints the referencing deployments in
// the same commit.
func SetVersionedValueWithDeploymentUpdates(
	store *state.Service,
	referenceType ReferenceType,
	stableID int32,
	updateDeployments bool,
	expected []*apigen.DeploymentExpectedSeq,
	author int32,
	insert func(q *pq.Queries, seq, now int64) (int32, *state.Update, error),
	afterCommit func([]int32),
) ([]int32, error) {
	ctx := context.Background()
	var updatedEvents []*apigen.DeploymentEvent
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		updates, err := prepareDeploymentReferenceUpdates(ctx, q, referenceType, stableID, updateDeployments, expected)
		if err != nil {
			return nil, err
		}
		now := time.Now().UnixMilli()
		newVersion, published, err := insert(q, seq, now)
		if err != nil {
			return nil, err
		}
		if published == nil {
			published = &state.Update{}
		}
		updatedEvents = make([]*apigen.DeploymentEvent, 0, len(updates))
		for _, update := range updates {
			def := update.def
			if !replaceDeploymentReferences(&def.Spec, referenceType, stableID, newVersion) {
				continue
			}
			event, _ := pq.BuildDeploymentUpdateEvent(update.prev, def, author, time.UnixMilli(now))
			event.Seq = seq
			if err := q.InsertDeploymentEvent(ctx, event); err != nil {
				return nil, fmt.Errorf("update deployment %d reference: %w", update.prev.DeploymentID, err)
			}
			updatedEvents = append(updatedEvents, event)
			pq.AppendMutations(published, pq.DeploymentMutation(event))
		}
		return published, nil
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

func prepareDeploymentReferenceUpdates(ctx context.Context, q *pq.Queries, referenceType ReferenceType, stableID int32, updateDeployments bool, expected []*apigen.DeploymentExpectedSeq) ([]deploymentReferenceUpdate, error) {
	if !updateDeployments {
		if len(expected) != 0 {
			return nil, fmt.Errorf("%w: deployment list requires update flag", ErrInvalidReferencingDeployments)
		}
		return nil, nil
	}
	actual := make(map[int32]deploymentReferenceUpdate)
	rows, err := q.ListLatestDeploymentEvents(ctx)
	if err != nil {
		return nil, err
	}
	for _, event := range rows {
		if event.Deleted() {
			continue
		}
		def, err := apigen.DecodeDeployment(event.Value.Encode())
		if err != nil {
			return nil, err
		}
		if !deploymentUsesReferences(&def.Spec, referenceType, stableID) {
			continue
		}
		actual[event.DeploymentID] = deploymentReferenceUpdate{prev: event, def: def}
	}
	seen := make(map[int32]struct{}, len(expected))
	for _, item := range expected {
		if item == nil || item.DeploymentID <= 0 || item.ExpectedSeq < 0 {
			return nil, fmt.Errorf("%w: deployment id must be positive and expected seq non-negative", ErrInvalidReferencingDeployments)
		}
		if _, duplicate := seen[item.DeploymentID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate deployment id %d", ErrInvalidReferencingDeployments, item.DeploymentID)
		}
		seen[item.DeploymentID] = struct{}{}
		current, ok := actual[item.DeploymentID]
		if !ok || (item.ExpectedSeq != 0 && current.prev.Seq > item.ExpectedSeq) {
			return nil, fmt.Errorf("%w: deployment %d changed or no longer references value %d", ErrReferencingDeploymentsChanged, item.DeploymentID, stableID)
		}
	}
	if len(seen) != len(actual) {
		return nil, fmt.Errorf("%w: expected %d deployments, got %d", ErrReferencingDeploymentsChanged, len(actual), len(seen))
	}
	updates := make([]deploymentReferenceUpdate, 0, len(actual))
	for _, item := range expected {
		updates = append(updates, actual[item.DeploymentID])
	}
	return updates, nil
}

func deploymentUsesReferences(spec *apigen.DeploymentSpec, referenceType ReferenceType, stableID int32) bool {
	container := spec.Container()
	if container == nil {
		return false
	}
	for _, value := range container.Runtime.EnvVars {
		if ref := referencedValue(value, referenceType); ref != nil && ref.ID == stableID {
			return true
		}
	}
	return false
}

func replaceDeploymentReferences(spec *apigen.DeploymentSpec, referenceType ReferenceType, stableID, version int32) bool {
	container := spec.Container()
	if container == nil {
		return false
	}
	changed := false
	for _, value := range container.Runtime.EnvVars {
		if ref := referencedValue(value, referenceType); ref != nil && ref.ID == stableID && ref.Version != version {
			ref.Version = version
			changed = true
		}
	}
	return changed
}

func referencedValue(value *apigen.EnvVarValue, referenceType ReferenceType) *apigen.ValueRef {
	if value == nil {
		return nil
	}
	if referenceType == SecretReference {
		return value.Secret
	}
	return value.Config
}
