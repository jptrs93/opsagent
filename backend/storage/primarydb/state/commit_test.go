package state

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func TestCommitRollbackPreservesIDsAndPublishesWriterUpdate(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	ctx := context.Background()
	before := s.BuildSnapshot(ctx).Seq
	sub, unsub := s.SubscribeUpdates()
	defer unsub()
	stop := errors.New("abort after writes")
	var firstID, firstEventID int64
	var returned apigen.CoreUpdate
	write := func(q *pq.Queries, seq int64) (*Update, error) {
		id, err := q.NextDeploymentID(ctx)
		if err != nil {
			return nil, err
		}
		event, err := q.WriteDeploymentCreate(apigen.Context{Ctx: ctx, User: &apigen.InternalUser{ID: 1}}, id, seq, &apigen.Deployment{Name: "written", SpaceID: defaultSpaceID})
		if err != nil {
			return nil, err
		}
		if firstID == 0 {
			firstID, firstEventID = id, event.EventID
		} else if id != firstID || event.EventID != firstEventID {
			t.Fatal("rollback consumed identity or event id")
		}
		returned = apigen.CoreUpdate{Seq: seq, DeploymentEvents: []*apigen.DeploymentEvent{event}}
		return &returned, nil
	}
	err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*Update, error) {
		update, err := write(q, seq)
		if err != nil {
			return nil, err
		}
		return update, stop
	})
	if !errors.Is(err, stop) || s.BuildSnapshot(ctx).Seq != before {
		t.Fatalf("rollback = %v", err)
	}
	select {
	case <-sub:
		t.Fatal("rollback published")
	default:
	}
	err = s.Commit(ctx, nil, write)
	if err != nil {
		t.Fatal(err)
	}
	got := <-sub
	core := got
	if got.Seq != before+1 || core.DeploymentEvents[0].Seq != got.Seq {
		t.Fatalf("incorrect writer sequence: %+v", got)
	}
	if !reflect.DeepEqual(core, returned) {
		t.Fatal("store changed the writer update")
	}
	assertUpdateMatchesRows(t, s, got)
}

func TestGrantEventsMatchRowsAndSnapshotReplay(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	ctx := context.Background()
	replay := s.BuildSnapshot(ctx)
	sub, unsub := s.SubscribeUpdates()
	defer unsub()
	check := func(eventType apigen.EventType, id int64) *apigen.AuthzGrantEvent {
		t.Helper()
		update := <-sub
		assertUpdateMatchesRows(t, s, update)
		if len(update.AuthzGrantEvents) != 1 {
			t.Fatalf("grant writer published %d events", len(update.AuthzGrantEvents))
		}
		event := update.AuthzGrantEvents[0]
		if event.AuthzGrantID != id || event.EventType != eventType || event.EventID == 0 || event.Seq != update.Seq {
			t.Fatalf("invalid grant envelope: %+v", event)
		}
		wire, err := apigen.DecodeCoreUpdate(update.Encode())
		if err != nil {
			t.Fatal(err)
		}
		foldSnapshot(replay, *wire)
		if canonicalSnapshot(replay) != canonicalSnapshot(s.BuildSnapshot(ctx)) {
			t.Fatal("grant replay differs from the later snapshot")
		}
		return event
	}
	first := insertGrantForTest(t, s, 7, 3, 123)
	created := check(apigen.EventType_EVENT_TYPE_CREATE, first)
	second := insertGrantForTest(t, s, 8, 0, 0)
	check(apigen.EventType_EVENT_TYPE_CREATE, second)
	deleteGrantForTest(t, s, first)
	deleted := check(apigen.EventType_EVENT_TYPE_DELETE, first)
	if deleted.Version != created.Version+1 || deleted.CreatedTime != created.CreatedTime || !reflect.DeepEqual(deleted.Value, created.Value) {
		t.Fatal("grant deletion lost its subject, bindings or original creation time")
	}
	if len(replay.AuthzGrantEvents) != 1 || replay.AuthzGrantEvents[0].AuthzGrantID != second {
		t.Fatal("grant deletion removed an unrelated grant")
	}
	if rows, err := s.q.ListAuthzGrantEventsAtSeq(ctx, created.Seq); err != nil || len(rows) != 1 {
		t.Fatalf("grant creation history was lost: %v", err)
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
		return &Update{AuthzGrantEvents: []*apigen.AuthzGrantEvent{&event}}, nil
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
		return &Update{AuthzGrantEvents: []*apigen.AuthzGrantEvent{&event}}, nil
	}); err != nil {
		t.Fatal(err)
	}
}
