package pq

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestDeploymentBuildersPreserveAttributionAndVersionFacts(t *testing.T) {
	q := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer q.Close()
	ctx := apigen.Context{Ctx: t.Context(), User: &apigen.InternalUser{ID: 7, Delegated: true}}
	def := &apigen.Deployment{Name: "app", SpaceID: 1}
	now := time.UnixMilli(time.Now().UnixMilli())
	commit := func(seq int64, event *apigen.DeploymentEvent) {
		t.Helper()
		commitForTest(t, q, seq, event.EventTime.UnixMilli(), event.Author, DeploymentMutation(event).Wire())
		if event.Deleted() {
			return
		}
		row, err := q.GetDeploymentEventByVersion(ctx, GetDeploymentEventByVersionParams{DeploymentID: int64(event.DeploymentID), Version: int64(event.Version)})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(event, row) {
			t.Fatalf("builder returned a different projection from reads\ngot: %+v\nrow: %+v", event, row)
		}
	}
	created := DeploymentCreateEvent(ctx, 42, 100, now, def)
	if created.DeploymentID != 42 || created.Seq != 100 || created.Author != -7 || created.Version != 1 || created.EventType != apigen.EventType_EVENT_TYPE_CREATE || !created.CreatedTime.Equal(now) || !created.EventTime.Equal(now) {
		t.Fatalf("create metadata = %+v", created)
	}
	commit(100, created)
	if event, err := q.DeploymentUpdateEvent(ctx, 42, 101, now.Add(time.Second), def); !errors.Is(err, ErrDeploymentUnchanged) || event != nil {
		t.Fatalf("unchanged update: %+v %v", event, err)
	}
	def.Name = "renamed"
	updated, err := q.DeploymentUpdateEvent(ctx, 42, 101, now.Add(time.Second), def)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Author != -7 || updated.Seq != 101 || updated.Version != 2 || updated.SpecVersion != created.SpecVersion || updated.EventType != apigen.EventType_EVENT_TYPE_UPDATE || !updated.CreatedTime.Equal(created.CreatedTime) || !updated.EventTime.Equal(now.Add(time.Second)) {
		t.Fatalf("update metadata = %+v", updated)
	}
	commit(101, updated)
	if rows, err := q.ListRetainedDeploymentVersions(ctx); err != nil || len(rows) != 1 || rows[0].Version != 2 {
		t.Fatalf("unpinned superseded version retained: %+v, %v", rows, err)
	}
	var stored []byte
	if err := q.db.QueryRowContext(ctx, `SELECT value FROM deployment_versions WHERE deployment_id = 42 AND version = 2`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if blob, err := apigen.DecodeDeployment(stored); err != nil || blob.Name != "renamed" {
		t.Fatalf("stored blob = %+v %v", blob, err)
	}
	if meta, err := q.MetaOf(ctx, apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, 42); err != nil || meta.Version != 2 || meta.SpecVersion != 1 || meta.CreatedTime != now.UnixMilli() || meta.UpdatedSeq != 101 || meta.UpdatedActor != -7 {
		t.Fatalf("row meta = %+v %v", meta, err)
	}
	deleted, err := q.DeploymentDeleteEvent(ctx, 42, 102, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !deleted.Deleted() || deleted.Author != -7 || deleted.Seq != 102 || deleted.Version != 2 || deleted.SpecVersion != updated.SpecVersion || !reflect.DeepEqual(deleted.Value, updated.Value) {
		t.Fatalf("delete metadata = %+v", deleted)
	}
	if m := DeploymentMutation(deleted).Wire(); m.Delete == nil {
		t.Fatalf("delete mutation = %+v", m)
	}
	commit(102, deleted)
	if _, err := q.GetLatestDeploymentEvent(ctx, 42); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted deployment still current: %v", err)
	}
	if rows, err := q.ListRetainedDeploymentVersions(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("unpinned versions retained after delete: %d, %v", len(rows), err)
	}
	for _, id := range []int64{42, 43} {
		if event, err := q.DeploymentUpdateEvent(ctx, id, 103, now, def); !errors.Is(err, sql.ErrNoRows) || event != nil {
			t.Fatalf("update deleted/missing %d: %+v %v", id, event, err)
		}
		if event, err := q.DeploymentDeleteEvent(ctx, id, 103, now); !errors.Is(err, sql.ErrNoRows) || event != nil {
			t.Fatalf("delete deleted/missing %d: %+v %v", id, event, err)
		}
	}
	history, err := q.ListDeploymentEvents(ctx, 42)
	if err != nil || len(history) != 3 {
		t.Fatalf("history = %d rows, %v", len(history), err)
	}
	if !reflect.DeepEqual(history[0], created) || !reflect.DeepEqual(history[1], updated) || !reflect.DeepEqual(history[2], deleted) {
		t.Fatalf("history differs from the builders\n%+v\n%+v\n%+v", history[0], history[1], history[2])
	}
	tombstones, err := q.ListDeletedDeploymentEvents(ctx)
	if err != nil || len(tombstones) != 1 || !reflect.DeepEqual(tombstones[0], deleted) {
		t.Fatalf("deleted listing = %+v, %v", tombstones, err)
	}
	seq, err := q.GetGlobalSeq(context.Background())
	if err != nil || seq != 102 {
		t.Fatalf("global counter = %d, %v", seq, err)
	}
}
