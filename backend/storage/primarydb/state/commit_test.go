package state

import (
	"context"
	"database/sql"
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
	var firstID int64
	var returned *WriteUpdate
	write := func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		id, err := q.NextDeploymentID(ctx)
		if err != nil {
			return nil, err
		}
		event := pq.DeploymentCreateEvent(apigen.Context{Ctx: ctx, User: &apigen.InternalUser{ID: 1}}, id, seq, time.Now(), &apigen.Deployment{Name: "written", SpaceID: defaultSpaceID})
		if firstID == 0 {
			firstID = id
		} else if id != firstID {
			t.Fatal("rollback consumed identity")
		}
		returned = pq.NewUpdate(pq.DeploymentMutation(event))
		return returned, nil
	}
	err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
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
	if m.Type() != apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT || m.Kind() != apigen.AuthzVerb_AUTHZ_VERB_CREATE || m.EntityID() != firstID || m.Meta() == nil || m.Meta().Version != 1 || m.Meta().SpecVersion != 1 {
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
	check := func(kind apigen.AuthzVerb, id int64) *apigen.AuthzGrant {
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
		assertFoldEqual(t, "grant replay", snapshotFold(t, s.q), retainForSnapshot(replay))
		assertSnapshotMatchesRebuild(t, s.q)
		if entity := m.Entity(); entity != nil {
			if meta := m.Meta(); meta.CreatedTime != update.Time || meta.UpdatedActor != update.Actor {
				t.Fatalf("grant meta %+v differs from the commit envelope %+v", meta, update)
			}
			return entity.AuthzGrant
		}
		return nil
	}
	first := insertGrantForTest(t, s, 7, 3, 123)
	created := check(apigen.AuthzVerb_AUTHZ_VERB_CREATE, first)
	if created == nil || created.UserID != 7 || created.TemplateID != 2 {
		t.Fatalf("grant payload lost its facts: %+v", created)
	}
	second := insertGrantForTest(t, s, 8, 0, 124)
	check(apigen.AuthzVerb_AUTHZ_VERB_CREATE, second)
	createdRow := erru.Must(s.q.GetAuthzGrant(ctx, first))
	if value := erru.Must(pq.AuthzGrantEntity(createdRow)); !reflect.DeepEqual(&value, created) {
		t.Fatalf("grant row %+v differs from the published payload %+v", value, created)
	}
	deleteGrantForTest(t, s, first)
	check(apigen.AuthzVerb_AUTHZ_VERB_DELETE, first)
	if _, err := s.q.GetAuthzGrant(ctx, first); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted grant still has a row: %v", err)
	}
	if latest := erru.Must(s.q.LatestMutation(ctx, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT, first)); latest.Kind() != apigen.AuthzVerb_AUTHZ_VERB_DELETE {
		t.Fatalf("newest logged grant mutation = %v, want a delete", latest.Kind())
	}
	grants := replay[apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT]
	if len(grants) != 1 || grants[second] == nil {
		t.Fatal("grant deletion removed an unrelated grant")
	}
	history := erru.Must(s.q.WriteEventsInRange(ctx, createdRow.Seq-1, createdRow.Seq))
	if len(history) != 1 || len(history[0].Mutations) != 1 || history[0].Mutations[0].Type() != apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT || history[0].Mutations[0].EntityID() != first {
		t.Fatalf("grant creation history was lost: %+v", history)
	}
}

func insertGrantForTest(t *testing.T, s *Service, userID, author, createdAt int64) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		var err error
		id, err = q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT)
		if err != nil {
			return nil, err
		}
		meta := pq.EventMeta{GlobalSeq: seq, EventTime: createdAt, Author: author, EventType: apigen.AuthzVerb_AUTHZ_VERB_CREATE}
		value := apigen.AuthzGrant{UserID: userID, TemplateID: 2, Spec: &apigen.AuthzGrantSpec{}}
		return pq.NewUpdate(pq.AuthzGrantMutation(meta, id, value)), nil
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func deleteGrantForTest(t *testing.T, s *Service, id int64) {
	t.Helper()
	ctx := context.Background()
	if err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		if _, err := q.GetAuthzGrant(ctx, id); err != nil {
			return nil, err
		}
		return pq.NewUpdate(pq.DeleteMutation(pq.EventMeta{GlobalSeq: seq, EventTime: 456}, apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT, id)), nil
	}); err != nil {
		t.Fatal(err)
	}
}
