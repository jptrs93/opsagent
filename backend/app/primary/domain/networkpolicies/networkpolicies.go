package networkpolicies

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

var InvalidErr = apigen.NewApiErr("Invalid network policy", "invalid_network_policy", http.StatusBadRequest)
var DenyUnsupportedErr = apigen.NewApiErr("Deny policies are not implemented yet", "network_policy_deny_unsupported", http.StatusBadRequest)
var RedundantErr = apigen.NewApiErr("Same-space traffic is always allowed; this rule is redundant", "network_policy_redundant", http.StatusBadRequest)
var PeerNotFoundErr = apigen.NewApiErr("Network policy peer not found", "network_policy_peer_not_found", http.StatusNotFound)
var NotFoundErr = apigen.NewApiErr("Network policy not found", "network_policy_not_found", http.StatusNotFound)
var VersionConflictErr = apigen.NewApiErr("Network policy was modified concurrently", "network_policy_version_conflict", http.StatusConflict)

func ByID(q *pq.Queries, id int32) *pq.NetworkPolicyEvent {
	row, err := q.GetNetworkPolicy(context.Background(), int64(id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		panic(err)
	}
	return row
}

func ValidatePeerRef(ref *apigen.NetworkPolicyPeerRef) error {
	if ref == nil {
		return InvalidErr
	}
	switch ref.Kind {
	case apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE:
		if ref.ID < 0 || ref.ID > network.MaxSpaceID {
			return InvalidErr
		}
	case apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_DEPLOYMENT:
		if ref.ID < 1 || ref.ID > network.MaxDeploymentID {
			return InvalidErr
		}
	default:
		return InvalidErr
	}
	return nil
}

func ValidateShape(policy *apigen.NetworkPolicy) error {
	if policy.Action == apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_DENY {
		return DenyUnsupportedErr
	}
	if policy.Action != apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW {
		return InvalidErr
	}
	if err := ValidatePeerRef(policy.Source); err != nil {
		return err
	}
	if err := ValidatePeerRef(policy.Destination); err != nil {
		return err
	}
	for _, port := range policy.Ports {
		if network.ValidateNetPortMatch(port) != nil {
			return InvalidErr
		}
	}
	return nil
}

func PeerSpace(q *pq.Queries, ref *apigen.NetworkPolicyPeerRef) (int32, bool) {
	if ref == nil {
		return 0, false
	}
	switch ref.Kind {
	case apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE:
		for _, space := range nodes.ListSpaces(q) {
			if space != nil && space.ID == ref.ID {
				return ref.ID, true
			}
		}
	case apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_DEPLOYMENT:
		cfg, err := q.GetLatestDeploymentEvent(context.Background(), int64(ref.ID))
		if err == nil && !cfg.Deleted() {
			return cfg.Value.SpaceID, true
		}
	}
	return 0, false
}

func view(seq, now, createdTime int64, author, id int32, policy apigen.NetworkPolicy) *pq.NetworkPolicyEvent {
	return &pq.NetworkPolicyEvent{NetworkPolicyID: id, Seq: seq, Author: author, CreatedTime: createdTime, EventTime: now, Value: policy}
}

func mutation(e *pq.NetworkPolicyEvent, verb apigen.AuthzVerb) pq.Mutation {
	meta := pq.EventMeta{GlobalSeq: e.Seq, EventTime: e.EventTime, Author: int64(e.Author), EventType: verb}
	return pq.NetworkPolicyMutation(meta, int64(e.NetworkPolicyID), e.Value)
}

func Create(store *state.Service, author int32, policy *apigen.NetworkPolicy) (*pq.NetworkPolicyEvent, error) {
	ctx := context.Background()
	var created *pq.NetworkPolicyEvent
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY)
		if err != nil {
			return nil, err
		}
		now := time.Now().UnixMilli()
		created = view(seq, now, now, author, int32(id), *policy)
		return pq.NewUpdate(mutation(created, apigen.AuthzVerb_AUTHZ_VERB_CREATE)), nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// Update replaces the policy as long as it has no write newer than
// expectedSeq; zero skips the check.
func Update(store *state.Service, id int32, expectedSeq int64, author int32, policy *apigen.NetworkPolicy) (*pq.NetworkPolicyEvent, error) {
	ctx := context.Background()
	var updated *pq.NetworkPolicyEvent
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		prev, err := livePrevious(ctx, q, id)
		if err != nil {
			return nil, err
		}
		if expectedSeq != 0 && prev.Seq > expectedSeq {
			return nil, VersionConflictErr
		}
		updated = view(seq, time.Now().UnixMilli(), prev.CreatedTime, author, id, *policy)
		return pq.NewUpdate(mutation(updated, apigen.AuthzVerb_AUTHZ_VERB_UPDATE)), nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func Delete(store *state.Service, id, author int32) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		if _, err := livePrevious(ctx, q, id); err != nil {
			return nil, err
		}
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: time.Now().UnixMilli(), Author: int64(author)}
		return pq.NewUpdate(pq.DeleteMutation(meta, apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY, int64(id))), nil
	})
}

func livePrevious(ctx context.Context, q *pq.Queries, id int32) (*pq.NetworkPolicyEvent, error) {
	prev, err := q.GetNetworkPolicy(ctx, int64(id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundErr
	}
	if err != nil {
		return nil, err
	}
	return prev, nil
}
