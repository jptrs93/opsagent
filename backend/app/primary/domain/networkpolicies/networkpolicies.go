package networkpolicies

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/jptrs93/goutil/erru"
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

func List(q *pq.Queries) []*apigen.NetworkPolicyEvent {
	return erru.Must(q.ListLatestLiveNetworkPolicyEvents(context.Background()))
}

func ByID(q *pq.Queries, id int32) *apigen.NetworkPolicyEvent {
	row, err := q.GetLatestNetworkPolicyEvent(context.Background(), int64(id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		panic(err)
	}
	if row.EventType == apigen.EventType_EVENT_TYPE_DELETE {
		return nil
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

func Create(store *state.Service, author int32, policy *apigen.NetworkPolicy) (*apigen.NetworkPolicyEvent, error) {
	ctx := context.Background()
	now := time.Now().UnixMilli()
	var event *apigen.NetworkPolicyEvent
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		id, err := q.NextNetworkPolicyID(ctx)
		if err != nil {
			return nil, err
		}
		event = &apigen.NetworkPolicyEvent{
			Seq:             seq,
			EventTime:       now,
			CreatedTime:     now,
			Author:          author,
			NetworkPolicyID: int32(id),
			Version:         1,
			Value:           *policy,
			EventType:       apigen.EventType_EVENT_TYPE_CREATE,
		}
		if err := q.InsertNetworkPolicyEvent(ctx, event); err != nil {
			return nil, err
		}
		return &state.Update{NetworkPolicyEvents: []*apigen.NetworkPolicyEvent{event}}, nil
	})
	if err != nil {
		return nil, err
	}
	return erru.Must(store.Queries().GetLatestNetworkPolicyEvent(ctx, int64(event.NetworkPolicyID))), nil
}

func Update(store *state.Service, id, expectedVersion, author int32, policy *apigen.NetworkPolicy) (*apigen.NetworkPolicyEvent, error) {
	ctx := context.Background()
	var updated *apigen.NetworkPolicyEvent
	err := store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		prev, err := livePrevious(ctx, q, id)
		if err != nil {
			return nil, err
		}
		if prev.Version != expectedVersion {
			return nil, VersionConflictErr
		}
		updated = &apigen.NetworkPolicyEvent{Seq: seq, EventTime: time.Now().UnixMilli(), CreatedTime: prev.CreatedTime, Author: author, NetworkPolicyID: id, Version: prev.Version + 1, Value: *policy, EventType: apigen.EventType_EVENT_TYPE_UPDATE}
		if err := q.InsertNetworkPolicyEvent(ctx, updated); err != nil {
			return nil, err
		}
		return &state.Update{NetworkPolicyEvents: []*apigen.NetworkPolicyEvent{updated}}, nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func Delete(store *state.Service, id, author int32) error {
	ctx := context.Background()
	return store.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		prev, err := livePrevious(ctx, q, id)
		if err != nil {
			return nil, err
		}
		event := &apigen.NetworkPolicyEvent{Seq: seq, EventTime: time.Now().UnixMilli(), CreatedTime: prev.CreatedTime, Author: author, NetworkPolicyID: id, Version: prev.Version + 1, Value: prev.Value, EventType: apigen.EventType_EVENT_TYPE_DELETE}
		if err := q.InsertNetworkPolicyEvent(ctx, event); err != nil {
			return nil, err
		}
		return &state.Update{NetworkPolicyEvents: []*apigen.NetworkPolicyEvent{event}}, nil
	})
}

func livePrevious(ctx context.Context, q *pq.Queries, id int32) (*apigen.NetworkPolicyEvent, error) {
	prev, err := q.GetLatestNetworkPolicyEvent(ctx, int64(id))
	if errors.Is(err, sql.ErrNoRows) || err == nil && prev.EventType == apigen.EventType_EVENT_TYPE_DELETE {
		return nil, NotFoundErr
	}
	if err != nil {
		return nil, err
	}
	return prev, nil
}
