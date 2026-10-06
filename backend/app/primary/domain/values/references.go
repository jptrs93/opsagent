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
	prev *apigen.DeploymentRecord
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
	stableID uint64,
	updateDeployments bool,
	expected []apigen.DeploymentExpectedSeq,
	author int64,
	insert func(q *pq.Queries, seq, now int64) (uint32, *state.WriteUpdate, error),
	afterCommit func([]uint64),
) ([]uint64, error) {
	ctx := context.Background()
	var updatedRecords []*apigen.DeploymentRecord
	if err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
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
			published = &state.WriteUpdate{}
		}
		updatedRecords = make([]*apigen.DeploymentRecord, 0, len(updates))
		for _, update := range updates {
			def := update.def
			if !replaceDeploymentReferences(&def.Spec, referenceType, stableID, newVersion) {
				continue
			}
			record, _ := pq.BuildDeploymentUpdateRecord(update.prev, def, author, time.UnixMilli(now))
			record.Meta.UpdatedSeq = seq
			updatedRecords = append(updatedRecords, record)
			pq.AppendMutations(published, pq.DeploymentMutation(record))
		}
		return published, nil
	}); err != nil {
		return nil, err
	}
	updatedIDs := make([]uint64, 0, len(updatedRecords))
	for _, record := range updatedRecords {
		updatedIDs = append(updatedIDs, record.Deployment.ID)
	}
	if afterCommit != nil {
		afterCommit(updatedIDs)
	}
	return updatedIDs, nil
}

func prepareDeploymentReferenceUpdates(ctx context.Context, q *pq.Queries, referenceType ReferenceType, stableID uint64, updateDeployments bool, expected []apigen.DeploymentExpectedSeq) ([]deploymentReferenceUpdate, error) {
	if !updateDeployments {
		if len(expected) != 0 {
			return nil, fmt.Errorf("%w: deployment list requires update flag", ErrInvalidReferencingDeployments)
		}
		return nil, nil
	}
	actual := make(map[uint64]deploymentReferenceUpdate)
	rows, err := q.ListActiveDeployments(ctx)
	if err != nil {
		return nil, err
	}
	for _, record := range rows {
		def, err := apigen.DecodeDeployment(record.Deployment.Encode())
		if err != nil {
			return nil, err
		}
		if !deploymentUsesReferences(&def.Spec, referenceType, stableID) {
			continue
		}
		actual[record.Deployment.ID] = deploymentReferenceUpdate{prev: record, def: def}
	}
	seen := make(map[uint64]struct{}, len(expected))
	for _, item := range expected {
		if item.DeploymentID == 0 || item.ExpectedSeq < 0 {
			return nil, fmt.Errorf("%w: deployment id must be positive and expected seq non-negative", ErrInvalidReferencingDeployments)
		}
		if _, duplicate := seen[item.DeploymentID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate deployment id %d", ErrInvalidReferencingDeployments, item.DeploymentID)
		}
		seen[item.DeploymentID] = struct{}{}
		current, ok := actual[item.DeploymentID]
		if !ok || (item.ExpectedSeq != 0 && current.prev.Meta.UpdatedSeq > item.ExpectedSeq) {
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

func deploymentUsesReferences(spec *apigen.DeploymentSpec, referenceType ReferenceType, stableID uint64) bool {
	container := spec.Container()
	if container == nil {
		return false
	}
	for _, value := range container.Runtime.EnvVars {
		if id, version := referencedValue(value, referenceType); version != nil && id == stableID {
			return true
		}
	}
	return false
}

func replaceDeploymentReferences(spec *apigen.DeploymentSpec, referenceType ReferenceType, stableID uint64, version uint32) bool {
	container := spec.Container()
	if container == nil {
		return false
	}
	changed := false
	for _, value := range container.Runtime.EnvVars {
		if id, current := referencedValue(value, referenceType); current != nil && id == stableID && *current != version {
			*current = version
			changed = true
		}
	}
	return changed
}

// referencedValue is the id of the value an env var references and a pointer
// to the pinned version, nil when the var is not a reference of that kind.
func referencedValue(value apigen.EnvVar, referenceType ReferenceType) (uint64, *uint32) {
	if referenceType == SecretReference {
		if secret := value.Value.Secret; secret != nil {
			return secret.Secret.SecretID, &secret.Secret.Version
		}
		return 0, nil
	}
	if config := value.Value.Config; config != nil {
		return config.Config.ConfigID, &config.Config.Version
	}
	return 0, nil
}
