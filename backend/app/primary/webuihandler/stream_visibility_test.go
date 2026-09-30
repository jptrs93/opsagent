package webuihandler

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/authz"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

type spaceEntities struct {
	deployment, instance, secret, config, asset int64
}

func (e spaceEntities) byType() map[apigen.CoreEntityType]int64 {
	return map[apigen.CoreEntityType]int64{
		apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT:         e.deployment,
		apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE: e.instance,
		apigen.CoreEntityType_CORE_ENTITY_SECRET:             e.secret,
		apigen.CoreEntityType_CORE_ENTITY_CONFIG:             e.config,
		apigen.CoreEntityType_CORE_ENTITY_ASSET:              e.asset,
	}
}

func seedSpaceEntities(t *testing.T, h *Handler, spaceID int32, tag string) spaceEntities {
	t.Helper()
	admin := enforceCtx(1, false)
	node := nodes.EnsurePrimaryNode(h.Store, "node-"+tag, "node-"+tag)
	dep := createTestDeployment(h.Store, "node-"+tag, spaceID, "dep-"+tag, ptr(remoteDeploymentSpec("nginx", hostNetworking())))
	inst := statetest.CreateScheduledInstance(h.Store, dep.DeploymentID, dep.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	secret, err := h.Secrets.Create("secret-"+tag, []byte("s"), 1, spaceID, 0)
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	config, err := values.CreateConfig(h.Store, "config-"+tag, spaceID, 0, 1, "c")
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	asset, err := createTestAsset(h, admin, "asset-"+tag, spaceID, 0, []byte("blob-"+tag))
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	return spaceEntities{int64(dep.DeploymentID), int64(inst.ID), int64(secret.SecretID), int64(config.ConfigID), int64(asset.AssetID)}
}

func assertFoldHasOnly(t *testing.T, fold map[apigen.CoreEntityType]map[int64]*apigen.CoreEntity, present, absent spaceEntities) {
	t.Helper()
	for typ, id := range present.byType() {
		if fold[typ][id] == nil {
			t.Fatalf("%v %d missing from the bootstrap: %v", typ, id, fold[typ])
		}
	}
	for typ, id := range absent.byType() {
		if fold[typ][id] != nil {
			t.Fatalf("%v %d from the hidden space leaked into the bootstrap", typ, id)
		}
	}
}

func TestSpaceScopedStreamSeesOnlyItsSpace(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	h.Assets = testAssetStore(t, h)
	visible := seedSpaceEntities(t, h, nodes.DefaultSpaceID, "default")
	hidden := seedSpaceEntities(t, h, staging.ID, "staging")

	opening, err := h.PostV1GlobalEvents(enforceCtx(2, false), &apigen.EventStreamRequest{})
	if err != nil {
		t.Fatal(err)
	}
	fold := statetest.Fold(opening.Events)
	assertFoldHasOnly(t, fold, visible, hidden)
	for typ, ids := range fold {
		if want, ok := visible.byType()[typ]; ok && (len(ids) != 1 || ids[want] == nil) {
			t.Fatalf("%v bootstrap = %v, want only %d", typ, ids, want)
		}
	}

	stream := startTestEventStream(t, h, 2)
	statetest.DeleteDeployment(h.Store, apigen.Context{}, int32(visible.deployment))
	msg := recvMsg(t, stream)
	var deleted bool
	for _, m := range mutationsOf(msg, apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT) {
		deleted = deleted || (m.Kind() == apigen.AuthzVerb_AUTHZ_VERB_DELETE && m.EntityID() == visible.deployment)
	}
	if !deleted || msg.Reset {
		t.Fatalf("delete of a sent deployment was not forwarded: %+v", msg)
	}

	statetest.DeleteDeployment(h.Store, apigen.Context{}, int32(hidden.deployment))
	marker, err := values.CreateConfig(h.Store, "marker", nodes.DefaultSpaceID, 0, 1, "ready")
	if err != nil {
		t.Fatal(err)
	}
	for {
		msg := recvMsg(t, stream)
		if msg.Reset {
			t.Fatalf("hidden delete reset the stream: %+v", msg)
		}
		for _, m := range mutationsOf(msg, apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT) {
			if m.EntityID() == hidden.deployment {
				t.Fatalf("delete of a never-sent deployment was forwarded: %+v", m)
			}
		}
		if configs := mutationsOf(msg, apigen.CoreEntityType_CORE_ENTITY_CONFIG); len(configs) == 1 && configs[0].EntityID() == int64(marker.ConfigID) {
			break
		}
	}

	if _, err := h.Authz.CreateGrant(&apigen.AuthzGrantRecord{UserID: 2, TemplateID: authz.ClusterAdminTemplateID, Grant: &apigen.AuthzGrant{}}); err != nil {
		t.Fatal(err)
	}
	reset := recvMsg(t, stream)
	if !reset.Reset || !reset.Synced {
		t.Fatalf("grant change did not re-bootstrap: %+v", reset)
	}
	fold = statetest.Fold(reset.Events)
	for typ, id := range hidden.byType() {
		if typ == apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT {
			continue
		}
		if fold[typ][id] == nil {
			t.Fatalf("%v %d still hidden after the grant: %v", typ, id, fold[typ])
		}
	}
	if fold[apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT][hidden.deployment] != nil || fold[apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT][visible.deployment] != nil {
		t.Fatal("deleted deployments reappeared in the re-bootstrap")
	}
}

func TestStreamsOpenedDuringCommitsSeeEverySeqOnce(t *testing.T) {
	h, _ := newEnforcementTestHandler(t)
	const writers, perWriter = 4, 40
	startSeq := globalSeq(t, h)
	var wg sync.WaitGroup
	var mu sync.Mutex
	created := map[int64]int64{}
	start := make(chan struct{})
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := range perWriter {
				event, err := values.CreateConfig(h.Store, fmt.Sprintf("c-%d-%d", w, i), nodes.DefaultSpaceID, 0, 1, "v")
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				created[int64(event.ConfigID)] = event.Seq
				mu.Unlock()
			}
		}()
	}
	close(start)
	type stream struct {
		ch      <-chan *apigen.EventStreamMsg
		opening *apigen.EventStreamMsg
		after   int64
	}
	var streams []stream
	for k := range 6 {
		for globalSeq(t, h) < startSeq+int64(k*15) {
			time.Sleep(time.Millisecond)
		}
		var after int64
		if k%2 == 1 {
			after = globalSeq(t, h) - 3
		}
		ch, opening := openTestEventStream(t, h, 1, after)
		if opening.Reset != (after == 0) {
			t.Fatalf("stream %d after %d reset = %v", k, after, opening.Reset)
		}
		streams = append(streams, stream{ch, opening, after})
	}
	wg.Wait()
	marker, err := values.CreateConfig(h.Store, "marker", nodes.DefaultSpaceID, 0, 1, "ready")
	if err != nil {
		t.Fatal(err)
	}
	for k, s := range streams {
		seen := map[int64]int64{}
		record := func(events []*apigen.CoreWriteUpdate) {
			for _, e := range events {
				for _, m := range e.Mutations {
					if m.Type() != apigen.CoreEntityType_CORE_ENTITY_CONFIG {
						continue
					}
					if prev, dup := seen[m.EntityID()]; dup {
						t.Fatalf("stream %d: config %d delivered twice (seq %d and %d)", k, m.EntityID(), prev, e.Seq)
					}
					seen[m.EntityID()] = e.Seq
				}
			}
		}
		record(s.opening.Events)
		for _, e := range s.opening.Events {
			if e.Seq > s.opening.Seq || (s.after > 0 && e.Seq <= s.after) {
				t.Fatalf("stream %d: opening event seq %d outside (%d, %d]", k, e.Seq, s.after, s.opening.Seq)
			}
		}
		last := s.opening.Seq
		for seen[int64(marker.ConfigID)] == 0 {
			msg := recvMsg(t, s.ch)
			if msg.Reset {
				t.Fatalf("stream %d reset without a visibility change", k)
			}
			if msg.Seq <= last || len(msg.Events) != 1 || msg.Events[0].Seq != msg.Seq {
				t.Fatalf("stream %d: live message seq %d after %d with %d events", k, msg.Seq, last, len(msg.Events))
			}
			last = msg.Seq
			record(msg.Events)
		}
		delete(seen, int64(marker.ConfigID))
		for id, seq := range created {
			got, ok := seen[id]
			if seq <= s.after {
				if ok {
					t.Fatalf("stream %d: replay from %d included config %d at seq %d", k, s.after, id, seq)
				}
				continue
			}
			if !ok || got != seq {
				t.Fatalf("stream %d: config %d created at seq %d seen at %d (%v)", k, id, seq, got, ok)
			}
		}
		for id := range seen {
			if _, ok := created[id]; !ok {
				t.Fatalf("stream %d: unknown config %d delivered", k, id)
			}
		}
	}
}

func TestOpeningDecidesAnEntityByItsNewestRow(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	secret, err := h.Secrets.Create("moved", []byte("s"), 1, staging.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	id := int64(secret.SecretID)
	noop := func(*pq.Queries) error { return nil }
	rows := func(msg *apigen.EventStreamMsg) int {
		n := 0
		for _, m := range mutationsOf(msg, apigen.CoreEntityType_CORE_ENTITY_SECRET) {
			if m.EntityID() == id {
				n++
			}
		}
		return n
	}
	if err := h.Secrets.MoveSpace(secret.SecretID, nodes.DefaultSpaceID, 0, 1, noop); err != nil {
		t.Fatal(err)
	}
	opening, err := h.PostV1GlobalEvents(enforceCtx(2, false), &apigen.EventStreamRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := rows(opening); got != 2 {
		t.Fatalf("secret moved into the visible space shipped %d rows, want its whole history", got)
	}

	stream := startTestEventStream(t, h, 2)
	if err := h.Secrets.MoveSpace(secret.SecretID, staging.ID, 0, 1, noop); err != nil {
		t.Fatal(err)
	}
	reset := recvMsg(t, stream)
	if !reset.Reset {
		t.Fatalf("space move did not re-bootstrap: %+v", reset)
	}
	if got := rows(reset); got != 0 {
		t.Fatalf("secret moved out of the visible space still shipped %d rows", got)
	}
	opening, err = h.PostV1GlobalEvents(enforceCtx(2, false), &apigen.EventStreamRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := rows(opening); got != 0 {
		t.Fatalf("one-shot opening shipped %d rows of a hidden secret", got)
	}
}
