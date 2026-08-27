package state

import (
	"context"
	"errors"
	"fmt"
	"time"

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

func networkPolicyRowToProto(row pq.NetworkPolicyRow) *apigen.NetworkPolicy {
	policy, err := apigen.DecodeNetworkPolicy(row.DataBlob)
	if err != nil {
		panic(fmt.Sprintf("decoding network policy %d: %v", row.ID, err))
	}
	policy.ID = int32(row.ID)
	policy.Version = int32(row.Version)
	policy.Deleted = row.DeletedAt != 0
	return policy
}

func (s *Service) ListNetworkPolicies() []*apigen.NetworkPolicy {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.listNetworkPoliciesLocked(false)
}

func (s *Service) listNetworkPoliciesLocked(includeDeleted bool) []*apigen.NetworkPolicy {
	rows, err := s.q.ListNetworkPolicyRows(context.Background())
	if err != nil {
		panic(fmt.Sprintf("ListNetworkPolicyRows: %v", err))
	}
	out := make([]*apigen.NetworkPolicy, 0, len(rows))
	for _, row := range rows {
		if !includeDeleted && row.DeletedAt != 0 {
			continue
		}
		out = append(out, networkPolicyRowToProto(row))
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

func (s *Service) CreateNetworkPolicy(policy *apigen.NetworkPolicy, author int64) (*apigen.NetworkPolicy, error) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	ctx := context.Background()
	blob := networkPolicyContentBlob(policy)
	var id int64
	err := s.q.Tx(ctx, func(q *pq.Queries) error {
		var err error
		id, err = q.CreateNetworkPolicyRow(ctx)
		if err != nil {
			return err
		}
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			return err
		}
		return q.AppendNetworkPolicyVersion(ctx, pq.AppendNetworkPolicyVersionParams{
			PolicyID:  id,
			CreatedAt: time.Now().UnixMilli(),
			Author:    author,
			DataBlob:  blob,
			GlobalSeq: seq,
		})
	})
	if err != nil {
		return nil, err
	}
	stored := networkPolicyRowToProto(pq.NetworkPolicyRow{ID: id, Version: 1, DataBlob: blob})
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
	err := s.q.Tx(ctx, func(q *pq.Queries) error {
		seq, err := q.NextGlobalSeq(ctx)
		if err != nil {
			return err
		}
		return q.AppendNetworkPolicyVersion(ctx, pq.AppendNetworkPolicyVersionParams{
			PolicyID:  int64(id),
			CreatedAt: time.Now().UnixMilli(),
			Author:    author,
			DataBlob:  blob,
			GlobalSeq: seq,
		})
	})
	if err != nil {
		return nil, err
	}
	stored := networkPolicyRowToProto(pq.NetworkPolicyRow{ID: int64(id), Version: int64(current.Version) + 1, DataBlob: blob})
	s.networkPolicySubs.Notify(*stored)
	return stored, nil
}

func (s *Service) DeleteNetworkPolicy(id int32) error {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	current := s.getNetworkPolicyLocked(id)
	if current == nil || current.Deleted {
		return ErrNotFound
	}
	ctx := context.Background()
	err := s.q.Tx(ctx, func(q *pq.Queries) error {
		if err := q.SetNetworkPolicyDeletedAt(ctx, pq.SetNetworkPolicyDeletedAtParams{
			DeletedAt: time.Now().UnixMilli(),
			ID:        int64(id),
		}); err != nil {
			return err
		}
		_, err := q.NextGlobalSeq(ctx)
		return err
	})
	if err != nil {
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
