package nodeaccesspolicies

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/nodeaccess"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var InvalidErr = apigen.NewApiErr("Invalid node access policy", "invalid_node_access_policy", http.StatusBadRequest)
var NotFoundErr = apigen.NewApiErr("Node access policy not found", "node_access_policy_not_found", http.StatusNotFound)
var VersionConflictErr = apigen.NewApiErr("Node access policy was modified concurrently", "node_access_policy_version_conflict", http.StatusConflict)
var DuplicateNameErr = apigen.NewApiErr("A node access policy with this name already exists", "node_access_policy_duplicate_name", http.StatusConflict)
var SpaceNotFoundErr = apigen.NewApiErr("Node access policy names a space that does not exist", "node_access_policy_space_not_found", http.StatusNotFound)
var NodeNotFoundErr = apigen.NewApiErr("Node access policy names a node that does not exist", "node_access_policy_node_not_found", http.StatusNotFound)
var PlacementsInUseErr = apigen.NewApiErr("Node access policies must keep every placed deployment allowed", "node_access_policy_placements_in_use", http.StatusConflict)
var BuiltinErr = apigen.NewApiErr("A built-in node access policy can only be enabled or disabled", "node_access_policy_builtin", http.StatusConflict)
var AlwaysEnabledErr = apigen.NewApiErr("This built-in node access policy cannot be disabled", "node_access_policy_always_enabled", http.StatusConflict)

func invalidf(format string, args ...any) error {
	e := InvalidErr
	e.DisplayErr = "Invalid node access policy: " + fmt.Sprintf(format, args...)
	return e
}

func Normalize(policy *apigen.NodeAccessPolicy) error {
	if err := nodeaccess.Normalize(policy); err != nil {
		return invalidf("%s", err)
	}
	return nil
}

func ByID(q *pq.Queries, id uint64) *pq.NodeAccessPolicyEvent {
	row, err := q.GetNodeAccessPolicy(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		panic(err)
	}
	return row
}

func view(seq, now, createdTime, author int64, id uint64, policy apigen.NodeAccessPolicy) *pq.NodeAccessPolicyEvent {
	policy.ID = id
	return &pq.NodeAccessPolicyEvent{NodeAccessPolicyID: id, Seq: seq, Author: author, CreatedTime: createdTime, EventTime: now, Value: policy}
}

func Create(store *state.Service, author int64, policy *apigen.NodeAccessPolicy) (*pq.NodeAccessPolicyEvent, error) {
	ctx := context.Background()
	var created *pq.NodeAccessPolicyEvent
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		current, err := q.NodeAccessPolicyValues(ctx)
		if err != nil {
			return nil, err
		}
		next := *policy
		next.Builtin = apigen.NodeAccessBuiltin_NODE_ACCESS_BUILTIN_NONE
		if err := validateWrite(ctx, q, current, 0, &next); err != nil {
			return nil, err
		}
		id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_NODE_ACCESS_POLICY)
		if err != nil {
			return nil, err
		}
		now := time.Now().UnixMilli()
		created = view(seq, now, now, author, id, next)
		return pq.NewUpdate(created.Mutation(apigen.AuthzVerb_AUTHZ_VERB_CREATE)), nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

func Update(store *state.Service, id uint64, expectedSeq int64, author int64, policy *apigen.NodeAccessPolicy) (*pq.NodeAccessPolicyEvent, error) {
	ctx := context.Background()
	var updated *pq.NodeAccessPolicyEvent
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		prev, err := livePrevious(ctx, q, id)
		if err != nil {
			return nil, err
		}
		if expectedSeq != 0 && prev.Seq > expectedSeq {
			return nil, VersionConflictErr
		}
		next := *policy
		next.Builtin = prev.Value.Builtin
		if next.Builtin != apigen.NodeAccessBuiltin_NODE_ACCESS_BUILTIN_NONE {
			if !nodeaccess.SameDefinition(prev.Value, next) {
				return nil, BuiltinErr
			}
			if next.Disabled && next.Builtin == apigen.NodeAccessBuiltin_NODE_ACCESS_BUILTIN_SYSTEM {
				return nil, AlwaysEnabledErr
			}
		}
		current, err := q.NodeAccessPolicyValues(ctx)
		if err != nil {
			return nil, err
		}
		if err := validateWrite(ctx, q, current, id, &next); err != nil {
			return nil, err
		}
		updated = view(seq, time.Now().UnixMilli(), prev.CreatedTime, author, id, next)
		return pq.NewUpdate(updated.Mutation(apigen.AuthzVerb_AUTHZ_VERB_UPDATE)), nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func Delete(store *state.Service, id uint64, author int64) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		prev, err := livePrevious(ctx, q, id)
		if err != nil {
			return nil, err
		}
		if prev.Value.Builtin != apigen.NodeAccessBuiltin_NODE_ACCESS_BUILTIN_NONE {
			return nil, BuiltinErr
		}
		current, err := q.NodeAccessPolicyValues(ctx)
		if err != nil {
			return nil, err
		}
		if err := requirePlacementsAllowed(ctx, q, without(current, id)); err != nil {
			return nil, err
		}
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), Author: author}
		return pq.NewUpdate(pq.DeleteMutation(meta, apigen.CoreEntityType_CORE_ENTITY_NODE_ACCESS_POLICY, id)), nil
	})
}

func EnsureBuiltins(store *state.Service) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		rows, err := q.ListNodeAccessPolicies(ctx)
		if err != nil {
			return nil, err
		}
		held := map[apigen.NodeAccessBuiltin]*pq.NodeAccessPolicyEvent{}
		for _, row := range rows {
			held[row.Value.Builtin] = row
		}
		now := time.Now().UnixMilli()
		var mutations []pq.Mutation
		for _, want := range nodeaccess.Builtins() {
			prev := held[want.Builtin]
			if prev == nil {
				id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_NODE_ACCESS_POLICY)
				if err != nil {
					return nil, err
				}
				mutations = append(mutations, view(seq, now, now, 0, id, want).Mutation(apigen.AuthzVerb_AUTHZ_VERB_CREATE))
				continue
			}
			want.Disabled = prev.Value.Disabled && want.Builtin != apigen.NodeAccessBuiltin_NODE_ACCESS_BUILTIN_SYSTEM
			if nodeaccess.SameDefinition(prev.Value, want) && prev.Value.Disabled == want.Disabled {
				continue
			}
			mutations = append(mutations, view(seq, now, prev.CreatedTime, 0, prev.NodeAccessPolicyID, want).Mutation(apigen.AuthzVerb_AUTHZ_VERB_UPDATE))
		}
		if len(mutations) == 0 {
			return nil, nil
		}
		return pq.NewUpdate(mutations...), nil
	})
}

func livePrevious(ctx context.Context, q *pq.Queries, id uint64) (*pq.NodeAccessPolicyEvent, error) {
	prev, err := q.GetNodeAccessPolicy(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundErr
	}
	if err != nil {
		return nil, err
	}
	return prev, nil
}

func without(policies []apigen.NodeAccessPolicy, id uint64) []apigen.NodeAccessPolicy {
	out := make([]apigen.NodeAccessPolicy, 0, len(policies))
	for _, p := range policies {
		if p.ID != id {
			out = append(out, p)
		}
	}
	return out
}

func validateWrite(ctx context.Context, q *pq.Queries, current []apigen.NodeAccessPolicy, id uint64, policy *apigen.NodeAccessPolicy) error {
	others := without(current, id)
	for _, other := range others {
		if other.Name == policy.Name {
			return DuplicateNameErr
		}
	}
	for _, spaceID := range nodeaccess.NamedSpaces(policy) {
		if _, err := q.GetSpace(ctx, spaceID); errors.Is(err, sql.ErrNoRows) {
			return SpaceNotFoundErr
		} else if err != nil {
			return err
		}
	}
	for _, nodeID := range nodeaccess.NamedNodes(policy) {
		if _, err := q.GetNodeRowByID(ctx, nodeID); errors.Is(err, sql.ErrNoRows) {
			return NodeNotFoundErr
		} else if err != nil {
			return err
		}
	}
	next := *policy
	next.ID = id
	return requirePlacementsAllowed(ctx, q, append(others, next))
}

func requirePlacementsAllowed(ctx context.Context, q *pq.Queries, policies []apigen.NodeAccessPolicy) error {
	deployments, err := q.ListActiveDeployments(ctx)
	if err != nil {
		return err
	}
	spaceNames := map[uint64]string{}
	nodeNames := map[uint64]string{}
	var stranded []string
	for _, record := range deployments {
		dep := &record.Deployment
		nodeID := dep.PlacementNodeID()
		if nodeaccess.Decide(policies, nodeID, nodeaccess.Run, dep.SpaceID).Allowed {
			continue
		}
		spaceName, ok := spaceNames[dep.SpaceID]
		if !ok {
			space, err := q.GetSpace(ctx, dep.SpaceID)
			if err != nil {
				return err
			}
			spaceName = space.Name
			spaceNames[dep.SpaceID] = spaceName
		}
		nodeName, ok := nodeNames[nodeID]
		if !ok {
			node, err := q.GetNodeRowByID(ctx, nodeID)
			if err != nil {
				return err
			}
			nodeName = node.Event.Value.Operator.Name
			nodeNames[nodeID] = nodeName
		}
		stranded = append(stranded, fmt.Sprintf("deployment %q of space %q on node %q", dep.Name, spaceName, nodeName))
	}
	if len(stranded) == 0 {
		return nil
	}
	const maxShown = 5
	total := len(stranded)
	if total > maxShown {
		stranded = append(stranded[:maxShown:maxShown], fmt.Sprintf("%d more", total-maxShown))
	}
	e := PlacementsInUseErr
	e.DisplayErr = "This change would leave deployments on nodes that may no longer run them: " + strings.Join(stranded, "; ")
	return e
}
