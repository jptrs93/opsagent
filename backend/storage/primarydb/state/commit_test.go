package state

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func TestCommitRollbackPreservesIDsAndPublishesWriterUpdate(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	ctx := context.Background()
	before := erru.Must(s.q.GetGlobalSeq(ctx))
	sub, unsub := s.SubscribeUpdates()
	defer unsub()
	stop := errors.New("abort after writes")
	var firstID, firstEventID int64
	var returned *Update
	write := func(q *pq.Queries, seq int64) (*Update, error) {
		id, err := q.NextDeploymentID(ctx)
		if err != nil {
			return nil, err
		}
		event, err := q.WriteDeploymentCreate(apigen.Context{Ctx: ctx, User: &apigen.InternalUser{ID: 1}}, id, seq, time.Now(), &apigen.Deployment{Name: "written", SpaceID: defaultSpaceID})
		if err != nil {
			return nil, err
		}
		if firstID == 0 {
			firstID, firstEventID = id, event.EventID
		} else if id != firstID || event.EventID != firstEventID {
			t.Fatal("rollback consumed identity or event id")
		}
		returned = pq.NewUpdate(pq.DeploymentMutation(event))
		return returned, nil
	}
	err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*Update, error) {
		update, err := write(q, seq)
		if err != nil {
			return nil, err
		}
		return update, stop
	})
	if !errors.Is(err, stop) || erru.Must(s.q.GetGlobalSeq(ctx)) != before {
		t.Fatalf("rollback = %v", err)
	}
	select {
	case <-sub:
		t.Fatal("rollback published")
	default:
	}
	if err := s.Commit(ctx, nil, write); err != nil {
		t.Fatal(err)
	}
	got := <-sub
	if got.Seq != before+1 || len(got.Mutations) != 1 {
		t.Fatalf("incorrect writer sequence: %+v", got)
	}
	m := got.Mutations[0]
	if m.Type() != apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT || m.Kind() != apigen.AuthzVerb_AUTHZ_VERB_CREATE || m.EntityID() != firstID || m.Entity().Deployment.Version != 1 {
		t.Fatalf("writer mutation: %+v", m)
	}
	if !reflect.DeepEqual(got, *returned) {
		t.Fatal("store changed the writer update")
	}
	assertUpdateMatchesRows(t, s, got)
}

func TestGrantEventsMatchRowsAndBootstrapReplay(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	ctx := context.Background()
	replay := fullFold(t, s.q)
	sub, unsub := s.SubscribeUpdates()
	defer unsub()
	check := func(kind apigen.AuthzVerb, id int64) *apigen.AuthzGrantValue {
		t.Helper()
		update := <-sub
		assertUpdateMatchesRows(t, s, update)
		if len(update.Mutations) != 1 {
			t.Fatalf("grant writer published %d mutations", len(update.Mutations))
		}
		m := update.Mutations[0]
		if m.Type() != apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT || m.EntityID() != id || m.Kind() != kind {
			t.Fatalf("invalid grant mutation: %+v", m)
		}
		foldInto(replay, &update)
		assertFoldEqual(t, "grant replay", bootstrapFold(t, s.q), retainForBootstrap(replay))
		if entity := m.Entity(); entity != nil {
			return entity.AuthzGrant
		}
		return nil
	}
	first := insertGrantForTest(t, s, 7, 3, 123)
	created := check(apigen.AuthzVerb_AUTHZ_VERB_CREATE, first)
	if created == nil || created.UserID != 7 || created.TemplateID != 2 || created.Author != 3 || created.CreatedTime != 123 {
		t.Fatalf("grant payload lost its facts: %+v", created)
	}
	second := insertGrantForTest(t, s, 8, 0, 0)
	check(apigen.AuthzVerb_AUTHZ_VERB_CREATE, second)
	createdEvent := erru.Must(s.q.GetLatestAuthzGrantEvent(ctx, first))
	deleteGrantForTest(t, s, first)
	check(apigen.AuthzVerb_AUTHZ_VERB_DELETE, first)
	deletedEvent := erru.Must(s.q.GetLatestAuthzGrantEvent(ctx, first))
	if deletedEvent.EventType != apigen.EventType_EVENT_TYPE_DELETE || deletedEvent.Version != createdEvent.Version+1 || deletedEvent.CreatedTime != createdEvent.CreatedTime || !reflect.DeepEqual(deletedEvent.Value, createdEvent.Value) {
		t.Fatal("grant deletion lost its subject, bindings or original creation time")
	}
	grants := replay[apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT]
	if len(grants) != 1 || grants[second] == nil {
		t.Fatal("grant deletion removed an unrelated grant")
	}
	history := erru.Must(s.q.MutationsInRange(ctx, createdEvent.Seq-1, createdEvent.Seq))
	if len(history) != 1 || history[0].Type != apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT || history[0].ID != first {
		t.Fatalf("grant creation history was lost: %+v", history)
	}
}

func insertGrantForTest(t *testing.T, s *Service, userID, author, createdAt int64) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*Update, error) {
		var err error
		id, err = q.NextAuthzGrantID(ctx)
		if err != nil {
			return nil, err
		}
		event := apigen.AuthzGrantEvent{Seq: seq, EventTime: createdAt, CreatedTime: createdAt, Author: author, AuthzGrantID: id, Version: 1,
			Value: apigen.AuthzGrantValue{UserID: userID, TemplateID: 2, Grant: &apigen.AuthzGrant{}}, EventType: apigen.EventType_EVENT_TYPE_CREATE}
		if err := q.InsertAuthzGrantEvent(ctx, &event); err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.AuthzGrantMutation(&event)), nil
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func deleteGrantForTest(t *testing.T, s *Service, id int64) {
	t.Helper()
	ctx := context.Background()
	if err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*Update, error) {
		prev, err := q.GetLatestAuthzGrantEvent(ctx, id)
		if err != nil {
			return nil, err
		}
		event := apigen.AuthzGrantEvent{Seq: seq, EventTime: 456, CreatedTime: prev.CreatedTime, AuthzGrantID: id, Version: prev.Version + 1,
			Value: prev.Value, EventType: apigen.EventType_EVENT_TYPE_DELETE}
		if err := q.InsertAuthzGrantEvent(ctx, &event); err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.AuthzGrantMutation(&event)), nil
	}); err != nil {
		t.Fatal(err)
	}
}
