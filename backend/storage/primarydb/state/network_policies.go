package state

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

var ErrVersionConflict = errors.New("version conflict")

func networkPolicyContentBlob(policy *apigen.NetworkPolicy) []byte {
	content := apigen.NetworkPolicy{
		Action:      policy.Action,
		Source:      policy.Source,
		Destination: policy.Destination,
		Ports:       policy.Ports,
	}
	return content.Encode()
}

func networkPolicyEventToProto(e pq.NetworkPolicyEvent) *apigen.NetworkPolicy {
	policy, err := apigen.DecodeNetworkPolicy(e.DataBlob)
	if err != nil {
		panic(fmt.Sprintf("decoding network policy %d: %v", e.PolicyID, err))
	}
	policy.ID = int32(e.PolicyID)
	policy.Version = int32(e.Version)
	policy.Deleted = e.EventType == pq.EventDelete
	return policy
}

func (s *Service) ListNetworkPolicies() []*apigen.NetworkPolicy {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.listNetworkPoliciesLocked(false)
}

func (s *Service) listNetworkPoliciesLocked(includeDeleted bool) []*apigen.NetworkPolicy {
	events := erru.Must(s.q.ListLatestNetworkPolicyEvents(context.Background()))
	out := make([]*apigen.NetworkPolicy, 0, len(events))
	for _, e := range events {
		if !includeDeleted && e.EventType == pq.EventDelete {
			continue
		}
		out = append(out, networkPolicyEventToProto(e))
	}
	return out
}

func (s *Service) GetNetworkPolicy(id int32) *apigen.NetworkPolicy {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	policy := s.getNetworkPolicyLocked(id)
	if policy == nil || policy.Deleted {
		return nil
	}
	return policy
}

func (s *Service) getNetworkPolicyLocked(id int32) *apigen.NetworkPolicy {
	for _, policy := range s.listNetworkPoliciesLocked(true) {
		if policy.ID == id {
			return policy
		}
	}
	return nil
}

func (s *Service) appendNetworkPolicyEventLocked(ctx context.Context, event pq.NetworkPolicyEvent) error {
	return s.q.Tx(ctx, func(q *pq.Queries) error {
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			return err
		}
		event.GlobalSeq = seq
		return q.InsertNetworkPolicyEvent(ctx, event)
	})
}

func (s *Service) CreateNetworkPolicy(policy *apigen.NetworkPolicy, author int64) (*apigen.NetworkPolicy, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	ctx := context.Background()
	blob := networkPolicyContentBlob(policy)
	now := time.Now().UnixMilli()
	var id int64
	err := s.q.Tx(ctx, func(q *pq.Queries) error {
		var err error
		id, err = q.NextNetworkPolicyID(ctx)
		if err != nil {
			return err
		}
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			return err
		}
		return q.InsertNetworkPolicyEvent(ctx, pq.NetworkPolicyEvent{
			GlobalSeq:   seq,
			EventTime:   now,
			CreatedTime: now,
			Author:      author,
			PolicyID:    id,
			Version:     1,
			DataBlob:    blob,
			EventType:   pq.EventCreate,
		})
	})
	if err != nil {
		return nil, err
	}
	stored := networkPolicyEventToProto(pq.NetworkPolicyEvent{PolicyID: id, Version: 1, DataBlob: blob, EventType: pq.EventCreate})
	s.networkPolicySubs.Notify(*stored)
	return stored, nil
}

func (s *Service) UpdateNetworkPolicy(id, expectedVersion int32, policy *apigen.NetworkPolicy, author int64) (*apigen.NetworkPolicy, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	current := s.getNetworkPolicyLocked(id)
	if current == nil || current.Deleted {
		return nil, ErrNotFound
	}
	if current.Version != expectedVersion {
		return nil, ErrVersionConflict
	}
	ctx := context.Background()
	blob := networkPolicyContentBlob(policy)
	prev := erru.Must(s.q.GetLatestNetworkPolicyEvent(ctx, int64(id)))
	event := pq.NetworkPolicyEvent{
		EventTime:   time.Now().UnixMilli(),
		CreatedTime: prev.CreatedTime,
		Author:      author,
		PolicyID:    int64(id),
		Version:     prev.Version + 1,
		DataBlob:    blob,
		EventType:   pq.EventUpdate,
	}
	if err := s.appendNetworkPolicyEventLocked(ctx, event); err != nil {
		return nil, err
	}
	stored := networkPolicyEventToProto(event)
	s.networkPolicySubs.Notify(*stored)
	return stored, nil
}

func (s *Service) DeleteNetworkPolicy(id int32, author int64) error {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	current := s.getNetworkPolicyLocked(id)
	if current == nil || current.Deleted {
		return ErrNotFound
	}
	ctx := context.Background()
	prev := erru.Must(s.q.GetLatestNetworkPolicyEvent(ctx, int64(id)))
	event := pq.NetworkPolicyEvent{
		EventTime:   time.Now().UnixMilli(),
		CreatedTime: prev.CreatedTime,
		Author:      author,
		PolicyID:    int64(id),
		Version:     prev.Version + 1,
		DataBlob:    notNullBlob(prev.DataBlob),
		EventType:   pq.EventDelete,
	}
	if err := s.appendNetworkPolicyEventLocked(ctx, event); err != nil {
		return err
	}
	tombstone := *current
	tombstone.Deleted = true
	s.networkPolicySubs.Notify(tombstone)
	return nil
}

func (s *Service) SubscribeNetworkPolicyUpdates() (*pubsubu.Sub[apigen.NetworkPolicy], func()) {
	sub := s.networkPolicySubs.Subscribe(nil)
	return sub, sub.Unsubscribe
}
