package nodes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/app/primary/domain/authz"

	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/ptru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

const DefaultSpaceID uint64 = 1

func NormalizedUserSpaceID(spaceID uint64) uint64 {
	if spaceID == 0 {
		return DefaultSpaceID
	}
	return spaceID
}

func InvalidateNodeRuntimeState(store *state.Service, nodeID uint64) (int64, error) {
	if nodeID == 0 {
		return 0, fmt.Errorf("deployment node ID must be positive")
	}
	ctx := context.Background()
	var invalidated int64
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if _, err := q.GetNodeRowByID(ctx, nodeID); err != nil {
			return nil, err
		}
		now := time.Now().UnixMilli()
		update := &state.WriteUpdate{}
		instances, err := q.ListRetainedScheduledInstances(ctx)
		if err != nil {
			return nil, err
		}
		for _, event := range instances {
			inst := event.Value
			if inst.NodeID != nodeID {
				continue
			}
			cfg, err := q.GetDeploymentVersion(ctx, inst.Deployment.DeploymentID, inst.Deployment.Version)
			if err != nil {
				return nil, err
			}
			if internaldeploy.IsSelfConfig(cfg) {
				continue
			}
			previous, err := q.GetLatestScheduledInstanceStatus(ctx, inst.ID)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			tombstone := &apigen.ScheduledInstanceStatus{ScheduledInstanceID: inst.ID, UpdatedAt: previous.UpdatedAt}
			tombstone.BumpUpdatedAt()
			pq.AppendMutations(update, pq.ScheduledInstanceStatusMutation(seq, now, tombstone))
			invalidated++
		}
		previous, err := q.GetLatestNodeStatus(ctx, nodeID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		tombstone := &apigen.NodeStatus{NodeID: nodeID}
		if previous != nil {
			tombstone.UpdatedAt = previous.UpdatedAt
		}
		tombstone.BumpUpdatedAt()
		pq.AppendMutations(update, pq.NodeStatusMutation(seq, now, tombstone))
		return update, nil
	})
	if err != nil {
		return 0, fmt.Errorf("invalidate runtime state for node %d: %w", nodeID, err)
	}
	return invalidated, nil
}

func ListSpaces(q *pq.Queries) []*apigen.Space {
	rows := erru.Must(q.ListSpaces(context.Background()))
	out := make([]*apigen.Space, 0, len(rows))
	for _, row := range rows {
		out = append(out, ptru.To(row))
	}
	return out
}

func spaceMeta(seq, now int64, author int64) pq.EventMeta {
	return pq.EventMeta{GlobalSeq: seq, EventTime: now, Author: author, EventType: apigen.AuthzVerb_AUTHZ_VERB_UPDATE}
}

func CreateSpace(store *state.Service, name string, author int64) (*apigen.Space, error) {
	ctx := context.Background()
	var space *apigen.Space
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_SPACE)
		if err != nil {
			return nil, err
		}
		meta := spaceMeta(seq, time.Now().UnixMilli(), author)
		meta.EventType = apigen.AuthzVerb_AUTHZ_VERB_CREATE
		space = &apigen.Space{ID: id, Name: name}
		nodes, err := updateAllNodeAllowedSpaces(ctx, q, seq, meta.EventTime, func(spaces []uint64) []uint64 { return append(spaces, space.ID) })
		if err != nil {
			return nil, err
		}
		update := pq.NewUpdate(pq.SpaceMutation(meta, *space))
		pq.AppendMutations(update, nodes...)
		return update, nil
	})
	return space, err
}

func UpdateSpace(store *state.Service, id uint64, name string, author int64) (*apigen.Space, error) {
	ctx := context.Background()
	var space *apigen.Space
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if _, err := q.GetSpace(ctx, id); err != nil {
			return nil, err
		}
		space = &apigen.Space{ID: id, Name: name}
		return pq.NewUpdate(pq.SpaceMutation(spaceMeta(seq, time.Now().UnixMilli(), author), *space)), nil
	})
	return space, err
}

// SpaceInUseError refuses a space delete while anything still lives in the
// space or names it by id. Uses lists every kind that stands in the way, so
// the operator sees the whole job at once rather than one kind per attempt.
type SpaceInUseError struct {
	Uses []SpaceUse
}

type SpaceUse struct {
	Singular string
	Plural   string
	Count    int
}

func (e *SpaceInUseError) Error() string {
	parts := make([]string, 0, len(e.Uses))
	for _, use := range e.Uses {
		name := use.Plural
		if use.Count == 1 {
			name = use.Singular
		}
		parts = append(parts, fmt.Sprintf("%d %s", use.Count, name))
	}
	return "Space is still in use: " + strings.Join(parts, ", ")
}

func DeleteSpace(store *state.Service, id uint64, author int64) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if _, err := q.GetSpace(ctx, id); err != nil {
			return nil, err
		}
		if err := requireSpaceUnused(ctx, q, id); err != nil {
			return nil, err
		}
		meta := spaceMeta(seq, time.Now().UnixMilli(), author)
		nodes, err := updateAllNodeAllowedSpaces(ctx, q, seq, meta.EventTime, func(spaces []uint64) []uint64 {
			out := spaces[:0:0]
			for _, space := range spaces {
				if space != id {
					out = append(out, space)
				}
			}
			return out
		})
		if err != nil {
			return nil, err
		}
		update := pq.NewUpdate(pq.DeleteMutation(meta, apigen.CoreEntityType_CORE_ENTITY_SPACE, id))
		pq.AppendMutations(update, nodes...)
		return update, nil
	})
}

// requireSpaceUnused runs under the write lock so nothing can land in the
// space between the check and the delete.
func requireSpaceUnused(ctx context.Context, q *pq.Queries, id uint64) error {
	var uses []SpaceUse
	add := func(singular, plural string, count int) {
		if count > 0 {
			uses = append(uses, SpaceUse{Singular: singular, Plural: plural, Count: count})
		}
	}
	countIn := func(singular, plural string, list func() (int, error)) error {
		count, err := list()
		if err != nil {
			return err
		}
		add(singular, plural, count)
		return nil
	}
	if err := countIn("deployment", "deployments", func() (int, error) {
		rows, err := q.ListActiveDeployments(ctx)
		return countWhere(rows, func(r *apigen.DeploymentRecord) bool { return r.Deployment.SpaceID == id }), err
	}); err != nil {
		return err
	}
	if err := countIn("secret", "secrets", func() (int, error) {
		rows, err := q.ListSecretRows(ctx)
		return countWhere(rows, func(r pq.SecretRow) bool { return r.SpaceID == id }), err
	}); err != nil {
		return err
	}
	if err := countIn("config", "configs", func() (int, error) {
		rows, err := q.ListConfigRows(ctx)
		return countWhere(rows, func(r pq.ConfigRow) bool { return r.SpaceID == id }), err
	}); err != nil {
		return err
	}
	if err := countIn("value directory", "value directories", func() (int, error) {
		rows, err := q.ListValueDirectories(ctx)
		return countWhere(rows, func(r *apigen.ValueDirectory) bool { return r.SpaceID == id }), err
	}); err != nil {
		return err
	}
	if err := countIn("asset", "assets", func() (int, error) {
		rows, err := q.ListAssetRows(ctx)
		return countWhere(rows, func(r pq.AssetRow) bool { return r.SpaceID == id }), err
	}); err != nil {
		return err
	}
	if err := countIn("asset directory", "asset directories", func() (int, error) {
		rows, err := q.ListAssetDirectories(ctx)
		return countWhere(rows, func(r apigen.AssetDirectory) bool { return r.SpaceID == id }), err
	}); err != nil {
		return err
	}
	if err := countIn("network policy", "network policies", func() (int, error) {
		rows, err := q.ListNetworkPolicies(ctx)
		return countWhere(rows, func(r *pq.NetworkPolicyEvent) bool {
			return policyPeerIsSpace(r.Value.Source, id) || policyPeerIsSpace(r.Value.Destination, id)
		}), err
	}); err != nil {
		return err
	}
	refs, err := authz.CountSpaceReferences(ctx, q, id)
	if err != nil {
		return err
	}
	add("access grant", "access grants", refs.Grants)
	add("access grant template", "access grant templates", refs.Templates)
	add("global access rule", "global access rules", refs.GlobalRules)
	if len(uses) == 0 {
		return nil
	}
	return &SpaceInUseError{Uses: uses}
}

func policyPeerIsSpace(peer apigen.NetworkPolicyPeer, id uint64) bool {
	return peer.Target.Value.Space != nil && peer.Target.Value.Space.SpaceID == id
}

func countWhere[T any](rows []T, match func(T) bool) int {
	n := 0
	for _, row := range rows {
		if match(row) {
			n++
		}
	}
	return n
}
