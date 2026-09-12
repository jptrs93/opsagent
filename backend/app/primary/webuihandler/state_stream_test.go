package webuihandler

import (
	"context"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/backup"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/agentsessions"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"testing"
	"time"

	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/authz"
)

func startTestStateStream(t *testing.T, h *Handler, userID int32) <-chan *apigen.StateStreamMsg {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	out := make(chan *apigen.StateStreamMsg, 64)
	go func() {
		defer close(out)
		for msg, err := range h.PostV1GlobalStateStream(apigen.Context{Ctx: ctx, User: &apigen.InternalUser{ID: userID}}) {
			if err != nil {
				return
			}
			select {
			case out <- msg:
			case <-ctx.Done():
				return
			}
		}
	}()
	if recvState(t, out).Snapshot == nil {
		t.Fatal("first message is not a snapshot")
	}
	return out
}

func TestBackupStatusIsIndependentOfCoreSequenceAndMutex(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	h.BackupStatus = &backup.StatusPublisher{}
	updates := startTestStateStream(t, h, 1)
	before := h.Store.BuildSnapshot(context.Background()).Seq
	status := apigen.BackupStatus{AssetPending: 2}
	published := make(chan struct{})
	h.Store.Mu.Lock()
	go func() { h.BackupStatus.Publish(status); close(published) }()
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Error("backup publication waited for the core mutex")
	}
	h.Store.Mu.Unlock()
	msg := recvState(t, updates)
	if msg.Core != nil || msg.BackupStatus == nil || *msg.BackupStatus != status {
		t.Fatalf("backup status was sequenced or lost: %+v", msg)
	}
	if h.Store.BuildSnapshot(context.Background()).Seq != before {
		t.Fatal("backup publication dirtied the database")
	}
}

func TestSidecarSnapshotValuesAreFrozen(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	h.secretsUpdates.Notify(apigen.SecretsStatusResponse{Unlocked: true})
	before := h.snapshotWithSidecars(enforceCtx(1, false))
	h.secretsUpdates.Notify(apigen.SecretsStatusResponse{Unlocked: false})
	if !before.SecretsStatus.Unlocked {
		t.Fatal("later publication changed an earlier snapshot")
	}
}

func TestObservedStatusStreamsWithoutACoreTransaction(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	node := nodes.EnsurePrimaryNode(h.Store, "primary", "primary")
	updates := startTestStateStream(t, h, 1)
	before := h.Store.BuildSnapshot(context.Background()).Seq
	nodes.SetNodeStatusByIdentifier(h.Store, node.Identifier, true, time.Now())
	msg := recvState(t, updates)
	if msg.Core == nil || msg.Core.HasCore() || len(msg.Core.NodeStatuses) != 1 || !msg.Core.NodeStatuses[0].IsConnected {
		t.Fatalf("observation was lost or sequenced as core: %+v", msg)
	}
	if msg.Core.Seq != before+1 || h.Store.BuildSnapshot(context.Background()).Seq != before+1 {
		t.Fatal("observation did not consume exactly one sequence")
	}
}

func TestCreatingCoreArrivesWithItsObservation(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	updates := startTestStateStream(t, h, 1)
	if _, _, err := nodes.UpsertEnrollmentRequest(h.Store, "192.0.2.2", "v1", apigen.NodeReported{Identifier: "worker", UnderlayAddress: "192.0.2.2"}); err != nil {
		t.Fatal(err)
	}
	msg := recvState(t, updates)
	if msg.Core == nil || len(msg.Core.NodeEvents) != 1 || len(msg.Core.NodeStatuses) != 1 {
		t.Fatalf("creating core and observation were not one message: %+v", msg)
	}
}

func TestAgentSessionSidecarUpdatesReachOnlyTheirOwner(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	a, b := startTestStateStream(t, h, 1), startTestStateStream(t, h, 2)
	before := h.Store.BuildSnapshot(context.Background()).Seq
	for _, userID := range []int32{1, 2} {
		if err := h.agentSessions().InsertAgentSession(agentsessions.Record{ID: fmt.Sprint(userID), UserID: userID, CreatedAt: time.Now(), Status: apigen.AgentSessionStatus_AGENT_SESSION_PENDING}); err != nil {
			t.Fatal(err)
		}
	}
	for i, ch := range []<-chan *apigen.StateStreamMsg{a, b} {
		msg := recvState(t, ch)
		if msg.AgentSessions == nil || len(msg.AgentSessions.Items) != 1 || msg.AgentSessions.Items[0].UserID != int32(i+1) {
			t.Fatalf("session update reached wrong owner: %+v", msg)
		}
	}
	if h.Store.BuildSnapshot(context.Background()).Seq != before {
		t.Fatal("agent session changed the core sequence")
	}
}

func TestGrantResetTargetsAffectedUserAndDebounces(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	a, b := startTestStateStream(t, h, 2), startTestStateStream(t, h, 3)
	for range 2 {
		if _, err := h.Authz.CreateGrant(&apigen.AuthzGrantRecord{UserID: 2, TemplateID: authz.ClusterAdminTemplateID, Grant: &apigen.AuthzGrant{}}); err != nil {
			t.Fatal(err)
		}
	}
	if msg := recvState(t, a); msg.Snapshot == nil {
		t.Fatalf("user A did not reset: %+v", msg)
	}
	// A visible marker proves B's stream has processed the grant transactions.
	created, err := values.CreateConfig(h.Store, "marker", 1, 0, 1, "ready")
	if err != nil {
		t.Fatal(err)
	}
	for {
		msg := recvState(t, b)
		if msg.Snapshot != nil {
			t.Fatal("grant for A reset B")
		}
		if msg.Core != nil && len(msg.Core.ConfigEvents) > 0 && msg.Core.ConfigEvents[0].ConfigID == created.ConfigID {
			break
		}
	}
	select {
	case msg := <-a:
		if msg.Snapshot != nil {
			t.Fatal("burst produced duplicate reset")
		}
	case <-time.After(300 * time.Millisecond):
	}
}

func TestSubscriberOverflowEndsStreamAndReconnectSnapshots(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	ready, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer close(done)
		for msg, err := range h.PostV1GlobalStateStream(apigen.Context{Ctx: ctx, User: &apigen.InternalUser{ID: 1}}) {
			if err != nil {
				return
			}
			if msg.Snapshot != nil {
				close(ready)
				<-release
			}
		}
	}()
	<-ready
	for i := 0; i <= pubsubu.DefaultChanBuffer; i++ {
		h.secretsUpdates.Notify(apigen.SecretsStatusResponse{Unlocked: i%2 == 0})
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("closed subscriber did not end stream")
	}
	// A fresh connection always repairs the gap with a full snapshot.
	startTestStateStream(t, h, 1)
}

func TestHiddenTransactionIsNotSent(t *testing.T) {
	h, hidden := newEnforcementTestHandler(t)
	event, err := values.CreateConfig(h.Store, "hidden", hidden.ID, 0, 1, "private")
	if err != nil {
		t.Fatal(err)
	}
	if got := h.visibleUpdate(enforceCtx(3, false), &apigen.CoreUpdate{Seq: event.Seq, ConfigEvents: []*apigen.ConfigEvent{event}}); got != nil {
		t.Fatalf("hidden transaction was sent: %+v", got)
	}
}

func TestPolicyScopeChangeResetsOnlyAffectedVisibility(t *testing.T) {
	h, hidden := newEnforcementTestHandler(t)
	policy := func(space int32) *apigen.NetworkPolicy {
		return &apigen.NetworkPolicy{Destination: &apigen.NetworkPolicyPeerRef{Kind: apigen.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE, ID: space}}
	}
	created := writeNetworkPolicyForTest(t, h.Store, 0, policy(1))
	limited, admin := startTestStateStream(t, h, 2), startTestStateStream(t, h, 1)
	changed := writeNetworkPolicyForTest(t, h.Store, created.NetworkPolicyID, policy(hidden.ID))
	if msg := recvState(t, limited); msg.Snapshot == nil || len(msg.Snapshot.NetworkPolicyEvents) != 0 {
		t.Fatalf("hidden policy was not removed by reset: %+v", msg)
	}
	if msg := recvState(t, admin); msg.Core == nil || len(msg.Core.NetworkPolicyEvents) != 1 {
		t.Fatalf("unchanged admin visibility reset: %+v", msg)
	}
	writeNetworkPolicyForTest(t, h.Store, changed.NetworkPolicyID, policy(1))
	if msg := recvState(t, limited); msg.Snapshot == nil || len(msg.Snapshot.NetworkPolicyEvents) != 1 {
		t.Fatalf("newly visible policy absent after reset: %+v", msg)
	}
}

func TestGrantDeletePublishesTombstoneAndResetsOnlyAffectedUser(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	hidden, err := values.CreateConfig(h.Store, "staging-only", staging.ID, 0, 1, "value")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := h.Authz.CreateGrant(&apigen.AuthzGrantRecord{UserID: 2, TemplateID: authz.ClusterAdminTemplateID, Grant: &apigen.AuthzGrant{}})
	if err != nil {
		t.Fatal(err)
	}
	initial := h.visibleSnapshot(enforceCtx(2, false), h.Store.BuildSnapshot(context.Background()))
	if len(initial.ConfigEvents) != 1 || initial.ConfigEvents[0].ConfigID != hidden.ConfigID {
		t.Fatal("grant did not reveal the staging config")
	}
	admin, affected, other := startTestStateStream(t, h, 1), startTestStateStream(t, h, 2), startTestStateStream(t, h, 3)
	if err := h.Authz.DeleteGrant(2, grant.ID); err != nil {
		t.Fatal(err)
	}
	msg := recvState(t, admin)
	if msg.Core == nil || len(msg.Core.AuthzGrantEvents) != 1 {
		t.Fatalf("admin did not receive a grant delta: %+v", msg)
	}
	event := msg.Core.AuthzGrantEvents[0]
	if event.EventType != apigen.EventType_EVENT_TYPE_DELETE || event.AuthzGrantID != grant.ID || event.Value.UserID != 2 || event.Seq != msg.Core.Seq {
		t.Fatalf("invalid grant tombstone: %+v", event)
	}
	msg = recvState(t, affected)
	if msg.Snapshot == nil || len(msg.Snapshot.ConfigEvents) != 0 {
		t.Fatal("revocation did not reset and remove the formerly visible config")
	}
	for _, event := range msg.Snapshot.AuthzGrantEvents {
		if event.AuthzGrantID == grant.ID || event.Value.UserID != 2 {
			t.Fatal("reset retained the revoked grant or exposed another user's grant")
		}
	}
	marker, err := values.CreateConfig(h.Store, "marker", 1, 0, 1, "ready")
	if err != nil {
		t.Fatal(err)
	}
	for {
		msg := recvState(t, other)
		if msg.Snapshot != nil || msg.Core != nil && len(msg.Core.AuthzGrantEvents) != 0 {
			t.Fatal("grant revocation reset another user or exposed the grant")
		}
		if msg.Core != nil && len(msg.Core.ConfigEvents) > 0 && msg.Core.ConfigEvents[0].ConfigID == marker.ConfigID {
			break
		}
	}
}

func writeNetworkPolicyForTest(t *testing.T, s *state.Service, id int32, policy *apigen.NetworkPolicy) *apigen.NetworkPolicyEvent {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UnixMilli()
	var event *apigen.NetworkPolicyEvent
	if err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.Update, error) {
		event = &apigen.NetworkPolicyEvent{Seq: seq, EventTime: now, CreatedTime: now, Author: 1, Version: 1, Value: *policy, EventType: apigen.EventType_EVENT_TYPE_CREATE}
		if id == 0 {
			next, err := q.NextNetworkPolicyID(ctx)
			if err != nil {
				return nil, err
			}
			event.NetworkPolicyID = int32(next)
		} else {
			prev, err := q.GetLatestNetworkPolicyEvent(ctx, int64(id))
			if err != nil {
				return nil, err
			}
			event.NetworkPolicyID, event.CreatedTime, event.Version, event.EventType = id, prev.CreatedTime, prev.Version+1, apigen.EventType_EVENT_TYPE_UPDATE
		}
		if err := q.InsertNetworkPolicyEvent(ctx, event); err != nil {
			return nil, err
		}
		return &state.Update{NetworkPolicyEvents: []*apigen.NetworkPolicyEvent{event}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	return event
}
