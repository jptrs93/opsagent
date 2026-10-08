package nodes

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func testPolicy() *apigen.NetworkPolicy {
	return &apigen.NetworkPolicy{
		Action:      apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW,
		Source:      apigen.NetworkPolicyPeer{Target: apigen.NetworkPolicyPeerTarget{Value: apigen.NetworkPolicyPeerTargetValueOneof{Space: &apigen.SpacePeer{SpaceID: 2}}}},
		Destination: apigen.NetworkPolicyPeer{Target: apigen.NetworkPolicyPeerTarget{Value: apigen.NetworkPolicyPeerTargetValueOneof{Deployment: &apigen.DeploymentPeer{DeploymentID: 7}}}},
		Ports:       []apigen.NetPortMatch{{Protocol: apigen.NetProtocol_NET_PROTOCOL_TCP, Range: apigen.PortRange{Start: 8080, End: 8080}}},
	}
}

func TestNetworkPolicyWriteAdvancesTheSequence(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	createNetworkPolicyForTest(store, testPolicy(), 1)
	policies := erru.Must(store.Queries().ListNetworkPolicies(context.Background()))
	if len(policies) != 1 || policies[0].NetworkPolicyID != 1 {
		t.Fatalf("policies = %+v, want the created policy", policies)
	}
	if globalSeq(t, store) <= 0 {
		t.Fatal("global seq not advanced by the policy write")
	}
}

func createNetworkPolicyForTest(s *state.Service, policy *apigen.NetworkPolicy, author int64) *pq.NetworkPolicyEvent {
	ctx := context.Background()
	now := time.Now().UnixMilli()
	var event *pq.NetworkPolicyEvent
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		id, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY)
		if err != nil {
			return nil, err
		}
		value := *policy
		event = &pq.NetworkPolicyEvent{Seq: seq, EventTime: now, CreatedTime: now, Author: author, NetworkPolicyID: id, Value: value}
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: now, Author: author, EventType: apigen.AuthzVerb_AUTHZ_VERB_CREATE}
		return pq.NewUpdate(pq.NetworkPolicyMutation(meta, id, value)), nil
	}))
	return event
}
