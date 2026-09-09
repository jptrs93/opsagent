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
		Source:      &apigen.NetworkPolicyPeerRef{Kind: apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE, ID: 2},
		Destination: &apigen.NetworkPolicyPeerRef{Kind: apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_DEPLOYMENT, ID: 7},
		Ports:       []*apigen.NetPortMatch{{Protocol: apigen.NetProtocol_NET_PROTOCOL_TCP, Port: 8080}},
	}
}

func TestNetworkPolicyMapInputsIncludeActivePolicies(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	createNetworkPolicyForTest(store, testPolicy(), 1)
	inputs := FetchNetworkMapInputs(store)
	if len(inputs.Policies) != 1 || inputs.Policies[0].NetworkPolicyID != 1 {
		t.Fatalf("map input policies = %+v, want the created policy", inputs.Policies)
	}
	if inputs.Seq <= 0 {
		t.Fatalf("map input seq = %d, want positive after policy write", inputs.Seq)
	}
}

func createNetworkPolicyForTest(s *state.Service, policy *apigen.NetworkPolicy, author int32) *apigen.NetworkPolicyEvent {
	ctx := context.Background()
	now := time.Now().UnixMilli()
	var event *apigen.NetworkPolicyEvent
	erru.Must(0, s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		id, err := q.NextNetworkPolicyID(ctx)
		if err != nil {
			return nil, err
		}
		event = &apigen.NetworkPolicyEvent{Seq: seq, EventTime: now, CreatedTime: now, Author: author, NetworkPolicyID: int32(id), Version: 1, Value: *policy, EventType: apigen.EventType_EVENT_TYPE_CREATE}
		if err := q.InsertNetworkPolicyEvent(ctx, event); err != nil {
			return nil, err
		}
		return &state.Update{NetworkPolicyEvents: []*apigen.NetworkPolicyEvent{event}}, nil
	}))
	return event
}
