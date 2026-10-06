package state

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func TestMergedCommitConditionallyStoresSequence(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	ctx := context.Background()
	node := testNode(s, "primary")
	before := erru.Must(s.q.GetGlobalSeq(ctx))
	sub, unsub := s.SubscribeUpdates()
	defer unsub()
	setNodeStatusForTest(s, node.Identifier, true, time.Now())
	observed := <-sub
	if len(observed.Mutations) != 1 || !observed.Has(apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS) || observed.Seq != before+1 {
		t.Fatalf("observed-only publication: %+v", observed)
	}
	assertUpdateMatchesRows(t, s, observed)
	if erru.Must(s.q.GetGlobalSeq(ctx)) != before+1 {
		t.Fatal("observed-only write did not consume sequence")
	}
	err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
		if seq != before+2 {
			t.Errorf("candidate = %d, want %d", seq, before+2)
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if erru.Must(s.q.GetGlobalSeq(ctx)) != before+1 {
		t.Fatal("empty update consumed sequence")
	}
	select {
	case <-sub:
		t.Fatal("empty update published")
	default:
	}
	createSpaceForTest(s, "next")
	if update := <-sub; update.Seq != before+2 {
		t.Fatalf("next core seq = %d", update.Seq)
	}
}

func TestMergedReadThenWriteConcurrentWithSessionCommits(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer s.Close()
	ctx := context.Background()
	node := testNode(s, "primary")
	before := erru.Must(s.q.GetGlobalSeq(ctx))
	const writes = 80
	start := make(chan struct{})
	failures := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < writes; i++ {
			err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
				runtime.Gosched()
				status, err := q.NodeConnectionStatus(ctx, node.Identifier, true, time.Now())
				if err != nil {
					return nil, err
				}
				return pq.NewUpdate(pq.NodeStatusMutation(seq, time.Now().UnixMilli(), status)), nil
			})
			if err != nil {
				failures <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < writes; i++ {
			id := fmt.Sprintf("session-%d", i)
			err := s.Commit(ctx, nil, func(q *pq.Queries, seq int64) (*WriteUpdate, error) {
				entityID, err := q.NextEntityID(ctx, apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION)
				if err != nil {
					return nil, err
				}
				meta := pq.EventMeta{GlobalSeq: seq, EventType: apigen.AuthzVerb_AUTHZ_VERB_CREATE}
				return pq.NewUpdate(pq.AgentSessionMutation(meta, entityID, &apigen.AgentSession{SessionID: id, UserID: 1, Status: apigen.AgentSessionStatus_AGENT_SESSION_STATUS_PENDING})), nil
			})
			if err != nil {
				failures <- err
				return
			}
		}
	}()
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if erru.Must(s.q.GetGlobalSeq(ctx)) != before+2*writes {
		t.Fatal("every session and observation commit must consume one seq")
	}
	sessions, err := s.q.ListAgentSessionsForUser(context.Background(), 1)
	if err != nil || len(sessions) != writes {
		t.Fatalf("sessions=%d err=%v", len(sessions), err)
	}
	for _, row := range sessions {
		if row.Seq <= before {
			t.Fatalf("session %s carries seq %d, before the run started", row.SessionID, row.Seq)
		}
	}
	if len(erru.Must(s.q.ListNodeStatusHistorySince(context.Background(), node.ID, time.Time{}))) != writes {
		t.Fatal("observation history lost writes")
	}
}
