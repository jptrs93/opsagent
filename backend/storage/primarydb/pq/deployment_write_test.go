package pq

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestDeploymentWritersPreserveAttributionAndDeleteFacets(t *testing.T) {
	q := Open(filepath.Join(t.TempDir(), "primary.db"))
	defer q.Close()
	ctx := apigen.Context{Ctx: t.Context(), User: &apigen.InternalUser{ID: 7, Delegated: true}}
	def := &apigen.Deployment{Name: "app", SpaceID: 1}
	now := time.UnixMilli(time.Now().UnixMilli())
	err := q.Tx(ctx, func(q *Queries) error {
		created, err := q.WriteDeploymentCreate(ctx, 42, 100, now, def)
		if err != nil {
			return err
		}
		if created.DeploymentID != 42 || created.Seq != 100 || created.Author != -7 || created.Version != 1 || !created.CreatedTime.Equal(now) || !created.EventTime.Equal(now) {
			t.Fatalf("create metadata = %+v", created)
		}
		if created.Value.Version != 1 || created.Value.SpecVersion != 1 || !created.Value.CreatedTime.Equal(now) {
			t.Fatalf("create payload facts = %+v", created.Value)
		}
		if event, err := q.WriteDeploymentUpdate(ctx, 42, 101, now.Add(time.Second), def); !errors.Is(err, ErrDeploymentUnchanged) || event != nil {
			t.Fatalf("unchanged update: %+v %v", event, err)
		}
		def.Name = "renamed"
		updated, err := q.WriteDeploymentUpdate(ctx, 42, 101, now.Add(time.Second), def)
		if err != nil {
			return err
		}
		if updated.Author != -7 || updated.Seq != 101 || updated.Version != 2 || updated.NameVersion != 2 || updated.SpecVersion != created.SpecVersion || updated.SpaceVersion != created.SpaceVersion || !updated.CreatedTime.Equal(created.CreatedTime) || !updated.EventTime.Equal(now.Add(time.Second)) {
			t.Fatalf("update metadata = %+v", updated)
		}
		if updated.Value.Version != 2 || updated.Value.SpecVersion != 1 || !updated.Value.CreatedTime.Equal(now) {
			t.Fatalf("update payload facts = %+v", updated.Value)
		}
		previous, err := q.GetLatestDeploymentEvent(ctx, 42)
		if err != nil {
			return err
		}
		deleted, err := q.WriteDeploymentDelete(ctx, 42, 102, now.Add(2*time.Second))
		if err != nil {
			return err
		}
		carried := updated.Value
		carried.Version = deleted.Version
		if !deleted.Deleted() || deleted.Author != -7 || deleted.Seq != 102 || deleted.Version != 3 || deleted.NameVersion != updated.NameVersion || deleted.SpecVersion != updated.SpecVersion || deleted.SpaceVersion != updated.SpaceVersion || !reflect.DeepEqual(deleted.Value, carried) {
			t.Fatalf("delete metadata = %+v", deleted)
		}
		row, err := q.GetLatestDeploymentEvent(ctx, 42)
		if err != nil {
			return err
		}
		var value, previousValue []byte
		var specChanged, spaceChanged, nameChanged int64
		if err := q.db.QueryRowContext(ctx, `SELECT value, spec_changed, space_assignment_changed, name_changed FROM deployment_event_log WHERE id = ?`, row.EventID).Scan(&value, &specChanged, &spaceChanged, &nameChanged); err != nil {
			return err
		}
		if err := q.db.QueryRowContext(ctx, `SELECT value FROM deployment_event_log WHERE id = ?`, previous.EventID).Scan(&previousValue); err != nil {
			return err
		}
		if !bytes.Equal(value, previousValue) || specChanged != 0 || spaceChanged != 0 || nameChanged != 0 {
			t.Fatal("delete changed the stored value or retained change flags")
		}
		stored, err := apigen.DecodeDeployment(value)
		if err != nil {
			return err
		}
		if stored.Version != 0 || stored.SpecVersion != 0 || !stored.CreatedTime.IsZero() {
			t.Fatalf("blob carries row facts: %+v", stored)
		}
		for _, event := range []*apigen.DeploymentEvent{created, updated, deleted} {
			row, err := q.GetDeploymentEventByVersion(ctx, GetDeploymentEventByVersionParams{DeploymentID: 42, Version: int64(event.Version)})
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(event, row) {
				t.Fatal("writer returned a different projection from reads")
			}
		}
		for _, id := range []int64{42, 43} {
			if event, err := q.WriteDeploymentUpdate(ctx, id, 103, now, def); !errors.Is(err, sql.ErrNoRows) || event != nil {
				t.Fatalf("update deleted/missing %d: %+v %v", id, event, err)
			}
		}
		// Delete assumes its caller checked that the latest event is live.
		if event, err := q.WriteDeploymentDelete(ctx, 43, 103, now); !errors.Is(err, sql.ErrNoRows) || event != nil {
			t.Fatalf("delete missing: %+v %v", event, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := q.ListDeploymentEvents(ctx, 42)
	if err != nil || len(rows) != 3 {
		t.Fatalf("history = %d rows, %v", len(rows), err)
	}
	// These methods stamp the supplied sequence; the transaction owner alone
	// decides whether to persist the global counter and publish the update.
	seq, err := q.GetGlobalSeq(context.Background())
	if err != nil || seq != 0 {
		t.Fatalf("writer changed global counter: %d, %v", seq, err)
	}
}
