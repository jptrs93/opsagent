package webuihandler

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/pubsubu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/backup"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/agentsessions"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/users"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func globalSeq(t *testing.T, h *Handler) int64 {
	t.Helper()
	return erru.Must(h.Store.Queries().GetGlobalSeq(context.Background()))
}

func recvMsg(t *testing.T, msgs <-chan *apigen.EventStreamMsg) *apigen.EventStreamMsg {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case m, ok := <-msgs:
			if !ok {
				t.Fatal("stream closed unexpectedly")
			}
			if m.Heartbeat {
				continue
			}
			return m
		case <-deadline:
			t.Fatal("timed out waiting for a stream message")
		}
	}
}

// The first message on a stream is the sidecar statuses, not the opening snapshot.
func openTestEventStream(t *testing.T, h *Handler, userID uint64) (<-chan *apigen.EventStreamMsg, *apigen.EventStreamMsg) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	out := make(chan *apigen.EventStreamMsg, 4096)
	go func() {
		defer close(out)
		for msg, err := range h.PostV1GlobalEventStream(apigen.Context{Ctx: ctx, User: &apigen.User{ID: userID}}, &apigen.EventStreamRequest{}) {
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
	side := recvMsg(t, out)
	if !side.SecretsStatus.Present || !side.BackupStatus.Present || !side.IngressDiagnostics.Present || len(side.Events) != 0 || side.Snapshot.Present || side.Synced {
		t.Fatalf("first message is not the sidecar statuses: %+v", side)
	}
	opening := recvMsg(t, out)
	if !opening.Synced || !opening.Snapshot.Present || opening.Snapshot.Value.Seq != opening.Seq || len(opening.Events) != 0 {
		t.Fatalf("second message is not the opening snapshot: %+v", opening)
	}
	return out, opening
}

func startTestEventStream(t *testing.T, h *Handler, userID uint64) <-chan *apigen.EventStreamMsg {
	t.Helper()
	out, _ := openTestEventStream(t, h, userID)
	return out
}

// foldOpening is the state a browser holds after a message: the snapshot
// when the message carries one, otherwise its events folded.
func foldOpening(msg *apigen.EventStreamMsg) map[apigen.CoreEntityType]map[uint64]*apigen.CoreEntity {
	if msg.Snapshot.Present {
		return foldEntities(msg.Snapshot.Value.Entities)
	}
	return foldEvents(msg.Events)
}

func mutationsOf(msg *apigen.EventStreamMsg, typ apigen.CoreEntityType) []*apigen.CoreMutation {
	var out []*apigen.CoreMutation
	for i := range msg.Events {
		for j := range msg.Events[i].Mutations {
			if m := &msg.Events[i].Mutations[j]; m.Type() == typ {
				out = append(out, m)
			}
		}
	}
	return out
}

func foldMsg(msg *apigen.EventStreamMsg, typ apigen.CoreEntityType) map[uint64]*apigen.CoreEntity {
	return foldOpening(msg)[typ]
}

func TestBackupStatusIsIndependentOfCoreSequenceAndMutex(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	h.BackupStatus = &backup.StatusPublisher{}
	updates := startTestEventStream(t, h, 1)
	before := globalSeq(t, h)
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
	msg := recvMsg(t, updates)
	if len(msg.Events) != 0 || msg.Seq != 0 || !msg.BackupStatus.Present || !reflect.DeepEqual(msg.BackupStatus.Value, status) {
		t.Fatalf("backup status was sequenced or lost: %+v", msg)
	}
	if globalSeq(t, h) != before {
		t.Fatal("backup publication dirtied the database")
	}
}

func TestSidecarValuesAreFrozen(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	h.secretsUpdates.Notify(apigen.SecretsStatusResponse{Unlocked: true})
	before, err := h.PostV1GlobalEvents(enforceCtx(1, false), &apigen.EventStreamRequest{})
	if err != nil {
		t.Fatal(err)
	}
	h.secretsUpdates.Notify(apigen.SecretsStatusResponse{Unlocked: false})
	if !before.SecretsStatus.Value.Unlocked {
		t.Fatal("later publication changed an earlier message")
	}
}

func TestObservedStatusIsOneSequencedEvent(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	node := ensureTestNode(h.Store, "primary", "primary")
	updates := startTestEventStream(t, h, 1)
	before := globalSeq(t, h)
	nodes.SetNodeStatusByIdentifier(h.Store, node.Identifier, true, time.Now())
	msg := recvMsg(t, updates)
	statuses := mutationsOf(msg, apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS)
	if len(msg.Events) != 1 || len(msg.Events[0].Mutations) != 1 || len(statuses) != 1 || !statuses[0].Entity().Value.NodeStatus.IsConnected {
		t.Fatalf("observation was lost or mixed with other rows: %+v", msg)
	}
	if msg.Seq != before+1 || msg.Events[0].Seq != before+1 || globalSeq(t, h) != before+1 {
		t.Fatal("observation did not consume exactly one sequence")
	}
}

func TestCreatingCoreArrivesWithItsObservation(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	updates := startTestEventStream(t, h, 1)
	if _, _, err := nodes.UpsertEnrollmentRequest(h.Store, "192.0.2.2", "v1", apigen.NodeReported{Identifier: "worker", UnderlayAddress: mustAddr("192.0.2.2")}); err != nil {
		t.Fatal(err)
	}
	msg := recvMsg(t, updates)
	if len(msg.Events) != 1 || len(mutationsOf(msg, apigen.CoreEntityType_CORE_ENTITY_NODE)) != 1 || len(mutationsOf(msg, apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS)) != 1 {
		t.Fatalf("creating core and observation were not one event: %+v", msg)
	}
}

func TestSessionUpdatesReachOnlyTheirOwner(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	a, b := startTestEventStream(t, h, 1), startTestEventStream(t, h, 2)
	before := globalSeq(t, h)
	for _, userID := range []uint64{1, 2} {
		if err := h.agentSessions().InsertAgentSession(agentsessions.Record{ID: fmt.Sprint(userID), UserID: userID, CreatedAt: time.Now(), Status: apigen.AgentSessionStatus_AGENT_SESSION_STATUS_PENDING}, 0); err != nil {
			t.Fatal(err)
		}
		if err := users.InsertUserSession(h.Store, users.UserSession{ID: "u" + fmt.Sprint(userID), UserID: userID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), TokenHash: []byte{1}, Kind: fullSession}); err != nil {
			t.Fatal(err)
		}
	}
	for i, ch := range []<-chan *apigen.EventStreamMsg{a, b} {
		owner := uint64(i + 1)
		agent := recvMsg(t, ch)
		agents := mutationsOf(agent, apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION)
		if len(agent.Events) != 1 || len(agents) != 1 || agents[0].Entity().Value.AgentSession.UserID != owner || len(mutationsOf(agent, apigen.CoreEntityType_CORE_ENTITY_USER_SESSION)) != 0 {
			t.Fatalf("agent session update reached wrong owner: %+v", agent)
		}
		user := recvMsg(t, ch)
		sessions := mutationsOf(user, apigen.CoreEntityType_CORE_ENTITY_USER_SESSION)
		if len(user.Events) != 1 || len(sessions) != 1 || sessions[0].Entity().Value.UserSession.UserID != owner || len(mutationsOf(user, apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION)) != 0 {
			t.Fatalf("user session update reached wrong owner: %+v", user)
		}
		if agent.Seq <= before || user.Seq <= agent.Seq || agent.Events[0].Seq != agent.Seq || user.Events[0].Seq != user.Seq {
			t.Fatalf("session commits were not sequenced in order: %d %d after %d", agent.Seq, user.Seq, before)
		}
	}
	if globalSeq(t, h) != before+4 {
		t.Fatal("each session write must consume one seq")
	}
	opening, err := h.PostV1GlobalEvents(enforceCtx(1, false), &apigen.EventStreamRequest{})
	if err != nil {
		t.Fatal(err)
	}
	agents, sessions := foldMsg(opening, apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION), foldMsg(opening, apigen.CoreEntityType_CORE_ENTITY_USER_SESSION)
	if len(agents) != 1 || len(sessions) != 1 {
		t.Fatalf("bootstrap leaked another user's sessions: %+v %+v", agents, sessions)
	}
	for _, e := range agents {
		if e.Value.AgentSession.UserID != 1 {
			t.Fatalf("bootstrap leaked agent session %+v", e.Value.AgentSession)
		}
	}
	for _, e := range sessions {
		if e.Value.UserSession.UserID != 1 {
			t.Fatalf("bootstrap leaked user session %+v", e.Value.UserSession)
		}
	}
}

func TestGrantResetTargetsAffectedUserAndDebounces(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	a, b := startTestEventStream(t, h, 2), startTestEventStream(t, h, 3)
	for range 2 {
		if _, err := h.Authz.CreateGrant(clusterAdminGrant(2), 0); err != nil {
			t.Fatal(err)
		}
	}
	if msg := recvMsg(t, a); !msg.Snapshot.Present {
		t.Fatalf("user A did not get a fresh snapshot: %+v", msg)
	}
	// A visible marker proves B's stream has processed the grant transactions.
	created, err := values.CreateConfig(h.Store, "marker", 1, 0, 1, "ready")
	if err != nil {
		t.Fatal(err)
	}
	for {
		msg := recvMsg(t, b)
		if msg.Snapshot.Present {
			t.Fatal("grant for A reset B")
		}
		if configs := mutationsOf(msg, apigen.CoreEntityType_CORE_ENTITY_CONFIG); len(configs) > 0 && configs[0].EntityID() == created.ConfigID {
			break
		}
	}
	select {
	case msg := <-a:
		if msg.Snapshot.Present {
			t.Fatal("burst produced duplicate reset")
		}
	case <-time.After(300 * time.Millisecond):
	}
}

func TestSubscriberOverflowEndsStreamAndReconnectBootstraps(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	ready, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer close(done)
		for msg, err := range h.PostV1GlobalEventStream(apigen.Context{Ctx: ctx, User: &apigen.User{ID: 1}}, &apigen.EventStreamRequest{}) {
			if err != nil {
				return
			}
			if msg.Synced {
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
	// A fresh connection always repairs the gap with a snapshot.
	startTestEventStream(t, h, 1)
}

func TestHiddenTransactionIsNotSent(t *testing.T) {
	h, hidden := newEnforcementTestHandler(t)
	event, err := values.CreateConfig(h.Store, "hidden", hidden.ID, 0, 1, "private")
	if err != nil {
		t.Fatal(err)
	}
	update := pq.NewUpdate(pq.ConfigMutation(pq.EventMeta{GlobalSeq: event.Seq, EventTime: event.EventTime, Author: event.Author, EventType: apigen.AuthzVerb_AUTHZ_VERB_CREATE}, event.ConfigID, event.Value))
	if got := h.visibleUpdate(enforceCtx(3, false), update); got != nil {
		t.Fatalf("hidden transaction was sent: %+v", got)
	}
}

func TestPolicyScopeChangeResetsOnlyAffectedVisibility(t *testing.T) {
	h, hidden := newEnforcementTestHandler(t)
	policy := func(space uint64) *apigen.NetworkPolicy {
		return &apigen.NetworkPolicy{Action: apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW, Source: spacePeer(space), Destination: spacePeer(space)}
	}
	created := writeNetworkPolicyForTest(t, h.Store, 0, policy(1))
	limited, admin := startTestEventStream(t, h, 2), startTestEventStream(t, h, 1)
	changed := writeNetworkPolicyForTest(t, h.Store, created.NetworkPolicyID, policy(hidden.ID))
	if msg := recvMsg(t, limited); !msg.Snapshot.Present || len(foldMsg(msg, apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY)) != 0 {
		t.Fatalf("hidden policy was not removed by reset: %+v", msg)
	}
	if msg := recvMsg(t, admin); msg.Snapshot.Present || len(mutationsOf(msg, apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY)) != 1 {
		t.Fatalf("unchanged admin visibility reset: %+v", msg)
	}
	writeNetworkPolicyForTest(t, h.Store, changed.NetworkPolicyID, policy(1))
	if msg := recvMsg(t, limited); !msg.Snapshot.Present || len(foldMsg(msg, apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY)) != 1 {
		t.Fatalf("newly visible policy absent after reset: %+v", msg)
	}
}

func TestGrantDeletePublishesTombstoneAndResetsOnlyAffectedUser(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	hidden, err := values.CreateConfig(h.Store, "staging-only", staging.ID, 0, 1, "value")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := h.Authz.CreateGrant(clusterAdminGrant(2), 0)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := h.PostV1GlobalEvents(enforceCtx(2, false), &apigen.EventStreamRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if configs := foldMsg(initial, apigen.CoreEntityType_CORE_ENTITY_CONFIG); len(configs) != 1 || configs[hidden.ConfigID] == nil {
		t.Fatal("grant did not reveal the staging config")
	}
	admin, affected, other := startTestEventStream(t, h, 1), startTestEventStream(t, h, 2), startTestEventStream(t, h, 3)
	if err := h.Authz.DeleteGrant(2, grant.ID, 0); err != nil {
		t.Fatal(err)
	}
	msg := recvMsg(t, admin)
	grants := mutationsOf(msg, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT)
	if len(msg.Events) != 1 || len(grants) != 1 {
		t.Fatalf("admin did not receive a grant delta: %+v", msg)
	}
	if grants[0].Kind() != apigen.AuthzVerb_AUTHZ_VERB_DELETE || grants[0].EntityID() != grant.ID || msg.Events[0].Seq != msg.Seq {
		t.Fatalf("invalid grant tombstone: %+v", grants[0])
	}
	msg = recvMsg(t, affected)
	if !msg.Snapshot.Present || len(foldMsg(msg, apigen.CoreEntityType_CORE_ENTITY_CONFIG)) != 0 {
		t.Fatal("revocation did not reset and remove the formerly visible config")
	}
	for id, e := range foldMsg(msg, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT) {
		if id == grant.ID || e.Value.AuthzGrant.UserID != 2 {
			t.Fatal("reset retained the revoked grant or exposed another user's grant")
		}
	}
	marker, err := values.CreateConfig(h.Store, "marker", 1, 0, 1, "ready")
	if err != nil {
		t.Fatal(err)
	}
	for {
		msg := recvMsg(t, other)
		if msg.Snapshot.Present || len(mutationsOf(msg, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT)) != 0 {
			t.Fatal("grant revocation reset another user or exposed the grant")
		}
		if configs := mutationsOf(msg, apigen.CoreEntityType_CORE_ENTITY_CONFIG); len(configs) > 0 && configs[0].EntityID() == marker.ConfigID {
			break
		}
	}
}

func writeNetworkPolicyForTest(t *testing.T, s *state.Service, id uint64, policy *apigen.NetworkPolicy) *pq.NetworkPolicyEvent {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UnixMilli()
	var event *pq.NetworkPolicyEvent
	if err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*state.WriteUpdate, error) {
		value := *policy
		event = &pq.NetworkPolicyEvent{Seq: seq, EventTime: now, CreatedTime: now, Author: 1}
		verb := apigen.AuthzVerb_AUTHZ_VERB_CREATE
		if id == 0 {
			next, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY)
			if err != nil {
				return nil, err
			}
			event.NetworkPolicyID = next
		} else {
			prev, err := q.GetNetworkPolicy(ctx, id)
			if err != nil {
				return nil, err
			}
			event.NetworkPolicyID, event.CreatedTime, verb = id, prev.CreatedTime, apigen.AuthzVerb_AUTHZ_VERB_UPDATE
		}
		event.Value = value
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: now, Author: 1, EventType: verb}
		return pq.NewUpdate(pq.NetworkPolicyMutation(meta, event.NetworkPolicyID, value)), nil
	}); err != nil {
		t.Fatal(err)
	}
	return event
}
