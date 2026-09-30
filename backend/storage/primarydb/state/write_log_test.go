package state

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/goutil/erru"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/sqlitedb"
)

func assertWriteLogMatchesTables(t *testing.T, s *Service) {
	t.Helper()
	ctx := context.Background()
	seq := erru.Must(s.q.GetGlobalSeq(ctx))
	want := pq.Events(erru.Must(s.q.MutationsInRange(ctx, -1, seq)))
	got := erru.Must(s.q.WriteEventsInRange(ctx, -1, seq))
	if len(got) != len(want) {
		t.Fatalf("write log holds %d events, the entity tables replay %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Seq != want[i].Seq || got[i].Time != want[i].Time || got[i].Actor != want[i].Actor {
			t.Fatalf("envelope %d: log (%d, %d, %d), tables (%d, %d, %d)", i, got[i].Seq, got[i].Time, got[i].Actor, want[i].Seq, want[i].Time, want[i].Actor)
		}
		if !bytes.Equal(canonicalUpdate(*got[i]), canonicalUpdate(*want[i])) {
			t.Fatalf("seq %d: write log differs from the entity tables\nlog: %+v\ntables: %+v", got[i].Seq, got[i], want[i])
		}
	}
}

func truncateWriteLogAfter(t *testing.T, dbPath string, seq int64) {
	t.Helper()
	db := sqlitedb.MustOpen(dbPath)
	for _, stmt := range []string{`DELETE FROM write_event_mutations WHERE seq > ?`, `DELETE FROM write_events WHERE seq > ?`} {
		if _, err := db.Exec(stmt, seq); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWriteLogMirrorsEveryCommitAndBackfillsAtOpen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	s := Open(dbPath)
	ctx := apigen.Context{}
	node := testNode(s, "node-a")
	space := createSpaceForTest(s, "team")
	dep := mustCreateDeploymentForNode(s, ctx, space.ID, "web", node.ID, testSpecWithVersion("v1"))
	createScheduledInstanceForTest(s, dep.DeploymentID, dep.Version, node.ID, 0, apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING)
	setNodeStatusForTest(s, "node-a", true, time.Now())
	asset := setAssetByKeyForTest(s, "bundle", []byte("x"))
	deleteAssetForTest(s, asset.AssetID)
	updateDeploymentSpec(s, ctx, dep.DeploymentID, testSpecWithVersion("v2"))
	deleteDeployment(s, ctx, dep.DeploymentID)
	assertWriteLogMatchesTables(t, s)

	seq := erru.Must(s.q.GetGlobalSeq(context.Background()))
	erru.Must(0, s.Commit(context.Background(), nil, func(*pq.Queries, int64) (*WriteUpdate, error) { return nil, nil }))
	if logged := erru.Must(s.q.LatestWriteEventSeq(context.Background())); logged != seq {
		t.Fatalf("an empty commit moved the write log to seq %d, want %d", logged, seq)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	truncateWriteLogAfter(t, dbPath, seq-2)
	s = Open(dbPath)
	assertWriteLogMatchesTables(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	truncateWriteLogAfter(t, dbPath, -1)
	s = Open(dbPath)
	defer s.Close()
	assertWriteLogMatchesTables(t, s)
	if logged := erru.Must(s.q.LatestWriteEventSeq(context.Background())); logged != seq {
		t.Fatalf("rebuilt write log ends at seq %d, want %d", logged, seq)
	}
}
